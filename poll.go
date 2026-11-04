package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

type Snapshot struct {
	Time            int64          `json:"time"`
	Version         string         `json:"version"`
	Demo            bool           `json:"demo"`
	P4              uint64         `json:"p4"`
	P6              uint64         `json:"p6"`
	B4              uint64         `json:"b4"`
	B6              uint64         `json:"b6"`
	D4              uint64         `json:"d4"`
	D6              uint64         `json:"d6"`
	Epoch           string         `json:"epoch"`
	Table           bool           `json:"table"`
	Logging         bool           `json:"logging"`
	Guard           map[string]any `json:"guard"`
	GuardRunning    bool           `json:"guard_running"`
	ObserverRunning bool           `json:"observer_running"`
	GuardEnabled    bool           `json:"guard_enabled"`
	MonitorEnabled  bool           `json:"monitor_enabled"`
	PasswallRunning bool           `json:"passwall_running"`
	Collector       string         `json:"collector"`
	Received        uint64         `json:"received"`
	Lost            uint64         `json:"lost"`
	WriterLost      uint64         `json:"writer_lost"`
	Error           string         `json:"error"`
	DBBytes         int64          `json:"db_bytes"`
	Operation       string         `json:"operation"`
}

func run(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	b, e := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if e != nil {
		return b, fmt.Errorf("%s: %w: %s", name, e, strings.TrimSpace(string(b)))
	}
	return b, nil
}
func nftBatch(s string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nft", "-f", "-")
	cmd.Stdin = strings.NewReader(s)
	b, e := cmd.CombinedOutput()
	if e != nil {
		return fmt.Errorf("nft: %w: %s", e, b)
	}
	return nil
}
func readCounters(raw []byte) (Snapshot, []string, error) {
	var root struct {
		NFT []struct {
			Chain map[string]any `json:"chain"`
			Rule  struct {
				Comment string                       `json:"comment"`
				Expr    []map[string]json.RawMessage `json:"expr"`
			} `json:"rule"`
		} `json:"nftables"`
	}
	var s Snapshot
	var logs []string
	if e := json.Unmarshal(raw, &root); e != nil {
		return s, nil, e
	}
	for _, v := range root.NFT {
		if v.Chain != nil {
			s.Table = true
		}
		if strings.HasPrefix(v.Rule.Comment, "CG_MONITOR_LOG") {
			logs = append(logs, v.Rule.Comment)
			continue
		}
		if !strings.HasPrefix(v.Rule.Comment, "Claude known destinations") {
			continue
		}
		for _, x := range v.Rule.Expr {
			if r, ok := x["counter"]; ok {
				var c struct{ Packets, Bytes uint64 }
				if e := json.Unmarshal(r, &c); e != nil {
					return s, nil, e
				}
				if strings.Contains(v.Rule.Comment, "IPv6") {
					s.P6 += c.Packets
					s.B6 += c.Bytes
				} else {
					s.P4 += c.Packets
					s.B4 += c.Bytes
				}
			}
		}
	}
	s.Logging = len(logs) == 2
	return s, logs, nil
}
func (a *App) ensureLogs(logs []string) error {
	have := map[string]bool{}
	for _, s := range logs {
		have[s] = true
	}
	batch := ""
	backend := a.logBackend
	if backend == "" {
		backend = "nflog"
	}
	// Remove only this monitor's incompatible logging rules; never touch DROP or sets.
	if len(logs) > 0 && !logsCompatible(logs, backend) {
		b, e := run(2*time.Second, "nft", "-j", "list", "chain", "inet", "claude_guard", "forward_guard")
		if e != nil {
			return e
		}
		var root struct {
			NFT []struct {
				Rule struct {
					Comment string `json:"comment"`
					Handle  int64  `json:"handle"`
				} `json:"rule"`
			} `json:"nftables"`
		}
		if e = json.Unmarshal(b, &root); e != nil {
			return e
		}
		for _, x := range root.NFT {
			if strings.HasPrefix(x.Rule.Comment, "CG_MONITOR_LOG") {
				if x.Rule.Handle <= 0 {
					return fmt.Errorf("missing log rule handle")
				}
				batch += fmt.Sprintf("delete rule inet claude_guard forward_guard handle %d\n", x.Rule.Handle)
			}
		}
		have = map[string]bool{}
	}
	for _, v := range []struct{ family, set, tag string }{{"ip", "targets4", "CG_MONITOR_LOG4"}, {"ip6", "targets6", "CG_MONITOR_LOG6"}} {
		if backend == "syslog" {
			v.tag += "_SYSLOG"
		}
		if !have[v.tag] {
			batch += monitorLogRule(v.family, v.set, v.tag, backend, a.cfg.Group)
		}
	}
	if batch != "" {
		return nftBatch(batch)
	}
	return nil
}
func serviceState(name string) (map[string]bool, bool) {
	b, e := run(2*time.Second, "ubus", "call", "service", "list", fmt.Sprintf(`{"name":%q}`, name))
	r := map[string]bool{}
	if e != nil {
		return r, false
	}
	var o map[string]struct {
		Instances map[string]struct {
			Running bool `json:"running"`
		} `json:"instances"`
	}
	if json.Unmarshal(b, &o) != nil {
		return r, false
	}
	for k, v := range o[name].Instances {
		r[k] = v.Running
	}
	return r, true
}
func enabled(name string) bool {
	_, e := run(time.Second, "/etc/init.d/"+name, "enabled")
	return e == nil
}
func (a *App) poll() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	prev := Snapshot{}
	epoch := randomToken()[:12]
	step := 0
	lastState := ""
	lastPolicy := ""
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-tick.C:
			step++
			s := Snapshot{Time: time.Now().UnixMilli(), Version: version, Demo: a.demo, Epoch: epoch, Guard: map[string]any{}}
			if a.demo {
				s.Table = true
				s.Logging = true
				s.GuardRunning = true
				s.ObserverRunning = true
				s.PasswallRunning = true
				s.GuardEnabled = true
				s.MonitorEnabled = true
				s.P4 = prev.P4
				s.B4 = prev.B4
				if step%3 == 0 {
					s.P4 += 2
					s.B4 += 120
					a.enqueue(Event{Src: "192.0.2.10", Dst: "160.79.104.10", SPort: 51000 + step, DPort: 443, Proto: "TCP", Family: 4, Bytes: 60, Domains: "claude.ai（模拟）", Reason: "模拟事件，仅用于本地演示", Source: "demo"})
				}
				s.Guard = map[string]any{"mode": "enforce", "selected_node_name": "演示节点 · 非生产数据", "strict_fail_closed": false, "domain_expressions": 36}
			} else {
				b, e := run(2*time.Second, "nft", "-j", "list", "chain", "inet", "claude_guard", "forward_guard")
				if e == nil {
					c, logs, er := readCounters(b)
					if er == nil {
						s.P4 = c.P4
						s.P6 = c.P6
						s.B4 = c.B4
						s.B6 = c.B6
						s.Table = c.Table
						s.Logging = logsCompatible(logs, a.logBackend)
						if !s.Logging && step%5 == 1 {
							if er = a.ensureLogs(logs); er != nil {
								s.Error = er.Error()
							}
						}
					} else {
						s.Error = er.Error()
					}
				}
				_ = readJSON("/var/run/claude-guard/status.json", &s.Guard)
				if step%3 == 1 {
					states, _ := serviceState("claude-guard")
					s.GuardRunning = states["guard"]
					s.ObserverRunning = states["dns-observer"]
					s.GuardEnabled = enabled("claude-guard")
					s.MonitorEnabled = enabled("claude-guard-monitor")
					out, _ := run(time.Second, "pgrep", "-f", "^/tmp/etc/passwall/bin/(xray|sing-box) ")
					s.PasswallRunning = len(out) > 0
				} else {
					s.GuardRunning = prev.GuardRunning
					s.ObserverRunning = prev.ObserverRunning
					s.GuardEnabled = prev.GuardEnabled
					s.MonitorEnabled = prev.MonitorEnabled
					s.PasswallRunning = prev.PasswallRunning
				}
			}
			if !s.Table || !prev.Table || s.P4 < prev.P4 || s.P6 < prev.P6 {
				s.Epoch = randomToken()[:12]
				epoch = s.Epoch
			} else {
				s.D4 = s.P4 - prev.P4
				s.D6 = s.P6 - prev.P6
			}
			state := fmt.Sprintf("守卫=%t 观察器=%t 防护表=%t PassWall进程=%t", s.GuardRunning, s.ObserverRunning, s.Table, s.PasswallRunning)
			if state != lastState {
				a.audit("service", state)
				lastState = state
			}
			policy := fmt.Sprint(s.Guard["selected_node_id"], "|", s.Guard["domain_expressions"], "|", s.Guard["error"])
			if policy != lastPolicy {
				a.audit("policy", fmt.Sprintf("当前节点=%v 域名表达式=%v 错误=%v", s.Guard["selected_node_name"], s.Guard["domain_expressions"], s.Guard["error"]))
				lastPolicy = policy
			}
			a.mu.Lock()
			s.Collector = a.collectorState
			s.Received = a.received
			s.Lost = a.lost
			s.WriterLost = a.writerLost
			if s.Error == "" {
				s.Error = a.lastError
			}
			s.Operation = a.op
			s.DBBytes = a.db.size()
			a.snapshot = s
			a.mu.Unlock()
			_, e := a.db.Exec("INSERT INTO samples(ts,epoch,p4,p6,b4,b6,d4,d6,received,lost) VALUES(?,?,?,?,?,?,?,?,?,?)", s.Time, s.Epoch, s.P4, s.P6, s.B4, s.B6, s.D4, s.D6, s.Received, s.Lost+s.WriterLost)
			if e != nil {
				a.mu.Lock()
				a.lastError = "统计落库失败: " + e.Error()
				a.mu.Unlock()
			}
			if step%60 == 0 {
				a.mu.RLock()
				retention := a.cfg
				a.mu.RUnlock()
				if e = a.db.prune(retention); e != nil {
					a.mu.Lock()
					a.lastError = "日志清理失败: " + e.Error()
					a.mu.Unlock()
				}
			}
			prev = s
		}
	}
}
func (a *App) getSnapshot() Snapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	s := a.snapshot
	s.Operation = a.op
	return s
}
func (a *App) domains(ip string) string {
	var cache map[string]map[string]int64
	if readJSON("/var/run/claude-guard/learned-live.json", &cache) != nil {
		return ""
	}
	addr, e := netip.ParseAddr(ip)
	if e != nil {
		return ""
	}
	out := []string{}
	for name, ips := range cache {
		for s := range ips {
			v, e := netip.ParseAddr(s)
			if e == nil && v == addr {
				out = append(out, name)
				break
			}
		}
	}
	sort.Strings(out)
	if len(out) > 8 {
		out = append(out[:8], "…")
	}
	return strings.Join(out, ", ")
}
func exists(p string) bool { _, e := os.Stat(p); return e == nil }
