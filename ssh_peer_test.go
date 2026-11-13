package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestPeerIPerfExecutionAndCleanup(t *testing.T) {
	commands := make(chan string, 8)
	serverClosed := make(chan struct{}, 8)
	target, _ := serveTestSSH(t, func(command string, c ssh.Channel) {
		if !strings.Contains(command, "iperf3 -s -1 -p 55201") {
			return
		}
		io.WriteString(c, "Server listening on 55201\n")
		io.Copy(io.Discard, c)
		c.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		serverClosed <- struct{}{}
	})
	source, _ := serveTestSSH(t, func(command string, c ssh.Channel) {
		if strings.HasPrefix(command, "sh -c") {
			commands <- command
			io.WriteString(c, `{"event":"interval","data":{"sum":{"end":1,"bytes":1250000,"bits_per_second":10000000}}}`+"\n")
			io.WriteString(c, `{"event":"end","data":{"sum_received":{"bytes":3750000,"seconds":3,"bits_per_second":10000000,"lost_packets":1,"packets":100,"lost_percent":1,"jitter_ms":0.2}}}`)
		}
		c.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
	})
	s := sshTestService(t)
	for _, protocol := range []string{"tcp", "udp"} {
		for _, direction := range []string{"upload", "download"} {
			t.Run(protocol+"/"+direction, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				sender, err := s.openSSH(ctx, source, false)
				if err != nil {
					t.Fatal(err)
				}
				defer sender.Close()
				receiver, err := s.openSSH(ctx, target, false)
				if err != nil {
					t.Fatal(err)
				}
				defer receiver.Close()
				r, err := measurePeerIPerf(ctx, sender, receiver, SSHHost{Host: "192.0.2.77"}, SSHSpeedRequest{Port: 55201, Seconds: 3, Bandwidth: 10}, SpeedResult{Direction: direction, Protocol: protocol}, func(SpeedResult) {})
				if err != nil || r.Status != "complete" || r.Mbps != 10 {
					t.Fatal(r, err)
				}
				command := <-commands
				for _, arg := range []string{"192.0.2.77", "55201", "sleep 18"} {
					if !strings.Contains(command, arg) {
						t.Fatalf("missing remote client argument %s", arg)
					}
				}
				if strings.Contains(command, "-R") != (direction == "download") || strings.Contains(command, "-u") != (protocol == "udp") {
					t.Fatal("incorrect direction/protocol")
				}
				select {
				case <-serverClosed:
				case <-time.After(time.Second):
					t.Fatal("target server remains")
				}
			})
		}
	}
}

func TestPeerRequestAndHistory(t *testing.T) {
	r := SSHSpeedRequest{HostID: "target", SourceHostID: "source", Mode: "iperf3", Protocol: "both", Direction: "both", Port: 5201, Seconds: 3, Bandwidth: 10}
	if err := r.validate(); err != nil {
		t.Fatal(err)
	}
	r.Mode = "ssh"
	if r.validate() == nil {
		t.Fatal("native peer request accepted")
	}
	r.Mode = "iperf3"
	r.SourceHostID = r.HostID
	if r.validate() == nil {
		t.Fatal("same host accepted")
	}
	s := sshTestService(t)
	item := SpeedResult{NodeID: "ssh:target", SourceHostID: "source", SourceHostName: "source name", Time: 10}
	if err := s.app.db.saveSpeed(&item); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"source", "target"} {
		rows, err := s.app.db.speedHistory(0, 20, 0, "ssh:"+id, "ssh")
		if err != nil || len(rows) != 1 {
			t.Fatal(fmt.Sprint(id, rows, err))
		}
	}
}
