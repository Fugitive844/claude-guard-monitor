package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type speedRoundTrip func(*http.Request) (*http.Response, error)

func (f speedRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCloudflareDownloadSplitsLargeBudget(t *testing.T) {
	var total int64
	requests := 0
	client := &http.Client{Transport: speedRoundTrip(func(r *http.Request) (*http.Response, error) {
		n, _ := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
		if n <= 0 || n > 8*1024*1024 {
			t.Fatalf("oversized sample: %d", n)
		}
		total += n
		requests++
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/octet-stream"}}, Body: io.NopCloser(io.LimitReader(&speedPayload{}, n))}, nil
	})}
	cfg := defaultSpeedSettings()
	cfg.DownloadMiB = 20
	r := measureSpeed(context.Background(), client, cfg, SpeedResult{Direction: "download"}, func(SpeedResult) {})
	if requests != 3 || total != 20*1024*1024 || r.Bytes != total || r.Status != "complete" {
		t.Fatalf("requests %d total %d result %+v", requests, total, r)
	}
}

func TestSpeedUploadAcknowledgementAndPrivacy(t *testing.T) {
	for _, status := range []int{200, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var received int64
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "PUT" || r.Header.Get("Content-Type") != "application/octet-stream" {
					t.Error("upload method or content type")
				}
				received, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(status)
			}))
			defer srv.Close()
			cfg := defaultSpeedSettings()
			cfg.Target = "custom"
			cfg.UploadURL = srv.URL + "/object?signature=secret"
			cfg.UploadMiB = 1
			r := measureSpeed(context.Background(), srv.Client(), cfg, SpeedResult{Direction: "upload"}, func(SpeedResult) {})
			if received != 1024*1024 {
				t.Fatalf("received %d", received)
			}
			if status == 200 && (r.Status != "complete" || r.Bytes != received || r.Mbps <= 0) {
				t.Fatalf("success: %+v", r)
			}
			if status == 403 && (r.Status != "error" || r.Bytes != 0 || r.Mbps != 0) {
				t.Fatalf("rejected upload counted: %+v", r)
			}
		})
	}
	msg := safeSpeedError(&url.Error{Op: "Put", URL: "https://example.com/a?signature=secret", Err: errors.New("connection failed")})
	if strings.Contains(msg, "secret") || strings.Contains(msg, "example.com") {
		t.Fatal("signed URL leaked")
	}
}

func TestSpeedDownloadLimitAndCancellation(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.CopyN(w, &speedPayload{}, 2*1024*1024)
	}))
	defer srv.Close()
	cfg := defaultSpeedSettings()
	cfg.Target = "custom"
	cfg.DownloadURL = srv.URL
	cfg.DownloadMiB = 1
	r := measureSpeed(context.Background(), srv.Client(), cfg, SpeedResult{Direction: "download"}, func(SpeedResult) {})
	if r.Status != "complete" || r.Bytes != 1024*1024 {
		t.Fatalf("limit: %+v", r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r = measureSpeed(ctx, srv.Client(), cfg, SpeedResult{Direction: "download"}, func(SpeedResult) {})
	if r.Status != "cancelled" || r.Mbps != 0 {
		t.Fatalf("cancel: %+v", r)
	}
}

func TestSpeedHistoryTimeAndCursor(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UnixMilli()
	for i := 0; i < 55; i++ {
		r := SpeedResult{Time: now, NodeID: "node", Direction: "download", Status: "complete"}
		if err = s.saveSpeed(&r); err != nil {
			t.Fatal(err)
		}
		other := SpeedResult{Time: now, NodeID: "other", Direction: "download"}
		if err = s.saveSpeed(&other); err != nil {
			t.Fatal(err)
		}
	}
	old := SpeedResult{Time: now - 86400000}
	if err = s.saveSpeed(&old); err != nil {
		t.Fatal(err)
	}
	page, err := s.speedHistory(now-1000, now+1, 0, "node")
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 51 {
		t.Fatalf("first page %d", len(page))
	}
	next, err := s.speedHistory(now-1000, now+1, page[49].ID, "node")
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 5 || next[0].ID >= page[49].ID {
		t.Fatalf("cursor page: %+v", next)
	}
}

func TestLatestSpeedSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.db")
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []SpeedResult{
		{NodeID: "a", Direction: "download", Status: "complete", Mbps: 20},
		{NodeID: "a", Direction: "upload", Status: "complete", Mbps: 10},
		{NodeID: "a", Direction: "download", Status: "error", Message: "HTTP 403"},
		{NodeID: "b", Direction: "download", Status: "complete", Mbps: 30},
		{NodeID: "deleted", Direction: "download", Status: "complete"},
	} {
		if err = s.saveSpeed(&r); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	items, err := s.latestSpeed([]SpeedNode{{ID: "a"}, {ID: "b"}, {ID: "untested"}}, false)
	if err != nil || len(items) != 3 {
		t.Fatalf("latest: %+v, %v", items, err)
	}
	byKey := map[string]SpeedResult{}
	for _, r := range items {
		byKey[r.NodeID+"/"+r.Direction] = r
	}
	if byKey["a/download"].Status != "error" || byKey["a/upload"].Mbps != 10 || byKey["b/download"].Mbps != 30 {
		t.Fatalf("wrong latest direction results: %+v", byKey)
	}
}
