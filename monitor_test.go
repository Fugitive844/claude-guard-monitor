package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testApp(t *testing.T) *App {
	t.Helper()
	db, e := openStore(filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	c := defaults()
	c.PasswordHash = passwordHash("test-password")
	return &App{cfg: c, demo: true, db: db, sessions: map[string]session{}, loginAttempts: map[string]time.Time{}, ctx: context.Background(), events: make(chan Event, 8)}
}

func TestTimelineRangeAndPagination(t *testing.T) {
	now := time.Now().UnixMilli()
	f, end, _, err := queryRange(url.Values{}, now)
	if err != nil || end != now || end-f != 86400000 {
		t.Fatal("invalid default range")
	}
	for _, query := range []string{"from=1", "from=abc&to=9", "from=20&to=10", "from=0&to=2678400001", "before=-1", "before=bad"} {
		q, _ := url.ParseQuery(query)
		if _, _, _, e := queryRange(q, now); e == nil {
			t.Fatalf("accepted invalid range %s", query)
		}
	}
	a := testApp(t)
	a.sessions["timeline-test"] = session{Expiry: time.Now().Add(time.Hour)}
	// Equal timestamps span page boundaries; outside-window records must never leak in.
	for i := 0; i < 56; i++ {
		ts := int64(2000)
		if i == 0 {
			ts = 999
		}
		if i == 55 {
			ts = 3000
		}
		if _, e := a.db.Exec("INSERT INTO lifecycle(ts,kind,message) VALUES(?,?,?)", ts, "test", fmt.Sprint(i)); e != nil {
			t.Fatal(e)
		}
	}
	read := func(before int64) struct {
		Items []map[string]any `json:"items"`
		More  bool             `json:"has_more"`
		Next  int64            `json:"next_before"`
	} {
		r := httptest.NewRequest("GET", fmt.Sprintf("http://guard.test/api/timeline?from=1000&to=3000&before=%d", before), nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Cookie", "cg_session=timeline-test")
		w := httptest.NewRecorder()
		a.handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var page struct {
			Items []map[string]any `json:"items"`
			More  bool             `json:"has_more"`
			Next  int64            `json:"next_before"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &page); e != nil {
			t.Fatal(e)
		}
		return page
	}
	p1 := read(0)
	p2 := read(p1.Next)
	if len(p1.Items) != 50 || !p1.More || len(p2.Items) != 4 || p2.More {
		t.Fatal("incorrect bounded pages", len(p1.Items), len(p2.Items))
	}
	seen := map[float64]bool{}
	for _, p := range append(p1.Items, p2.Items...) {
		id := p["id"].(float64)
		if seen[id] || p["time"].(float64) != 2000 {
			t.Fatal("duplicate or out of range")
		}
		seen[id] = true
	}
}
func TestEventTimePaginationAndExport(t *testing.T) {
	a := testApp(t)
	a.sessions["events-test"] = session{Expiry: time.Now().Add(time.Hour)}
	var events []Event
	for i := 0; i < 54; i++ {
		events = append(events, Event{First: 2000, Last: 2000, Src: "client", Dst: "target", Domains: "claude.ai", Proto: "TCP", Packets: 1})
	}
	events = append(events, Event{First: 999, Last: 999, Domains: "claude.ai"}, Event{First: 3000, Last: 3000, Domains: "claude.ai"}, Event{First: 2000, Last: 2000, Domains: "other.example"})
	if err := a.db.insertBatch(events); err != nil {
		t.Fatal(err)
	}
	request := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://guard.test"+path, nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Cookie", "cg_session=events-test")
		w := httptest.NewRecorder()
		a.handler().ServeHTTP(w, r)
		return w
	}
	type page struct {
		Items []Event `json:"items"`
		More  bool    `json:"has_more"`
		Next  int64   `json:"next_before"`
	}
	var first, second page
	w := request("/api/events?from=1000&to=3000&q=claude&limit=100000")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	w = request(fmt.Sprintf("/api/events?from=1000&to=3000&q=claude&before=%d", first.Next))
	if err := json.Unmarshal(w.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 50 || !first.More || len(second.Items) != 4 || second.More {
		t.Fatal("unbounded or missing records")
	}
	seen := map[int64]bool{}
	for _, x := range append(first.Items, second.Items...) {
		if seen[x.ID] || x.Last != 2000 || x.Domains != "claude.ai" {
			t.Fatal("duplicate or filter bypass")
		}
		seen[x.ID] = true
	}
	w = request("/api/export?from=1000&to=3000&q=claude")
	if w.Code != 200 || strings.Count(w.Body.String(), "\n") != 55 || strings.Contains(w.Body.String(), "other.example") {
		t.Fatal("export differs from filters")
	}
	for _, path := range []string{"/api/events?from=0&to=2678400001", "/api/export?from=0&to=2678400001"} {
		if w = request(path); w.Code != 400 {
			t.Fatal("range limit missing", path)
		}
	}
}

func TestPacketDecode(t *testing.T) {
	b := make([]byte, 40)
	b[0] = 0x45
	b[9] = 6
	binary.BigEndian.PutUint16(b[2:4], 40)
	copy(b[12:16], []byte{192, 0, 2, 10})
	copy(b[16:20], []byte{160, 79, 104, 10})
	binary.BigEndian.PutUint16(b[20:22], 50123)
	binary.BigEndian.PutUint16(b[22:24], 443)
	v, e := parsePacket(b)
	if e != nil || v.Src != "192.0.2.10" || v.Dst != "160.79.104.10" || v.DPort != 443 || v.SPort != 50123 || v.Bytes != 40 {
		t.Fatalf("%+v %v", v, e)
	}
	for n := 0; n < 20; n++ {
		if _, e = parsePacket(b[:n]); e == nil {
			t.Fatal("truncated accepted")
		}
	}
	b[6] = 0
	b[7] = 1
	v, e = parsePacket(b)
	if e != nil || v.DPort != 0 {
		t.Fatal("fragment ports guessed")
	}
	b6 := make([]byte, 48)
	b6[0] = 0x60
	b6[6] = 17
	b6[8] = 0x20
	b6[24] = 0x26
	binary.BigEndian.PutUint16(b6[42:44], 443)
	v, e = parsePacket(b6)
	if e != nil || v.Family != 6 || v.Proto != "UDP" || v.DPort != 443 {
		t.Fatalf("IPv6: %+v %v", v, e)
	}
}
func TestCounterSeparation(t *testing.T) {
	raw := []byte(`{"nftables":[{"chain":{}},{"rule":{"comment":"CG_MONITOR_LOG4","expr":[{"counter":{"packets":100,"bytes":400}}]}},{"rule":{"comment":"Claude known destinations: no direct forwarding","expr":[{"counter":{"packets":55,"bytes":11581}}]}},{"rule":{"comment":"Claude known destinations IPv6: no direct forwarding","expr":[{"counter":{"packets":3,"bytes":200}}]}}]}`)
	s, l, e := readCounters(raw)
	if e != nil || s.P4 != 55 || s.P6 != 3 || len(l) != 1 || s.Logging {
		t.Fatalf("%+v %v", s, e)
	}
}
func TestSQLiteFilterRetention(t *testing.T) {
	a := testApp(t)
	now := time.Now().UnixMilli()
	if e := a.db.insertBatch([]Event{{First: now, Last: now, Src: "192.0.2.10", Dst: "160.79.104.10", Proto: "TCP", Domains: "claude.ai", Packets: 3}, {First: 1, Last: 1, Src: "x", Dst: "y", Packets: 1}}); e != nil {
		t.Fatal(e)
	}
	v, e := a.db.events("claude", 0, 100)
	if e != nil || len(v) != 1 || v[0].Packets != 3 {
		t.Fatal(v, e)
	}
	v, e = a.db.events("' OR 1=1 --", 0, 100)
	if e != nil || len(v) != 0 {
		t.Fatal("unsafe filter", v, e)
	}
	if e = a.db.prune(a.cfg); e != nil {
		t.Fatal(e)
	}
	v, e = a.db.events("", 0, 100)
	if e != nil || len(v) != 1 {
		t.Fatal("retention", v, e)
	}
}
func TestLoginOriginAndCSRF(t *testing.T) {
	a := testApp(t)
	h := a.handler()
	req := func(method, path, body, origin, cookie, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://guard.test"+path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", csrf)
		if cookie != "" {
			r.Header.Set("Cookie", cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := req("GET", "/api/status", "", "", "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := req("POST", "/api/login", `{"username":"admin","password":"test-password"}`, "http://evil.test", "", ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	w := req("POST", "/api/login", `{"username":"admin","password":"test-password"}`, "http://guard.test", "", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var data map[string]string
	json.Unmarshal(w.Body.Bytes(), &data)
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("cookie flags")
	}
	if w = req("POST", "/api/lifecycle", `{"action":"disable","confirm":"解除防护"}`, "http://guard.test", cookie.String(), ""); w.Code != 403 {
		t.Fatal("CSRF not rejected", w.Code)
	}
	if w = req("POST", "/api/lifecycle", `{"action":"disable","confirm":""}`, "http://guard.test", cookie.String(), data["csrf"]); w.Code != 400 {
		t.Fatal("confirmation not enforced", w.Code)
	}
	r := httptest.NewRequest("GET", "http://guard.test/", nil)
	r.RemoteAddr = "8.8.8.8:4321"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("non-LAN allowed")
	}
}
func TestConsolePageRoutes(t *testing.T) {
	a := testApp(t)
	h := a.handler()
	for _, path := range []string{"/", "/overview", "/blocks", "/timeline", "/speed", "/speed-history", "/ssh", "/manage"} {
		t.Run(path, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://guard.test"+path, nil)
			r.RemoteAddr = "127.0.0.1:1234"
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") || !strings.Contains(w.Body.String(), `id="loginForm"`) {
				t.Fatal("page refresh must load the console shell", w.Code)
			}
			r.RemoteAddr = "8.8.8.8:4321"
			w = httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatal("page route bypassed management network restriction", w.Code)
			}
		})
	}
}

func TestWebSocketSnapshotAndOrigin(t *testing.T) {
	a := testApp(t)
	a.sessions["test-session"] = session{Expiry: time.Now().Add(time.Hour), CSRF: "csrf"}
	a.snapshot = Snapshot{P4: 42, Version: version}
	s := httptest.NewServer(a.handler())
	defer s.Close()
	header := http.Header{"Origin": {s.URL}, "Cookie": {"cg_session=test-session"}}
	c, _, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http")+"/ws", header)
	if e != nil {
		t.Fatal(e)
	}
	var msg struct {
		Status Snapshot `json:"status"`
	}
	if e = c.ReadJSON(&msg); e != nil || msg.Status.P4 != 42 {
		t.Fatal(msg, e)
	}
	c.Close()
	header.Set("Origin", "http://evil.test")
	if c, _, e = websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(s.URL, "http")+"/ws", header); e == nil {
		c.Close()
		t.Fatal("cross-origin WS accepted")
	}
}
func TestTemplateValidation(t *testing.T) {
	g := defaults().Guard
	g.DNSIP = "127.0.0.2"
	g.DNSPort = 5353
	g.DNSSources = []string{"::1", "192.168.2.1"}
	g.ClientCIDRs = []string{"192.168.2.0/24"}
	g.RouterIPs = []string{"192.168.2.1"}
	if e := validGuard(g); e != nil {
		t.Fatal(e)
	}
	for _, n := range []string{"guard.lua", "capture.sh", "observer.lua"} {
		b, e := renderBundle(n, g)
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(string(b), "192.168.10.1") {
			t.Fatal("old source retained", n)
		}
		if n == "guard.lua" && !strings.Contains(string(b), "sock:connect('127.0.0.2',5353)") {
			t.Fatal("DNS not replaced")
		}
	}
	g.DNSIP = "127.0.0.1'; os.execute('bad')"
	if validGuard(g) == nil {
		t.Fatal("injection accepted")
	}
	if csvCell("=2+2") == "=2+2" {
		t.Fatal("CSV formula not escaped")
	}
}
