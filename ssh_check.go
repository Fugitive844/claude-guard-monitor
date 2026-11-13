package main

import (
	"context"
	"net/http"
	"strings"
	"time"
)

func (a *App) sshCheckAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	if a.speed == nil {
		respond(w, 503, map[string]string{"error": "SSH 服务未就绪"})
		return
	}
	var host SSHHost
	if decodeBody(w, r, &host) != nil {
		respond(w, 400, map[string]string{"error": "SSH 主机格式错误"})
		return
	}
	if host.ID != "" {
		a.speed.mu.Lock()
		hosts, err := a.speed.readHosts()
		a.speed.mu.Unlock()
		if err != nil {
			respond(w, 500, map[string]string{"error": err.Error()})
			return
		}
		found := false
		for _, old := range hosts {
			if old.ID != host.ID {
				continue
			}
			found = true
			if host.Auth == old.Auth {
				if host.Password == "" {
					host.Password = old.Password
				}
				if host.PrivateKey == "" {
					host.PrivateKey, host.Passphrase = old.PrivateKey, old.Passphrase
				}
			}
			break
		}
		if !found {
			respond(w, 400, map[string]string{"error": "主机已删除，请刷新列表"})
			return
		}
	}
	host.Host, host.Username = strings.TrimSpace(host.Host), strings.TrimSpace(host.Username)
	host.Name = "连接测试"
	if host.Auth == "password" {
		host.PrivateKey, host.Passphrase = "", ""
	} else {
		host.Password = ""
	}
	if err := host.validate(); err != nil {
		respond(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if _, err := host.authMethod(); err != nil {
		respond(w, 400, map[string]string{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	client, err := a.speed.openSSH(ctx, host, false)
	if err != nil {
		respond(w, 502, map[string]string{"error": err.Error()})
		return
	}
	client.Close()
	respond(w, 200, map[string]string{"message": "连接成功，SSH 端口可达且登录认证通过。"})
}
