package main

import (
	"strings"
	"testing"
	"time"
)

func TestKernelLogParsing(t *testing.T) {
	stamp := time.Date(2026, 11, 4, 18, 0, 0, 0, time.UTC)
	line := "Wed Nov 4 18:00:00 2026 kern.notice kernel: [ 123.456] CG_DROP IN=br-lan OUT=eth0 SRC=192.0.2.20 DST=160.79.104.1 LEN=60 PROTO=TCP SPT=54321 DPT=443 SYN"
	e, err := parseKernelLog(line, stamp.Add(-time.Second))
	if err != nil || e.Src != "192.0.2.20" || e.Dst != "160.79.104.1" || e.Family != 4 || e.Bytes != 60 || e.DPort != 443 || e.SPort != 54321 || e.First != stamp.UnixMilli() {
		t.Fatalf("IPv4: %+v %v", e, err)
	}
	v6 := strings.NewReplacer("192.0.2.20", "fd00::10", "160.79.104.1", "2607:6bc0::1", "LEN=60", "LEN=20", "PROTO=TCP", "PROTO=UDP").Replace(line)
	e, err = parseKernelLog(v6, stamp.Add(-time.Second))
	if err != nil || e.Family != 6 || e.Bytes != 60 || e.Proto != "UDP" {
		t.Fatalf("IPv6: %+v %v", e, err)
	}
	for _, bad := range []string{strings.Replace(line, "kern.notice", "user.notice", 1), strings.Replace(line, "DPT=443", "DPT=65536", 1), strings.Replace(line, "LEN=60", "LEN=-1", 1), strings.Replace(line, "SRC=192.0.2.20", "SRC=bad", 1)} {
		if _, err := parseKernelLog(bad, stamp.Add(-time.Second)); err == nil {
			t.Fatalf("accepted invalid line %s", bad)
		}
	}
	if _, err := parseKernelLog(line, stamp.Add(time.Second)); err == nil {
		t.Fatal("replayed historical record")
	}
}

func TestLoggingBackendIsolation(t *testing.T) {
	for _, backend := range []string{"nflog", "syslog"} {
		rule := monitorLogRule("ip", "guard_v4", "test", backend, 10631)
		if strings.Contains(rule, " drop") || strings.Contains(rule, " accept") {
			t.Fatal("logger must not change verdict")
		}
		if backend == "syslog" && (!strings.Contains(rule, "5/second") || !strings.Contains(rule, "level notice") || strings.Contains(rule, "log group")) {
			t.Fatal(rule)
		}
		if backend == "nflog" && !strings.Contains(rule, "log group 10631") {
			t.Fatal(rule)
		}
	}
	old := []string{"CG_MONITOR_LOG4", "CG_MONITOR_LOG6"}
	compat := []string{"CG_MONITOR_LOG4_SYSLOG", "CG_MONITOR_LOG6_SYSLOG"}
	if !logsCompatible(old, "nflog") || logsCompatible(old, "syslog") || !logsCompatible(compat, "syslog") || logsCompatible(compat, "nflog") {
		t.Fatal("incorrect backend migration detection")
	}
}
