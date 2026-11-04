package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Opt-in read-only acceptance check. Uses the router's exact downloaded certificate.
func TestLiveRouterWebSocket(t *testing.T) {
	if os.Getenv("CG_LIVE_TEST") != "1" {
		t.Skip("opt-in router check")
	}
	caPath := os.Getenv("CG_LIVE_CA")
	if caPath == "" {
		caPath = "dev-data/router-server.crt"
	}
	ca, e := os.ReadFile(caPath)
	if e != nil {
		t.Fatal(e)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		t.Fatal("invalid CA")
	}
	tc := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12, ServerName: os.Getenv("CG_LIVE_TLS_NAME")}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tc}, Jar: jar, Timeout: 10 * time.Second}
	credsPath := os.Getenv("CG_LIVE_CREDENTIALS")
	if credsPath == "" {
		credsPath = "credentials.txt"
	}
	creds, e := os.ReadFile(credsPath)
	if e != nil {
		t.Fatal(e)
	}
	var pw string
	for _, line := range strings.Split(string(creds), "\n") {
		if strings.HasPrefix(line, "密码: ") {
			pw = strings.TrimSpace(strings.TrimPrefix(line, "密码: "))
		}
	}
	if pw == "" {
		t.Fatal("missing password")
	}
	base := os.Getenv("CG_LIVE_URL")
	if base == "" {
		base = "https://192.168.10.1:9088"
	}
	b, _ := json.Marshal(map[string]string{"username": "admin", "password": pw})
	req, _ := http.NewRequest("POST", base+"/api/login", bytes.NewReader(b))
	req.Header.Set("Origin", base)
	req.Header.Set("Content-Type", "application/json")
	res, e := client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("login status %d", res.StatusCode)
	}
	u, _ := url.Parse(base)
	h := http.Header{"Origin": []string{base}}
	for _, c := range jar.Cookies(u) {
		h.Add("Cookie", c.String())
	}
	dial := websocket.Dialer{TLSClientConfig: tc, HandshakeTimeout: 5 * time.Second}
	conn, _, e := dial.Dial("wss"+strings.TrimPrefix(base, "https")+"/ws", h)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	for i := 0; i < 2; i++ {
		var snap struct {
			Type   string   `json:"type"`
			Status Snapshot `json:"status"`
			Events []Event  `json:"events"`
		}
		if e = conn.ReadJSON(&snap); e != nil {
			t.Fatal(e)
		}
		if snap.Type != "snapshot" || snap.Status.Demo {
			t.Fatal("not a real snapshot")
		}
		t.Logf("real websocket frame=%d historical_events=%d", i+1, len(snap.Events))
	}
}
