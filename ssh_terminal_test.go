package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

func TestSSHTerminalBridgeAndSessionRevocation(t *testing.T) {
	closed := make(chan struct{}, 2)
	h, auth := serveTestSSH(t, func(command string, c ssh.Channel) {
		defer func() { closed <- struct{}{} }()
		if command != "shell" {
			return
		}
		io.Copy(c, c)
	})
	s := sshTestService(t)
	h = saveTestSSH(t, s, h)
	s.app.sessions["terminal-test"] = session{Expiry: time.Now().Add(time.Hour), CSRF: "terminal-csrf"}
	server := httptest.NewServer(s.app.handler())
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/ssh/terminal"
	headers := http.Header{"Origin": {server.URL}, "Cookie": {"cg_session=terminal-test"}}
	bad := headers.Clone()
	bad.Set("Origin", "http://other.test")
	if c, resp, err := websocket.DefaultDialer.Dial(url, bad); err == nil {
		c.Close()
		t.Fatal("cross-origin terminal accepted")
	} else if resp == nil || resp.StatusCode != 403 {
		t.Fatal("unexpected origin response", resp, err)
	}
	noLogin := headers.Clone()
	noLogin.Del("Cookie")
	if c, resp, err := websocket.DefaultDialer.Dial(url, noLogin); err == nil {
		c.Close()
		t.Fatal("anonymous terminal accepted")
	} else if resp == nil || resp.StatusCode != 401 {
		t.Fatal("unexpected session response", resp, err)
	}
	open := func(token string) *websocket.Conn {
		c, _, err := websocket.DefaultDialer.Dial(url, headers)
		if err != nil {
			t.Fatal(err)
		}
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		if err = c.WriteJSON(terminalMessage{Type: "open", CSRF: token, HostID: h.ID, Cols: 80, Rows: 24}); err != nil {
			t.Fatal(err)
		}
		return c
	}
	badConn := open("wrong")
	if _, _, err := badConn.ReadMessage(); err == nil {
		t.Fatal("wrong CSRF accepted")
	}
	badConn.Close()
	if auth.Load() != 0 {
		t.Fatal("SSH authentication attempted before CSRF check")
	}
	for _, revoke := range []bool{false, true} {
		c := open("terminal-csrf")
		var ready map[string]string
		if err := c.ReadJSON(&ready); err != nil || ready["type"] != "ready" {
			t.Fatal("terminal not ready", ready, err)
		}
		if err := c.WriteJSON(terminalMessage{Type: "resize", Cols: 120, Rows: 40}); err != nil {
			t.Fatal(err)
		}
		payload := []byte("echo 测试\r\x03\x1b[A")
		if err := c.WriteJSON(terminalMessage{Type: "input", Data: string(payload)}); err != nil {
			t.Fatal(err)
		}
		var got []byte
		for len(got) < len(payload) {
			kind, data, err := c.ReadMessage()
			if err != nil || kind != websocket.BinaryMessage {
				t.Fatal("terminal output", kind, err)
			}
			got = append(got, data...)
		}
		if !bytes.Equal(payload, got) {
			t.Fatal("terminal input/output changed")
		}
		if revoke {
			s.app.mu.Lock()
			delete(s.app.sessions, "terminal-test")
			s.app.mu.Unlock()
			if _, _, err := c.ReadMessage(); err == nil {
				t.Fatal("terminal survived logout")
			}
		}
		c.Close()
		select {
		case <-closed:
		case <-time.After(3 * time.Second):
			t.Fatal("remote session was not closed")
		}
	}
}

func TestSpeedHistoryScopes(t *testing.T) {
	s := sshTestService(t)
	for i := 0; i < 60; i++ {
		for _, id := range []string{"proxy-a", "ssh:host-a"} {
			r := SpeedResult{Time: int64(i + 1), NodeID: id}
			if err := s.app.db.saveSpeed(&r); err != nil {
				t.Fatal(err)
			}
		}
	}
	for scope, id := range map[string]string{"proxy": "proxy-a", "ssh": "ssh:host-a"} {
		first, err := s.app.db.speedHistory(0, 100, 0, "", scope)
		if err != nil || len(first) != 51 {
			t.Fatal(scope, len(first), err)
		}
		for _, r := range first {
			if r.NodeID != id {
				t.Fatal("mixed history", scope, r.NodeID)
			}
		}
		next, err := s.app.db.speedHistory(0, 100, first[49].ID, "", scope)
		if err != nil || len(next) != 10 {
			t.Fatal("scope pagination", scope, len(next), err)
		}
	}
}
