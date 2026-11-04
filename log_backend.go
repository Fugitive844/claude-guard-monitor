package main

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func selectLogBackend(config string) string {
	if config == "nflog" || config == "syslog" {
		return config
	}
	if exists("/sys/module/nfnetlink_log") {
		return "nflog"
	}
	return "syslog"
}
func logsCompatible(logs []string, backend string) bool {
	suffix := ""
	if backend == "syslog" {
		suffix = "_SYSLOG"
	}
	have := map[string]bool{}
	for _, s := range logs {
		have[s] = true
	}
	return len(logs) == 2 && have["CG_MONITOR_LOG4"+suffix] && have["CG_MONITOR_LOG6"+suffix]
}
func monitorLogRule(family, set, tag, backend string, group uint16) string {
	spec := fmt.Sprintf("limit rate 30/second burst 100 packets log group %d snaplen 96 queue-threshold 1 prefix \"CG_DROP \"", group)
	if backend == "syslog" {
		spec = "limit rate 5/second burst 10 packets log prefix \"CG_DROP \" level notice"
	}
	return fmt.Sprintf("insert rule inet claude_guard forward_guard ct direction original %s daddr @%s %s comment \"%s\"\n", family, set, spec, tag)
}

var kernelRecordRE = regexp.MustCompile(`^([A-Z][a-z]{2} [A-Z][a-z]{2} +[0-9]{1,2} [0-9:]{8} [0-9]{4}) kern\.[a-z]+ kernel: \[\s*[0-9]+(?:\.[0-9]+)?\] CG_DROP `)

// logread runs with TZ=UTC0. Some firmware's printk clock differs from /proc/uptime,
// so use the log daemon's wall timestamp, not an inferred kernel boot time.
func parseKernelLog(line string, since time.Time) (Event, error) {
	var e Event
	m := kernelRecordRE.FindStringSubmatchIndex(line)
	if m == nil {
		return e, fmt.Errorf("not a guard kernel record")
	}
	stamp, err := time.Parse("Mon Jan _2 15:04:05 2006", line[m[2]:m[3]])
	if err != nil || stamp.Before(since.Truncate(time.Second)) {
		return e, fmt.Errorf("old or invalid timestamp")
	}
	fields := map[string]string{}
	for _, s := range strings.Fields(line[m[1]:]) {
		if k, v, ok := strings.Cut(s, "="); ok {
			if _, has := fields[k]; !has {
				fields[k] = v
			}
		}
	}
	src, err := netip.ParseAddr(fields["SRC"])
	if err != nil {
		return e, err
	}
	dst, err := netip.ParseAddr(fields["DST"])
	if err != nil || src.Is4() != dst.Is4() {
		return e, fmt.Errorf("invalid IP family")
	}
	e.Src = src.String()
	e.Dst = dst.String()
	e.Family = 6
	if src.Is4() {
		e.Family = 4
	}
	e.Proto = fields["PROTO"]
	if e.Proto == "" {
		return e, fmt.Errorf("missing protocol")
	}
	e.Bytes, err = strconv.ParseInt(fields["LEN"], 10, 64)
	if err != nil || e.Bytes < 0 {
		return e, fmt.Errorf("invalid length")
	}
	if e.Family == 6 {
		e.Bytes += 40
	} // IPv6 kernel log LEN is payload length.
	if e.Proto == "TCP" || e.Proto == "UDP" {
		for _, p := range []struct {
			k   string
			out *int
		}{{"SPT", &e.SPort}, {"DPT", &e.DPort}} {
			if fields[p.k] == "" {
				continue
			} // Noninitial fragments may omit ports.
			n, err := strconv.ParseUint(fields[p.k], 10, 16)
			if err != nil {
				return e, err
			}
			*p.out = int(n)
		}
	}
	e.First = stamp.UnixMilli()
	e.Source = "kernel-log"
	e.Reason = "已知目标直连转发被阻断（内核日志采集）"
	return e, nil
}
