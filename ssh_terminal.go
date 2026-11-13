package main

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

type terminalMessage struct {
	Type   string `json:"type"`
	CSRF   string `json:"csrf"`
	HostID string `json:"host_id"`
	Data   string `json:"data"`
	Cols   int    `json:"cols"`
	Rows   int    `json:"rows"`
}

func terminalSize(cols, rows int) bool { return cols >= 2 && cols <= 500 && rows >= 2 && rows <= 200 }

type terminalOutput struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (o *terminalOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := o.conn.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (o *terminalOutput) status(kind, message string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_ = o.conn.WriteJSON(map[string]string{"type": kind, "message": message})
}

func (a *App) sshTerminal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	if a.speed == nil {
		respond(w, 503, map[string]string{"error": "SSH 服务未就绪"})
		return
	}
	u := websocket.Upgrader{CheckOrigin: sameOrigin, ReadBufferSize: 4096, WriteBufferSize: 16384}
	c, err := u.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	c.SetReadLimit(65536)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var first terminalMessage
	if c.ReadJSON(&first) != nil {
		return
	}
	login, ok := a.session(r)
	if !ok || first.Type != "open" || !constantEqual(first.CSRF, login.CSRF) {
		return
	}
	c.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.SetPongHandler(func(string) error { return c.SetReadDeadline(time.Now().Add(60 * time.Second)) })
	out := &terminalOutput{conn: c}
	if !terminalSize(first.Cols, first.Rows) {
		out.status("error", "终端尺寸无效")
		return
	}
	a.speed.mu.Lock()
	hosts, err := a.speed.readHosts()
	a.speed.mu.Unlock()
	if err != nil {
		out.status("error", err.Error())
		return
	}
	var host SSHHost
	for _, h := range hosts {
		if h.ID == first.HostID {
			host = h
			break
		}
	}
	if host.ID == "" {
		out.status("error", "SSH 主机不存在")
		return
	}
	if _, err = host.authMethod(); err != nil {
		out.status("error", err.Error())
		return
	}
	ctx, cancel := context.WithCancel(a.ctx)
	defer cancel()
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		heartbeats := 0
		for {
			select {
			case <-ctx.Done():
				c.Close()
				return
			case <-tick.C:
				if _, valid := a.session(r); !valid {
					cancel()
				}
				heartbeats++
				if heartbeats%25 == 0 && c.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
					cancel()
				}
			}
		}
	}()
	setupTimeout := time.AfterFunc(15*time.Second, cancel)
	defer setupTimeout.Stop()
	client, err := a.speed.connectSSH(ctx, host)
	if err != nil {
		out.status("error", err.Error())
		return
	}
	defer client.Close()
	stop := context.AfterFunc(ctx, func() { client.Close() })
	defer stop()
	term, err := client.NewSession()
	if err != nil {
		out.status("error", "创建 SSH 会话失败")
		return
	}
	defer term.Close()
	stdin, err := term.StdinPipe()
	if err != nil {
		out.status("error", "打开终端输入失败")
		return
	}
	term.Stdout, term.Stderr = out, out
	if err = term.RequestPty("xterm-256color", first.Rows, first.Cols, ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}); err != nil {
		out.status("error", "远端不支持交互终端")
		return
	}
	if err = term.Shell(); err != nil {
		out.status("error", "启动远程 Shell 失败")
		return
	}
	setupTimeout.Stop()
	out.status("ready", "已连接")
	go func() {
		defer cancel()
		for {
			var m terminalMessage
			if c.ReadJSON(&m) != nil {
				return
			}
			switch m.Type {
			case "input":
				if _, err := stdin.Write([]byte(m.Data)); err != nil {
					return
				}
			case "resize":
				if !terminalSize(m.Cols, m.Rows) || term.WindowChange(m.Rows, m.Cols) != nil {
					return
				}
			default:
				return
			}
		}
	}()
	_ = term.Wait()
	out.status("closed", "远程终端已断开")
}
