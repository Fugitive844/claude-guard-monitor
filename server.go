package main

import (
	"context"
	"crypto/tls"
	"embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed web/* bundle/*
var assets embed.FS

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	return d.Decode(v)
}
func (a *App) allowed(r *http.Request) bool {
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		return false
	}
	ip, e := netip.ParseAddr(host)
	if e != nil {
		return false
	}
	ip = ip.Unmap()
	for _, s := range a.cfg.AllowedCIDRs {
		p, e := netip.ParsePrefix(s)
		if e == nil && p.Contains(ip) {
			return true
		}
	}
	return false
}
func (a *App) session(r *http.Request) (session, bool) {
	c, e := r.Cookie("cg_session")
	if e != nil {
		return session{}, false
	}
	a.mu.RLock()
	s, ok := a.sessions[c.Value]
	a.mu.RUnlock()
	return s, ok && time.Now().Before(s.Expiry)
}
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	u, e := url.Parse(o)
	return e == nil && u.Host == r.Host && ((r.TLS != nil && u.Scheme == "https") || (r.TLS == nil && u.Scheme == "http"))
}
func (a *App) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'")
		if !a.allowed(r) {
			http.Error(w, "仅允许配置的管理网段访问", 403)
			return
		}
		if r.URL.Path == "/api/login" {
			a.login(w, r)
			return
		}
		pageRoute := false
		switch r.URL.Path {
		case "/", "/overview", "/blocks", "/timeline", "/speed", "/speed-history", "/ssh", "/manage":
			pageRoute = true
		}
		if pageRoute || r.URL.Path == "/app.js" || r.URL.Path == "/privacy.js" || r.URL.Path == "/speed.js" || r.URL.Path == "/ssh-speed.js" || r.URL.Path == "/ssh-details.js" || r.URL.Path == "/style.css" || r.URL.Path == "/ssh-terminal.js" || r.URL.Path == "/vendor/xterm.js" || r.URL.Path == "/vendor/addon-fit.js" || r.URL.Path == "/vendor/xterm.css" {
			if r.Method != "GET" {
				w.WriteHeader(405)
				return
			}
			p := "web" + r.URL.Path
			if pageRoute {
				p = "web/index.html"
			}
			b, e := assets.ReadFile(p)
			if e != nil {
				http.NotFound(w, r)
				return
			}
			switch {
			case pageRoute:
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
			case strings.HasSuffix(r.URL.Path, ".js"):
				w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			case strings.HasSuffix(r.URL.Path, ".css"):
				w.Header().Set("Content-Type", "text/css; charset=utf-8")
			}
			_, _ = w.Write(b)
			return
		}
		s, ok := a.session(r)
		if !ok {
			respond(w, 401, map[string]string{"error": "请先登录"})
			return
		}
		if r.Method != "GET" {
			if !sameOrigin(r) || !constantEqual(r.Header.Get("X-CSRF-Token"), s.CSRF) {
				respond(w, 403, map[string]string{"error": "请求校验失败"})
				return
			}
		}
		switch r.URL.Path {
		case "/api/ssh/terminal":
			a.sshTerminal(w, r)
		case "/api/speed/ssh/hosts":
			a.sshHostsAPI(w, r)
		case "/api/speed/ssh/check":
			a.sshCheckAPI(w, r)
		case "/api/speed/ssh/start":
			a.sshStartAPI(w, r)
		case "/api/speed/targets", "/api/speed/nodes", "/api/speed/settings", "/api/speed/start", "/api/speed/stop", "/api/speed/status", "/api/speed/history":
			a.speedAPI(w, r)
		case "/api/session":
			respond(w, 200, map[string]any{"csrf": s.CSRF, "expires": s.Expiry.UnixMilli()})
		case "/api/logout":
			if r.Method != "POST" {
				w.WriteHeader(405)
				return
			}
			c, _ := r.Cookie("cg_session")
			a.mu.Lock()
			delete(a.sessions, c.Value)
			a.mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: "cg_session", Path: "/", MaxAge: -1, HttpOnly: true, Secure: !a.demo, SameSite: http.SameSiteStrictMode})
			respond(w, 200, map[string]bool{"ok": true})
		case "/api/status":
			respond(w, 200, a.getSnapshot())
		case "/api/events":
			from, to, before, e := queryRange(r.URL.Query(), time.Now().UnixMilli())
			if e != nil {
				respond(w, 400, map[string]string{"error": e.Error()})
				return
			}
			q := r.URL.Query().Get("q")
			if len(q) > 200 {
				w.WriteHeader(400)
				return
			}
			v, e := a.db.eventsRange(q, before, 51, from, to)
			if e != nil {
				respond(w, 500, map[string]string{"error": "读取历史失败"})
				return
			}
			more := len(v) > 50
			if more {
				v = v[:50]
			}
			var next int64
			if more {
				next = v[len(v)-1].ID
			}
			respond(w, 200, map[string]any{"items": v, "has_more": more, "next_before": next, "from": from, "to": to})
		case "/api/timeline":
			from, to, before, e := queryRange(r.URL.Query(), time.Now().UnixMilli())
			if e != nil {
				respond(w, 400, map[string]string{"error": e.Error()})
				return
			}
			v, e := a.db.timeline(from, to, before)
			if e != nil {
				respond(w, 500, map[string]string{"error": "读取时间线失败"})
				return
			}
			more := len(v) > 50
			if more {
				v = v[:50]
			}
			var next int64
			if more {
				next = v[len(v)-1]["id"].(int64)
			}
			respond(w, 200, map[string]any{"items": v, "has_more": more, "next_before": next, "from": from, "to": to})
		case "/api/environment":
			respond(w, 200, a.environment())
		case "/api/config":
			if r.Method == "GET" {
				a.mu.RLock()
				g := a.cfg.Guard
				a.mu.RUnlock()
				respond(w, 200, g)
				return
			}
			if r.Method != "POST" {
				w.WriteHeader(405)
				return
			}
			var g GuardConfig
			if e := decodeBody(w, r, &g); e != nil {
				respond(w, 400, map[string]string{"error": "参数格式错误"})
				return
			}
			if e := validGuard(g); e != nil {
				respond(w, 400, map[string]string{"error": e.Error()})
				return
			}
			if !a.lifeMu.TryLock() {
				respond(w, 409, map[string]string{"error": "管理任务进行中"})
				return
			}
			a.mu.Lock()
			next := a.cfg
			next.Guard = g
			e := saveConfig(a.configPath, next)
			if e == nil {
				a.cfg.Guard = g
			}
			a.mu.Unlock()
			a.lifeMu.Unlock()
			if e != nil {
				respond(w, 500, map[string]string{"error": "保存失败"})
				return
			}
			a.audit("config", "守卫部署参数已保存，执行安装/更新后应用；未修改 PassWall")
			respond(w, 200, map[string]bool{"ok": true})
		case "/api/lifecycle":
			if r.Method != "POST" {
				w.WriteHeader(405)
				return
			}
			var v struct {
				Action  string `json:"action"`
				Confirm string `json:"confirm"`
			}
			if e := decodeBody(w, r, &v); e != nil {
				respond(w, 400, map[string]string{"error": "请求格式错误"})
				return
			}
			if v.Action == "disable" || v.Action == "uninstall" {
				if v.Confirm != "解除防护" {
					respond(w, 400, map[string]string{"error": "请输入：解除防护"})
					return
				}
			}
			if !validAction(v.Action) {
				respond(w, 400, map[string]string{"error": "未知操作"})
				return
			}
			if a.demo {
				respond(w, 400, map[string]string{"error": "演示模式不操作系统"})
				return
			}
			if !a.lifeMu.TryLock() {
				respond(w, 409, map[string]string{"error": "已有管理任务进行中"})
				return
			}
			a.mu.Lock()
			a.op = v.Action
			a.mu.Unlock()
			go func() {
				defer a.lifeMu.Unlock()
				a.audit("action_start", v.Action)
				e := a.lifecycle(v.Action)
				if e != nil {
					a.audit("action_error", v.Action+": "+e.Error())
					a.mu.Lock()
					a.lastError = e.Error()
					a.mu.Unlock()
				} else {
					a.audit("action_done", v.Action+" 完成")
				}
				a.mu.Lock()
				a.op = ""
				a.mu.Unlock()
			}()
			respond(w, 202, map[string]bool{"accepted": true})
		case "/api/export":
			a.export(w, r)
		case "/api/backup":
			if r.Method != "POST" {
				w.WriteHeader(405)
				return
			}
			b, e := a.backupBytes()
			if e != nil {
				respond(w, 500, map[string]string{"error": "备份失败"})
				return
			}
			w.Header().Set("Content-Type", "application/zip")
			w.Header().Set("Content-Disposition", "attachment; filename=claude-guard-backup.zip")
			_, _ = w.Write(b)
		case "/ws":
			a.ws(w, r, s)
		default:
			http.NotFound(w, r)
		}
	})
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	if !sameOrigin(r) {
		w.WriteHeader(403)
		return
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	a.mu.Lock()
	now := time.Now()
	if now.Sub(a.lastLoginSweep) > time.Minute {
		for ip, t := range a.loginAttempts {
			if now.Sub(t) > time.Minute {
				delete(a.loginAttempts, ip)
			}
		}
		for id, s := range a.sessions {
			if now.After(s.Expiry) {
				delete(a.sessions, id)
			}
		}
		a.lastLoginSweep = now
	}
	last := a.loginAttempts[host]
	if now.Sub(last) < 2*time.Second {
		a.mu.Unlock()
		respond(w, 429, map[string]string{"error": "请稍后重试"})
		return
	}
	if len(a.loginAttempts) > 1024 {
		a.mu.Unlock()
		w.WriteHeader(429)
		return
	}
	a.loginAttempts[host] = now
	a.mu.Unlock()
	var v struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if decodeBody(w, r, &v) != nil || v.Username != "admin" || !constantEqual(passwordHash(v.Password), a.cfg.PasswordHash) {
		respond(w, 401, map[string]string{"error": "用户名或密码错误"})
		return
	}
	id := randomToken()
	s := session{Expiry: now.Add(8 * time.Hour), CSRF: randomToken()}
	a.mu.Lock()
	if len(a.sessions) >= 64 {
		a.mu.Unlock()
		w.WriteHeader(429)
		return
	}
	a.sessions[id] = s
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "cg_session", Value: id, Path: "/", HttpOnly: true, Secure: !a.demo, SameSite: http.SameSiteStrictMode, MaxAge: 28800})
	respond(w, 200, map[string]string{"csrf": s.CSRF})
}
func (a *App) ws(w http.ResponseWriter, r *http.Request, s session) {
	u := websocket.Upgrader{CheckOrigin: sameOrigin, ReadBufferSize: 1024, WriteBufferSize: 16384}
	c, e := u.Upgrade(w, r, nil)
	if e != nil {
		return
	}
	defer c.Close()
	c.SetReadLimit(1024)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, e := c.ReadMessage(); e != nil {
				return
			}
		}
	}()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		if time.Now().After(s.Expiry) {
			return
		}
		if _, ok := a.session(r); !ok {
			return
		}
		v, _ := a.db.events("", 0, 6)
		c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if e = c.WriteJSON(map[string]any{"type": "snapshot", "status": a.getSnapshot(), "events": v, "speed": a.speedStatus()}); e != nil {
			return
		}
		select {
		case <-done:
			return
		case <-tick.C:
		case <-a.ctx.Done():
			return
		}
	}
}
func csvCell(s string) string {
	if strings.ContainsAny(s[:min(len(s), 1)], "=+-@\t\r") {
		return "'" + s
	}
	return s
}
func (a *App) export(w http.ResponseWriter, r *http.Request) {
	from, to, _, err := queryRange(r.URL.Query(), time.Now().UnixMilli())
	if err != nil {
		respond(w, 400, map[string]string{"error": err.Error()})
		return
	}
	q := r.URL.Query().Get("q")
	if len(q) > 200 {
		w.WriteHeader(400)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=claude-guard-events.csv")
	_, _ = io.WriteString(w, "\xef\xbb\xbf")
	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write([]string{"首次时间", "末次时间", "来源IP", "目标IP", "来源端口", "目标端口", "协议", "采集包数", "候选域名", "原因"})
	var before int64
	for page := 0; page < 50; page++ {
		v, e := a.db.eventsRange(q, before, 200, from, to)
		if e != nil || len(v) == 0 {
			return
		}
		for _, x := range v {
			_ = cw.Write([]string{time.UnixMilli(x.First).Format(time.RFC3339Nano), time.UnixMilli(x.Last).Format(time.RFC3339Nano), x.Src, x.Dst, strconv.Itoa(x.SPort), strconv.Itoa(x.DPort), x.Proto, strconv.FormatInt(x.Packets, 10), csvCell(x.Domains), csvCell(x.Reason)})
			before = x.ID
		}
	}
}
func queryRange(q url.Values, now int64) (from, to, before int64, err error) {
	to = now
	from = to - 24*60*60*1000
	if q.Has("from") || q.Has("to") {
		from, err = strconv.ParseInt(q.Get("from"), 10, 64)
		if err == nil {
			to, err = strconv.ParseInt(q.Get("to"), 10, 64)
		}
		if err != nil {
			return 0, 0, 0, fmt.Errorf("请提供有效的开始和结束时间")
		}
	}
	if from < 0 || to <= from || to-from > 31*24*60*60*1000 {
		return 0, 0, 0, fmt.Errorf("结束时间须晚于开始时间，查询范围最多 31 天")
	}
	if q.Has("before") {
		before, err = strconv.ParseInt(q.Get("before"), 10, 64)
		if err != nil || before < 0 {
			return 0, 0, 0, fmt.Errorf("无效的分页游标")
		}
	}
	return
}

func (a *App) serve() error {
	s := &http.Server{Addr: a.cfg.Listen, Handler: a.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	go func() {
		<-a.ctx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	}()
	fmt.Printf("Claude Guard Monitor %s listening on %s (demo=%t)\n", version, a.cfg.Listen, a.demo)
	if a.demo {
		return s.ListenAndServe()
	}
	return s.ListenAndServeTLS(filepath.Join(filepath.Dir(a.configPath), "server.crt"), filepath.Join(filepath.Dir(a.configPath), "server.key"))
}
