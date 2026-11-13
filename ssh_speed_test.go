package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func sshTestService(t *testing.T) *SpeedService {
	t.Helper()
	a := testApp(t)
	a.configPath = filepath.Join(t.TempDir(), "config.json")
	a.speed = newSpeedService(a)
	return a.speed
}

func TestSSHHostCredentialsAndAPI(t *testing.T) {
	s := sshTestService(t)
	h := SSHHost{Name: "test", Host: "127.0.0.1", Port: 22, Username: "test", Auth: "password", Password: "secret-ssh-password"}
	if err := s.saveHost(h); err != nil {
		t.Fatal(err)
	}
	hosts, _ := s.readHosts()
	h = hosts[0]
	s.app.sessions["ssh-test"] = session{Expiry: time.Now().Add(time.Hour), CSRF: "test-csrf"}
	request := func(method, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://guard.test/api/speed/ssh/hosts", strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Cookie", "cg_session=ssh-test")
		r.Header.Set("Origin", "http://guard.test")
		r.Header.Set("X-CSRF-Token", token)
		w := httptest.NewRecorder()
		s.app.handler().ServeHTTP(w, r)
		return w
	}
	w := request("GET", "", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), h.Password) || !strings.Contains(w.Body.String(), `"has_secret":true`) {
		t.Fatal("credential disclosure or missing host", w.Code)
	}
	if request("DELETE", `{"id":"`+h.ID+`"}`, "").Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	edit := h.public()
	edit.Name = "renamed"
	if err := s.saveHost(edit); err != nil {
		t.Fatal(err)
	}
	hosts, _ = s.readHosts()
	if hosts[0].Password != h.Password {
		t.Fatal("blank edit lost credential")
	}
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	edit.Auth = "key"
	edit.PrivateKey = string(pem.EncodeToMemory(block))
	if err = s.saveHost(edit); err != nil {
		t.Fatal(err)
	}
	hosts, _ = s.readHosts()
	if hosts[0].Password != "" || hosts[0].PrivateKey == "" {
		t.Fatal("auth switch did not replace credential")
	}
	if request("DELETE", `{"id":"`+h.ID+`"}`, "test-csrf").Code != 200 {
		t.Fatal("delete failed")
	}
	hosts, _ = s.readHosts()
	if len(hosts) != 0 {
		t.Fatal("host remains")
	}
}

func TestSSHHostPendingCredentials(t *testing.T) {
	s := sshTestService(t)
	h := SSHHost{Name: "pending", Host: "192.0.2.10", Port: 22, Username: "root", Auth: "password"}
	if err := s.saveHost(h); err != nil {
		t.Fatal(err)
	}
	hosts, err := s.readHosts()
	if err != nil || len(hosts) != 1 {
		t.Fatal("pending host not saved", err)
	}
	h = hosts[0]
	if h.public().HasSecret {
		t.Fatal("pending host marked ready")
	}
	if _, err := h.authMethod(); err == nil {
		t.Fatal("empty password accepted for connection")
	}
	h.Password = "new-password"
	if err := s.saveHost(h); err != nil {
		t.Fatal(err)
	}
	hosts, _ = s.readHosts()
	if len(hosts) != 1 || !hosts[0].public().HasSecret {
		t.Fatal("credential update created a duplicate or failed")
	}
	h = hosts[0].public()
	h.Name = "renamed"
	if err := s.saveHost(h); err != nil {
		t.Fatal(err)
	}
	hosts, _ = s.readHosts()
	if hosts[0].Password != "new-password" {
		t.Fatal("blank edit cleared saved password")
	}
	h.Auth = "invalid"
	if err := s.saveHost(h); err == nil {
		t.Fatal("invalid auth accepted")
	}
}

func serveTestSSH(t *testing.T, run func(string, ssh.Channel)) (SSHHost, *atomic.Int32) {
	t.Helper()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(key)
	auth := &atomic.Int32{}
	config := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		auth.Add(1)
		if c.User() != "test" || string(p) != "test-password" {
			return nil, fmt.Errorf("invalid auth")
		}
		return nil, nil
	}}
	config.AddHostKey(signer)
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				stop := context.AfterFunc(ctx, func() { conn.Close() })
				defer stop()
				c, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer c.Close()
				go ssh.DiscardRequests(requests)
				for ch := range channels {
					if ch.ChannelType() != "session" {
						ch.Reject(ssh.UnknownChannelType, "session only")
						continue
					}
					channel, requests, e := ch.Accept()
					if e != nil {
						continue
					}
					wg.Add(1)
					go func() {
						defer wg.Done()
						defer channel.Close()
						for r := range requests {
							if r.Type == "pty-req" || r.Type == "window-change" {
								r.Reply(true, nil)
								continue
							}
							if r.Type == "shell" {
								r.Reply(true, nil)
								wg.Add(1)
								go func() { defer wg.Done(); run("shell", channel); channel.Close() }()
								continue
							}
							if r.Type != "exec" {
								r.Reply(false, nil)
								continue
							}
							var command struct{ Command string }
							if ssh.Unmarshal(r.Payload, &command) != nil {
								r.Reply(false, nil)
								return
							}
							r.Reply(true, nil)
							run(command.Command, channel)
							return
						}
					}()
				}
			}()
		}
	}()
	t.Cleanup(func() { cancel(); l.Close(); wg.Wait() })
	return SSHHost{Name: "loopback", Host: "127.0.0.1", Port: l.Addr().(*net.TCPAddr).Port, Username: "test", Auth: "password", Password: "test-password"}, auth
}

func saveTestSSH(t *testing.T, s *SpeedService, h SSHHost) SSHHost {
	t.Helper()
	if err := s.saveHost(h); err != nil {
		t.Fatal(err)
	}
	hosts, err := s.readHosts()
	if err != nil {
		t.Fatal(err)
	}
	return hosts[len(hosts)-1]
}

func TestSSHFingerprintPinnedBeforeAuthentication(t *testing.T) {
	h, auth := serveTestSSH(t, func(_ string, c ssh.Channel) {})
	s := sshTestService(t)
	h = saveTestSSH(t, s, h)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := s.connectSSH(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	hosts, _ := s.readHosts()
	if !strings.HasPrefix(hosts[0].Fingerprint, "SHA256:") {
		t.Fatal("fingerprint not saved")
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, _ := ssh.NewSignerFromKey(other)
	hosts[0].Fingerprint = ssh.FingerprintSHA256(otherSigner.PublicKey())
	if err = s.writeHosts(hosts); err != nil {
		t.Fatal(err)
	}
	_, err = s.connectSSH(ctx, h)
	if err == nil || auth.Load() != 1 {
		t.Fatal("changed host key reached password authentication")
	}
}

func TestNativeSSHTransferAndCancellation(t *testing.T) {
	h, _ := serveTestSSH(t, func(command string, c ssh.Channel) {
		if strings.HasPrefix(command, "head -c ") {
			fields := strings.Fields(command)
			n, _ := strconv.ParseInt(fields[2], 10, 64)
			_, _ = io.CopyN(c, &speedPayload{}, n)
		} else if command == "LC_ALL=C wc -c" {
			n, _ := io.Copy(io.Discard, c)
			fmt.Fprintln(c, n)
		}
		c.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
	})
	s := sshTestService(t)
	h = saveTestSSH(t, s, h)
	for _, direction := range []string{"download", "upload"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		client, err := s.connectSSH(ctx, h)
		if err != nil {
			t.Fatal(err)
		}
		cfg := defaultSpeedSettings()
		cfg.DownloadMiB = 2
		cfg.UploadMiB = 2
		r, err := measureSSH(ctx, client, SSHSpeedRequest{Seconds: 3}, cfg, SpeedResult{Direction: direction, Status: "running"}, func(SpeedResult) {})
		client.Close()
		cancel()
		if err != nil || r.Bytes != 2*1024*1024 || r.Status != "complete" || r.Mbps <= 0 {
			t.Fatalf("%s: %+v %v", direction, r, err)
		}
	}
	blocked, _ := serveTestSSH(t, func(_ string, c ssh.Channel) { time.Sleep(300 * time.Millisecond); io.CopyN(c, &speedPayload{}, 1024) })
	blocked = saveTestSSH(t, s, blocked)
	ctx, cancel := context.WithCancel(context.Background())
	client, err := s.connectSSH(ctx, blocked)
	if err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	defer timer.Stop()
	start := time.Now()
	_, err = measureSSH(ctx, client, SSHSpeedRequest{Seconds: 60}, defaultSpeedSettings(), SpeedResult{Direction: "download"}, func(SpeedResult) {})
	client.Close()
	if err == nil || ctx.Err() == nil || time.Since(start) > time.Second {
		t.Fatal("cancellation did not close transfer")
	}
}

func TestIPerfReceiverStatistics(t *testing.T) {
	for _, protocol := range []string{"tcp", "udp"} {
		b := []byte(`{"end":{"sum_sent":{"bytes":2000000,"seconds":1,"bits_per_second":16000000},"sum_received":{"bytes":900000,"seconds":1,"bits_per_second":7200000,"packets":1000,"lost_packets":100,"lost_percent":10,"jitter_ms":0.25}}}`)
		r, err := parseIPerf(b, SpeedResult{Protocol: protocol})
		if err != nil || r.Bytes != 900000 || r.Mbps != 7.2 {
			t.Fatal(r, err)
		}
		if protocol == "udp" && (r.LostPercent != 10 || r.JitterMS != 0.25) {
			t.Fatal("missing UDP metrics")
		}
	}
	if _, err := parseIPerf([]byte(`{"error":"failed"}`), SpeedResult{}); err == nil {
		t.Fatal("iperf error accepted")
	}
	if _, err := parseIPerf([]byte(`{"end":{"sum_sent":{"bytes":2000}}}`), SpeedResult{}); err == nil {
		t.Fatal("sender-only result accepted")
	}
}

func TestIPerfLocalIntegration(t *testing.T) {
	if os.Getenv("CG_IPERF_TEST") != "1" {
		t.Skip("set CG_IPERF_TEST=1 on Linux with iperf3 and CAP_NET_ADMIN")
	}
	h, _ := serveTestSSH(t, func(command string, c ssh.Channel) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		cmd.Stdin = c
		cmd.Stdout = c
		cmd.Stderr = c.Stderr()
		err := cmd.Run()
		status := uint32(0)
		if err != nil {
			status = 1
		}
		c.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
	})
	s := sshTestService(t)
	h = saveTestSSH(t, s, h)
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	for _, protocol := range []string{"tcp", "udp"} {
		for _, direction := range []string{"download", "upload"} {
			t.Run(protocol+"-"+direction, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				client, err := s.connectSSH(ctx, h)
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				req := SSHSpeedRequest{Seconds: 3, Port: port, Bandwidth: 10, Parallel: 2, Omit: 1, TCPBandwidth: 8, UDPLength: 1000}
				r, err := measureIPerf(ctx, client, h, req, SpeedResult{Protocol: protocol, Direction: direction, TestConfig: &req}, func(SpeedResult) {})
				if err != nil || r.Status != "complete" || r.Bytes == 0 || r.Mbps <= 0 {
					t.Fatalf("result %+v error %v", r, err)
				}
				if protocol == "udp" && r.Packets == 0 {
					t.Fatal("missing UDP packets")
				}
				if len(r.Samples) < 3 || r.Samples[0].End > 1.2 || r.Seconds > 3.5 || r.SenderMbps == nil {
					t.Fatal("missing samples or warmup included", r)
				}
				listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
				if err != nil {
					t.Fatal("remote listener remains after test", err)
				}
				listener.Close()
				b, _ := json.Marshal(r)
				t.Log(string(b))
			})
		}
	}
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		client, err := s.connectSSH(ctx, h)
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		timer := time.AfterFunc(time.Second, cancel)
		defer timer.Stop()
		start := time.Now()
		_, err = measureIPerf(ctx, client, h, SSHSpeedRequest{Seconds: 30, Port: port}, SpeedResult{Protocol: "tcp", Direction: "download"}, func(SpeedResult) {})
		if err == nil || time.Since(start) > 5*time.Second {
			t.Fatal("iperf cancellation failed", err)
		}
		listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		// Closing the SSH transport delivers EOF before the remote shell finishes cleanup.
		deadline := time.Now().Add(2 * time.Second)
		for err != nil && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
			listener, err = net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		}
		if err != nil {
			t.Fatal("remote listener remains after cancel", err)
		}
		listener.Close()
	})
}
