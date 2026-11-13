package main

import (
	"path/filepath"
	"strconv"
	"testing"
)

func TestSpeedHistoryGroupsKeepRoundsAcrossPages(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, scope := range []string{"ssh", "proxy"} {
		node := "node"
		if scope == "ssh" {
			node = "ssh:node"
		}
		for i := 0; i < 53; i++ {
			for _, direction := range []string{"download", "upload"} {
				r := SpeedResult{Time: 1000 + int64(i), Job: strconv.Itoa(i), NodeID: node, SourceHostID: "source", Mode: "iperf3", Protocol: "tcp", Direction: direction}
				if err = s.saveSpeed(&r); err != nil {
					t.Fatal(err)
				}
			}
		}
		page, err := s.speedHistoryGroups(0, 2000, 0, "", scope)
		if err != nil || len(page) != 51 {
			t.Fatalf("%s first page: %d %v", scope, len(page), err)
		}
		for _, g := range page {
			if len(g.Items) != 2 || g.Items[0].Job != g.Items[1].Job {
				t.Fatalf("split round: %+v", g)
			}
		}
		next, err := s.speedHistoryGroups(0, 2000, page[49].ID, "", scope)
		if err != nil || len(next) != 3 || next[0].Items[0].Job != "2" {
			t.Fatalf("%s cursor: %+v %v", scope, next, err)
		}
	}
}

func TestSpeedHistoryGroupsSeparatePathsAndMissingJobs(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, r := range []SpeedResult{
		{Job: "round", NodeID: "ssh:a", SourceHostID: "source", Protocol: "tcp", Direction: "download"},
		{Job: "round", NodeID: "ssh:a", SourceHostID: "source", Protocol: "tcp", Direction: "upload", Status: "cancelled"},
		{Job: "round", NodeID: "ssh:a", SourceHostID: "source", Protocol: "udp", Direction: "download"},
		{Job: "round", NodeID: "ssh:a", SourceHostID: "other", Protocol: "tcp", Direction: "upload"},
		{Job: "other-round", NodeID: "ssh:a", SourceHostID: "source", Protocol: "tcp", Direction: "upload"},
		{NodeID: "ssh:a", Direction: "download"},
		{NodeID: "ssh:a", Direction: "upload"},
	} {
		r.Time = 1000
		if err = s.saveSpeed(&r); err != nil {
			t.Fatal(err)
		}
	}
	groups, err := s.speedHistoryGroups(0, 2000, 0, "", "ssh")
	if err != nil || len(groups) != 6 {
		t.Fatalf("groups=%+v error=%v", groups, err)
	}
	groups, err = s.speedHistoryGroups(0, 2000, 0, "ssh:source", "ssh")
	if err != nil || len(groups) != 3 || len(groups[2].Items) != 2 {
		t.Fatalf("source filter=%+v error=%v", groups, err)
	}
}
