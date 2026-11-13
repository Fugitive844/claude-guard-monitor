package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestSSHCheckDraftAndSavedCredentials(t *testing.T) {
	h, _ := serveTestSSH(t, func(string, ssh.Channel) {})
	s := sshTestService(t)
	saved := saveTestSSH(t, s, h)
	s.app.sessions["check-test"] = session{Expiry: time.Now().Add(time.Hour), CSRF: "check-csrf"}
	before, _ := os.ReadFile(s.hostsPath())
	check := func(host SSHHost, token string) int {
		body, _ := json.Marshal(host)
		r := httptest.NewRequest("POST", "http://guard.test/api/speed/ssh/check", bytes.NewReader(body))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Origin", "http://guard.test")
		r.Header.Set("Cookie", "cg_session=check-test")
		r.Header.Set("X-CSRF-Token", token)
		w := httptest.NewRecorder()
		s.app.handler().ServeHTTP(w, r)
		if bytes.Contains(w.Body.Bytes(), []byte(h.Password)) {
			t.Fatal("credential leaked")
		}
		return w.Code
	}
	if check(saved.public(), "") != 403 {
		t.Fatal("CSRF accepted")
	}
	if check(saved.public(), "check-csrf") != 200 {
		t.Fatal("saved credential not reused")
	}
	h.Name = ""
	if check(h, "check-csrf") != 200 {
		t.Fatal("unsaved host check failed")
	}
	h.Password = "wrong"
	if check(h, "check-csrf") != 502 {
		t.Fatal("wrong password accepted")
	}
	h.Port = 0
	if check(h, "check-csrf") != 400 {
		t.Fatal("invalid port accepted")
	}
	after, _ := os.ReadFile(s.hostsPath())
	if !bytes.Equal(before, after) {
		t.Fatal("connection check changed stored hosts or fingerprint")
	}
}
