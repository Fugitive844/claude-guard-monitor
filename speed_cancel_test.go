package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestStoppedProxyKeepsMeasuredBytes(t *testing.T) {
	for _, direction := range []string{"download", "upload"} {
		t.Run(direction, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cfg := defaultSpeedSettings()
			var client *http.Client
			if direction == "download" {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/octet-stream")
					io.CopyN(w, &speedPayload{}, 64*1024)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				}))
				defer srv.Close()
				cfg.Target, cfg.DownloadURL = "custom", srv.URL
				client = srv.Client()
			} else {
				client = &http.Client{Transport: speedRoundTrip(func(r *http.Request) (*http.Response, error) {
					io.Copy(io.Discard, r.Body)
					time.Sleep(time.Millisecond)
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
				})}
			}
			r := measureSpeed(ctx, client, cfg, SpeedResult{NodeID: "node", Direction: direction}, func(r SpeedResult) {
				if r.Bytes > 0 {
					cancel()
				}
			})
			if r.Status != "cancelled" || r.Bytes <= 0 || r.Bytes >= 1024*1024 || r.Seconds <= 0 || r.Mbps != float64(r.Bytes)*8/r.Seconds/1e6 {
				t.Fatalf("stopped transfer lost data: %+v", r)
			}
			s := sshTestService(t)
			if err := s.app.db.saveSpeed(&r); err != nil {
				t.Fatal(err)
			}
			rows, err := s.app.db.latestSpeed([]SpeedNode{{ID: "node"}}, true)
			if err != nil || len(rows) != 1 || rows[0].Mbps != r.Mbps || rows[0].Bytes != r.Bytes {
				t.Fatal(rows, err)
			}
		})
	}
}

func TestStoppedNativeSSHKeepsBothDirections(t *testing.T) {
	h, _ := serveTestSSH(t, func(command string, c ssh.Channel) {
		if strings.Contains(command, "head") {
			for {
				if _, err := io.CopyN(c, &speedPayload{}, 32*1024); err != nil {
					return
				}
				time.Sleep(time.Millisecond)
			}
		}
		buffer := make([]byte, 32768)
		for {
			if _, err := c.Read(buffer); err != nil {
				return
			}
			time.Sleep(time.Millisecond)
		}
	})
	s := sshTestService(t)
	h = saveTestSSH(t, s, h)
	for _, direction := range []string{"download", "upload"} {
		t.Run(direction, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client, err := s.connectSSH(ctx, h)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			cfg := defaultSpeedSettings()
			cfg.UploadMiB = 500
			r, err := measureSSH(ctx, client, SSHSpeedRequest{Seconds: 5}, cfg, SpeedResult{Mode: "ssh", Direction: direction}, func(r SpeedResult) {
				if r.Bytes > 0 {
					cancel()
				}
			})
			r = cancelledSpeed(r)
			if err == nil || ctx.Err() == nil || r.Bytes <= 0 || r.Mbps <= 0 || r.Seconds <= 0 {
				t.Fatal(r, err)
			}
			if direction == "upload" && r.RateBasis != "sender" {
				t.Fatal("unacknowledged upload must identify sender statistics", r)
			}
		})
	}
}

func TestStoppedIPerfRetainsIntervalsAndMetrics(t *testing.T) {
	for _, source := range []string{"", "peer-source"} {
		for _, direction := range []string{"download", "upload"} {
			p := newIPerfProgress(SpeedResult{Mode: "iperf3", Protocol: "udp", SourceHostID: source, Direction: direction}, func(SpeedResult) {})
			p.Write([]byte("[ 5] 0.00-1.00 sec 1 MBytes 8.00 Mbits/sec 0.20 ms 1/100 (1%)\n"))
			p.Write([]byte("[ 5] 1.00-2.00 sec 2 MBytes 16.00 Mbits/sec 0.30 ms 1/300 (0.33%)"))
			r, err := p.result()
			if err == nil {
				t.Fatal("interrupted run unexpectedly has receiver summary")
			}
			r = cancelledSpeed(r)
			if r.Bytes != 3*1024*1024 || r.Seconds != 2 || r.Mbps != float64(r.Bytes)*8/2/1e6 || len(r.Samples) != 2 || r.IntervalMbps != nil || r.Packets != 400 || r.LostPackets != 2 || r.LostPercent != 0.5 || r.JitterMS != 0.3 {
				t.Fatal(r)
			}
			if (r.RateBasis == "sender") != (direction == "upload") {
				t.Fatal(r)
			}
		}
	}
}

func TestPreviousSpeedMatchesLinkProtocolAndPort(t *testing.T) {
	s := sshTestService(t)
	base := SpeedResult{NodeID: "ssh:target", SourceHostID: "source-a", Direction: "upload", Mode: "iperf3", Protocol: "tcp", Status: "complete", Bytes: 1000000, Seconds: 1, Mbps: 8, TestConfig: &SSHSpeedRequest{Port: 5201}}
	save := func(r SpeedResult) {
		t.Helper()
		if err := s.app.db.saveSpeed(&r); err != nil {
			t.Fatal(err)
		}
	}
	save(base)
	partial := base
	partial.Status = "cancelled"
	partial.Bytes = 2000000
	partial.Mbps = 16
	save(partial)
	failed := base
	failed.Status = "error"
	failed.Bytes = 0
	save(failed)
	other := base
	other.SourceHostID = "source-b"
	other.Mbps = 24
	save(other)
	other = base
	other.Protocol = "udp"
	other.Mbps = 32
	save(other)
	other = base
	other.TestConfig = &SSHSpeedRequest{Port: 5202}
	other.Mbps = 40
	save(other)
	other = base
	other.Mode = "ssh"
	other.SourceHostID = ""
	other.TestConfig = nil
	other.Mbps = 48
	save(other)
	empty := base
	empty.Direction = "download"
	empty.Status = "cancelled"
	empty.Bytes = 0
	save(empty)
	rows, err := s.app.db.latestSpeed([]SpeedNode{{ID: "ssh:target"}}, true)
	if err != nil || len(rows) != 5 {
		t.Fatal(rows, err)
	}
	for _, r := range rows {
		if r.Direction != "upload" || r.Status == "error" {
			t.Fatal(r)
		}
		if r.SourceHostID == "source-a" && r.Protocol == "tcp" && r.TestConfig.Port == 5201 && r.Mbps != 16 {
			t.Fatal("valid stopped measurement must survive a later failed attempt", r)
		}
	}
}
