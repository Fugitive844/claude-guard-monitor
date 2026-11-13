package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIPerfProgressStreamingAndFinal(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			var updates []SpeedResult
			p := newIPerfProgress(SpeedResult{Direction: "upload", Protocol: "udp"}, func(r SpeedResult) { updates = append(updates, r) })
			intervals := `{"event":"interval","data":{"sum":{"end":1,"bytes":1250000,"bits_per_second":10000000}}}` + "\n" + `{"event":"interval","data":{"sum":{"end":2,"bytes":750000,"bits_per_second":6000000}}}` + "\n"
			final := `{"event":"end","data":{"sum_sent":{"bytes":2000000,"seconds":2,"bits_per_second":8000000},"sum_received":{"bytes":1750000,"seconds":2,"bits_per_second":7000000,"lost_percent":12.5,"lost_packets":1,"packets":8,"jitter_ms":0.2}}}` + "\n"
			if format == "text" {
				intervals = "[  5] 0.00-1.00 sec 1.19 MBytes 10.00 Mbits/sec 4\n[  5] 1.00-2.00 sec 732 KBytes 6.00 Mbits/sec 4\n"
				final = "[  5] 0.00-2.00 sec 1.91 MBytes 8.00 Mbits/sec 0.000 ms 0/8 (0%) sender\n[  5] 0.00-2.00 sec 1.67 MBytes 7.00 Mbits/sec 0.200 ms 1/8 (12.5%) receiver\nServer output:\n[  5] 0.00-3.00 sec 1 MBytes 99.00 Mbits/sec\n"
			}
			// SSH and process stdout may split any line across writes.
			for _, c := range []byte(intervals) {
				if _, err := p.Write([]byte{c}); err != nil {
					t.Fatal(err)
				}
			}
			if len(updates) != 2 || *updates[0].IntervalMbps != 10 || *updates[1].IntervalMbps != 6 || updates[1].Seconds != 2 {
				t.Fatal(updates)
			}
			if !strings.Contains(updates[1].Message, "实时发送") {
				t.Fatal(updates[1])
			}
			if _, err := p.result(); err == nil {
				t.Fatal("accepted incomplete result")
			}
			p.Write([]byte(final))
			r, err := p.result()
			if err != nil || r.Mbps != 7 || r.LostPercent != 12.5 || r.Status != "complete" || r.IntervalMbps != nil || r.Message != "" || len(updates) != 2 {
				t.Fatal(r, err, updates)
			}
		})
	}
}

func TestIPerfAdvancedSamplesAndPersistence(t *testing.T) {
	config := SSHSpeedRequest{Mode: "iperf3", Protocol: "both", Direction: "both", HostID: "target", Port: 55205, Seconds: 20, Bandwidth: 10, Parallel: 2, Omit: 2, TCPBandwidth: 8, UDPLength: 1000}
	if err := config.validate(); err != nil {
		t.Fatal(err)
	}
	tcp := strings.Join(iperfArgs(config.Port, config, SpeedResult{Protocol: "tcp"}), " ")
	udp := strings.Join(iperfArgs(config.Port, config, SpeedResult{Protocol: "udp"}), " ")
	if !strings.Contains(tcp, "-P 2 -b 4.000M") || !strings.Contains(tcp, "-O 2") || !strings.Contains(udp, "-b 10M -l 1000") || strings.Contains(udp, "-P") {
		t.Fatal(tcp, udp)
	}
	for _, format := range []string{"json", "text"} {
		p := newIPerfProgress(SpeedResult{NodeID: "ssh:target", SourceHostID: "source", Protocol: "tcp", Direction: "upload", Time: 10, TestConfig: &config}, func(SpeedResult) {})
		if format == "json" {
			p.Write([]byte(`{"event":"interval","data":{"sum":{"start":0,"end":1,"bytes":900,"bits_per_second":7200,"omitted":true}}}` + "\n"))
			p.Write([]byte(`{"event":"interval","data":{"sum":{"start":0,"end":1,"bytes":1000000,"bits_per_second":8000000,"retransmits":2}}}` + "\n"))
			p.Write([]byte(`{"event":"end","data":{"sum_sent":{"bytes":1000000,"seconds":1,"bits_per_second":8000000,"retransmits":2},"sum_received":{"bytes":900000,"seconds":1,"bits_per_second":7200000},"streams":[{"sender":{"mean_rtt":1000,"min_rtt":500,"max_rtt":2000}},{"sender":{"mean_rtt":3000,"min_rtt":1000,"max_rtt":4000}}]}}` + "\n"))
		} else {
			p.Write([]byte("[SUM] 0.00-1.00 sec 9 MBytes 72 Mbits/sec (omitted)\n[ 5] 0.00-1.00 sec 500 KBytes 4.00 Mbits/sec 1 100 KBytes\n[ 7] 0.00-1.00 sec 500 KBytes 4.00 Mbits/sec 1 100 KBytes\n[SUM] 0.00-1.00 sec 977 KBytes 8.00 Mbits/sec 2\n[SUM] 0.00-1.00 sec 977 KBytes 8.00 Mbits/sec 2 sender\n[SUM] 0.00-1.00 sec 879 KBytes 7.20 Mbits/sec receiver\n"))
		}
		r, err := p.result()
		if err != nil || len(r.Samples) != 1 || r.Samples[0].Mbps != 8 || r.Samples[0].Retransmits == nil || *r.Samples[0].Retransmits != 2 || r.Retransmits == nil || *r.Retransmits != 2 || r.SenderMbps == nil || *r.SenderMbps != 8 {
			t.Fatal(format, r, err)
		}
		if format == "json" && (r.RTTMeanMS == nil || *r.RTTMeanMS != 2 || *r.RTTMinMS != 0.5 || *r.RTTMaxMS != 4) {
			t.Fatal(r)
		}
		s := sshTestService(t)
		if err = s.app.db.saveSpeed(&r); err != nil {
			t.Fatal(err)
		}
		var raw string
		if err = s.app.db.QueryRow("SELECT result FROM speed_results WHERE id=?", r.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var saved map[string]any
		if json.Unmarshal([]byte(raw), &saved) != nil {
			t.Fatal(raw)
		}
		if saved["samples"] != nil || saved["interval_mbps"] != nil || saved["test_config"] == nil || saved["retransmits"] == nil || saved["mbps"] != 7.2 || len(r.Samples) != 1 {
			t.Fatal(saved, r)
		}
	}
}
