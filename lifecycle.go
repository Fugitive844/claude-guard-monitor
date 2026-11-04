package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var guardFiles = []string{"guard.lua", "observer.lua", "tls.lua", "cli.lua", "capture.sh", "test.lua", "test-sni.lua"}

func validAction(s string) bool {
	switch s {
	case "install", "start", "stop", "disable", "uninstall", "guard_boot_on", "guard_boot_off", "monitor_boot_on", "monitor_boot_off":
		return true
	}
	return false
}
func (a *App) environment() map[string]any {
	if a.demo {
		return map[string]any{"os": runtime.GOOS, "arch": runtime.GOARCH, "demo": true, "ready": false, "checks": map[string]bool{"演示模式禁止管理操作": false}}
	}
	checks := map[string]bool{}
	for _, cmd := range []string{"lua", "nft", "tcpdump", "ubus", "geoview"} {
		_, e := run(time.Second, "/bin/sh", "-c", "command -v "+cmd)
		checks[cmd] = e == nil
	}
	_, e := run(2*time.Second, "lua", "-e", `require('uci'); require('nixio'); require('nixio.fs'); require('luci.jsonc')`)
	checks["Lua 模块"] = e == nil
	b, e := run(time.Second, "uci", "-q", "get", "passwall.claude")
	checks["PassWall claude 规则"] = e == nil && strings.TrimSpace(string(b)) == "shunt_rules"
	checks["procd"] = exists("/etc/rc.common")
	if a.logBackend == "syslog" {
		checks["内核日志采集"] = exists("/sbin/logread") && exists("/sys/module/nft_log")
	} else {
		checks["NFLOG 内核模块"] = exists("/sys/module/nfnetlink_log")
	}
	ready := true
	for k, v := range checks {
		if k != "geoview" && !v {
			ready = false
		}
	}
	return map[string]any{"os": runtime.GOOS, "arch": runtime.GOARCH, "checks": checks, "ready": ready, "installed": exists("/etc/init.d/claude-guard"), "rule": "claude", "data_dir": a.cfg.DataDir, "group": a.cfg.Group, "log_backend": a.logBackend}
}
func renderBundle(name string, g GuardConfig) ([]byte, error) {
	b, e := assets.ReadFile("bundle/" + name)
	if e != nil {
		return nil, e
	}
	s := string(b)
	switch name {
	case "guard.lua":
		s = strings.Replace(s, "sock:connect('127.0.0.1',15353)", fmt.Sprintf("sock:connect('%s',%d)", g.DNSIP, g.DNSPort), 1)
	case "observer.lua":
		var ips, ports []string
		for _, x := range g.DNSSources {
			v, _ := netip.ParseAddr(x)
			if v.Is6() {
				x = v.StringExpanded()
			}
			ips = append(ips, "['"+x+"']=true")
		}
		for _, v := range g.DNSPorts {
			ports = append(ports, "["+strconv.Itoa(v)+"]=true")
		}
		s = regexp.MustCompile(`if not \(\{[^\n]+\}\)\[source\] then return end`).ReplaceAllString(s, "if not ({"+strings.Join(ips, ",")+"})[source] then return end")
		s = regexp.MustCompile(`if not \(\{[^\n]+\}\)\[port\] then return end`).ReplaceAllString(s, "if not ({"+strings.Join(ports, ",")+"})[port] then return end")
	case "capture.sh":
		var ports, nets, exclude []string
		for _, v := range g.DNSPorts {
			ports = append(ports, fmt.Sprintf("src port %d", v))
		}
		for _, v := range g.ClientCIDRs {
			nets = append(nets, "src net "+v)
		}
		for _, v := range g.RouterIPs {
			exclude = append(exclude, "not src host "+v)
		}
		bpf := "(udp and (" + strings.Join(ports, " or ") + ")) or (tcp dst port 443 and (" + strings.Join(nets, " or ") + ") and " + strings.Join(exclude, " and ") + ")"
		lines := strings.Split(s, "\n")
		for i, l := range lines {
			if strings.HasPrefix(l, "/usr/bin/tcpdump ") {
				lines[i] = "/usr/bin/tcpdump -i any -n -U -s 65535 -w \"$fifo\" '" + bpf + "' 2>/dev/null &"
			}
		}
		s = strings.Join(lines, "\n")
	}
	return []byte(s), nil
}
func (a *App) backupBytes() ([]byte, error) {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	paths := []string{"/etc/init.d/claude-guard", "/etc/claude-guard/last-policy.json", "/etc/claude-guard/learned.json", "/etc/claude-guard/README"}
	for _, n := range guardFiles {
		paths = append(paths, "/usr/libexec/claude-guard/"+n)
	}
	for _, p := range paths {
		v, e := os.ReadFile(p)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return nil, e
		}
		w, e := z.Create(strings.TrimPrefix(p, "/"))
		if e != nil {
			return nil, e
		}
		if _, e = w.Write(v); e != nil {
			return nil, e
		}
	}
	if e := z.Close(); e != nil {
		return nil, e
	}
	return b.Bytes(), nil
}
func (a *App) lifecycle(action string) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("仅支持 Linux 软路由")
	}
	call := func(name, op string) error { _, e := run(25*time.Second, "/etc/init.d/"+name, op); return e }
	switch action {
	case "guard_boot_on":
		return call("claude-guard", "enable")
	case "guard_boot_off":
		return call("claude-guard", "disable")
	case "monitor_boot_on":
		return call("claude-guard-monitor", "enable")
	case "monitor_boot_off":
		return call("claude-guard-monitor", "disable")
	case "start":
		return call("claude-guard", "start")
	case "stop":
		return call("claude-guard", "stop")
	case "disable", "uninstall":
		if action == "uninstall" {
			b, e := a.backupBytes()
			if e != nil {
				return e
			}
			if e = writeAtomic(filepath.Join(a.cfg.DataDir, "backup-"+time.Now().Format("20060102-150405")+".zip"), b, 0600); e != nil {
				return e
			}
		}
		if exists("/etc/init.d/claude-guard") {
			if e := call("claude-guard", "disable"); e != nil {
				return e
			}
			if e := call("claude-guard", "stop"); e != nil {
				return e
			}
		}
		for i := 0; i < 8; i++ {
			s, ok := serviceState("claude-guard")
			if !ok {
				return fmt.Errorf("无法确认守卫已停止，保留阻断表")
			}
			if !s["guard"] && !s["dns-observer"] {
				break
			}
			if i == 7 {
				return fmt.Errorf("守卫仍运行，保留阻断表")
			}
			time.Sleep(time.Second)
		}
		if _, e := run(time.Second, "nft", "list", "table", "inet", "claude_guard"); e == nil {
			if e = nftBatch("delete table inet claude_guard\n"); e != nil {
				return e
			}
		}
		if action == "uninstall" {
			var e error
			for _, n := range guardFiles {
				if e = os.Remove("/usr/libexec/claude-guard/" + n); e != nil && !os.IsNotExist(e) {
					return e
				}
			}
			if e = os.Remove("/etc/init.d/claude-guard"); e != nil && !os.IsNotExist(e) {
				return e
			}
		}
		return nil
	case "install":
		return a.installGuard()
	}
	return fmt.Errorf("unknown action")
}
func (a *App) installGuard() (result error) {
	env := a.environment()
	if env["ready"] != true {
		return fmt.Errorf("环境检查未通过，请先处理缺项")
	}
	a.mu.RLock()
	g := a.cfg.Guard
	a.mu.RUnlock()
	if e := validGuard(g); e != nil {
		return e
	}
	stage, e := os.MkdirTemp("/tmp", "cg-install-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(stage)
	names := append(append([]string{}, guardFiles...), "init.sh")
	for _, n := range names {
		b, e := renderBundle(n, g)
		if e != nil {
			return e
		}
		if e = os.WriteFile(filepath.Join(stage, n), b, 0700); e != nil {
			return e
		}
		if strings.HasSuffix(n, ".lua") {
			if _, e = run(3*time.Second, "lua", "-e", fmt.Sprintf("assert(loadfile(%q))", filepath.Join(stage, n))); e != nil {
				return e
			}
		} else {
			if _, e = run(3*time.Second, "sh", "-n", filepath.Join(stage, n)); e != nil {
				return e
			}
		}
	}
	// Parse policy before changing the running deployment. This only reads UCI and geosite.
	code := fmt.Sprintf("package.path=%q..'/?.lua;'..package.path; local p=require('guard').policy(); assert(#p.domains>0 or next(p.static4) or next(p.static6),'empty policy')", stage)
	if _, e = run(20*time.Second, "lua", "-e", code); e != nil {
		return e
	}
	backup, e := a.backupBytes()
	if e != nil {
		return e
	}
	if e = writeAtomic(filepath.Join(a.cfg.DataDir, "backup-"+time.Now().Format("20060102-150405")+".zip"), backup, 0600); e != nil {
		return e
	}
	oldFiles := map[string][]byte{}
	for _, n := range names {
		p := "/usr/libexec/claude-guard/" + n
		if n == "init.sh" {
			p = "/etc/init.d/claude-guard"
		}
		b, err := os.ReadFile(p)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		oldFiles[p] = b
	}
	oldState, _ := serviceState("claude-guard")
	oldEnabled := enabled("claude-guard")
	mutated := false
	defer func() {
		if result == nil || !mutated {
			return
		}
		_, _ = run(10*time.Second, "/etc/init.d/claude-guard", "stop")
		time.Sleep(time.Second)
		for p, b := range oldFiles {
			if b == nil {
				_ = os.Remove(p)
			} else if err := writeAtomic(p, b, 0700); err != nil {
				a.audit("rollback_error", p+": "+err.Error())
			}
		}
		if exists("/etc/init.d/claude-guard") {
			if oldEnabled {
				_, _ = run(3*time.Second, "/etc/init.d/claude-guard", "enable")
			} else {
				_, _ = run(3*time.Second, "/etc/init.d/claude-guard", "disable")
			}
			if oldState["guard"] {
				_, _ = run(10*time.Second, "/etc/init.d/claude-guard", "start")
			}
		}
		a.audit("rollback", "安装未通过，已尝试恢复原脚本和自启状态；已有阻断表未删除")
	}()
	mutated = true
	if exists("/etc/init.d/claude-guard") {
		if _, e = run(15*time.Second, "/etc/init.d/claude-guard", "stop"); e != nil {
			return e
		}
		time.Sleep(2 * time.Second)
		states, ok := serviceState("claude-guard")
		if !ok || states["guard"] || states["dns-observer"] {
			return fmt.Errorf("旧守卫未停止，取消覆盖")
		}
	}
	for _, n := range names {
		b, e := os.ReadFile(filepath.Join(stage, n))
		if e != nil {
			return e
		}
		p := "/usr/libexec/claude-guard/" + n
		if n == "init.sh" {
			p = "/etc/init.d/claude-guard"
		}
		if e = writeAtomic(p, b, 0700); e != nil {
			return e
		}
	}
	if e = os.MkdirAll("/etc/claude-guard", 0700); e != nil {
		return e
	}
	if _, e = run(3*time.Second, "/etc/init.d/claude-guard", "enable"); e != nil {
		return e
	}
	if _, e = run(10*time.Second, "/etc/init.d/claude-guard", "start"); e != nil {
		return e
	}
	time.Sleep(3 * time.Second)
	s, ok := serviceState("claude-guard")
	if !ok || !s["guard"] || !s["dns-observer"] {
		return fmt.Errorf("启动未通过，旧阻断表保留，备份已保存")
	}
	return nil
}
