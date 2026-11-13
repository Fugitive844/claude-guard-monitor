package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

type SSHHost struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	Auth        string `json:"auth"`
	Password    string `json:"password,omitempty"`
	PrivateKey  string `json:"private_key,omitempty"`
	Passphrase  string `json:"passphrase,omitempty"`
	Fingerprint string `json:"fingerprint"`
	HasSecret   bool   `json:"has_secret,omitempty"`
}

func (h SSHHost) public() SSHHost {
	h.HasSecret = h.Password != "" || h.PrivateKey != ""
	h.Password, h.PrivateKey, h.Passphrase = "", "", ""
	return h
}

func (h SSHHost) authMethod() (ssh.AuthMethod, error) {
	if h.Auth == "password" && h.Password != "" {
		return ssh.Password(h.Password), nil
	}
	if h.Auth != "key" || h.PrivateKey == "" {
		return nil, errors.New("请填写密码或私钥")
	}
	var signer ssh.Signer
	var err error
	if h.Passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(h.PrivateKey), []byte(h.Passphrase))
	} else {
		signer, err = ssh.ParsePrivateKey([]byte(h.PrivateKey))
	}
	if err != nil {
		return nil, errors.New("私钥格式或私钥口令无效")
	}
	return ssh.PublicKeys(signer), nil
}

func (h SSHHost) validate() error {
	if h.Name == "" || len(h.Name) > 100 || net.ParseIP(h.Host) == nil || h.Port < 1 || h.Port > 65535 || h.Username == "" || len(h.Username) > 100 || strings.ContainsAny(h.Username, "\x00\r\n") {
		return errors.New("请填写名称、有效 IP、SSH 端口（1–65535）和用户名")
	}
	if h.Fingerprint != "" {
		b, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(h.Fingerprint, "SHA256:"))
		if !strings.HasPrefix(h.Fingerprint, "SHA256:") || err != nil || len(b) != 32 {
			return errors.New("主机指纹应为 SHA256 格式")
		}
	}
	if h.Auth != "password" && h.Auth != "key" {
		return errors.New("请选择密码或私钥认证")
	}
	if h.Password == "" && h.PrivateKey == "" && h.Passphrase == "" {
		return nil
	}
	_, err := h.authMethod()
	return err
}

func (s *SpeedService) hostsPath() string {
	return filepath.Join(filepath.Dir(s.app.configPath), "ssh-hosts.json")
}

// Called with s.mu held. Credentials never appear in list responses or history.
func (s *SpeedService) readHosts() ([]SSHHost, error) {
	b, err := os.ReadFile(s.hostsPath())
	if os.IsNotExist(err) {
		return []SSHHost{}, nil
	}
	if err != nil {
		return nil, errors.New("读取 SSH 主机失败")
	}
	var hosts []SSHHost
	if json.Unmarshal(b, &hosts) != nil {
		return nil, errors.New("SSH 主机配置格式错误")
	}
	return hosts, nil
}

func (s *SpeedService) writeHosts(hosts []SSHHost) error {
	b, err := json.MarshalIndent(hosts, "", "  ")
	if err == nil {
		err = writeAtomic(s.hostsPath(), b, 0600)
	}
	if err != nil {
		return errors.New("保存 SSH 主机失败")
	}
	return nil
}

func (s *SpeedService) saveHost(h SSHHost) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Running {
		return errors.New("请等待当前任务结束后修改主机")
	}
	hosts, err := s.readHosts()
	if err != nil {
		return err
	}
	i := -1
	for n, old := range hosts {
		if old.ID != h.ID {
			continue
		}
		i = n
		if old.Auth == h.Auth {
			if h.Password == "" {
				h.Password = old.Password
			}
			if h.PrivateKey == "" {
				h.PrivateKey, h.Passphrase = old.PrivateKey, old.Passphrase
			}
		}
	}
	if h.ID != "" && i < 0 {
		return errors.New("主机已删除，请刷新列表")
	}
	if i < 0 && len(hosts) >= 100 {
		return errors.New("最多保存 100 台 SSH 主机")
	}
	h.Name, h.Host, h.Username = strings.TrimSpace(h.Name), strings.TrimSpace(h.Host), strings.TrimSpace(h.Username)
	h.HasSecret = false
	if h.Auth == "password" {
		h.PrivateKey, h.Passphrase = "", ""
	} else {
		h.Password = ""
	}
	if err = h.validate(); err != nil {
		return err
	}
	if i >= 0 {
		hosts[i] = h
	} else {
		h.ID = randomToken()[:16]
		hosts = append(hosts, h)
	}
	return s.writeHosts(hosts)
}

func (a *App) sshHostsAPI(w http.ResponseWriter, r *http.Request) {
	s := a.speed
	if s == nil {
		respond(w, 503, map[string]string{"error": "测速服务未就绪"})
		return
	}
	var err error
	switch r.Method {
	case "GET":
		s.mu.Lock()
		hosts, e := s.readHosts()
		s.mu.Unlock()
		if e != nil {
			err = e
			break
		}
		for i := range hosts {
			hosts[i] = hosts[i].public()
		}
		respond(w, 200, map[string]any{"items": hosts})
		return
	case "POST":
		var h SSHHost
		if decodeBody(w, r, &h) != nil {
			err = errors.New("SSH 主机格式错误")
			break
		}
		err = s.saveHost(h)
	case "DELETE":
		var req struct {
			ID string `json:"id"`
		}
		if decodeBody(w, r, &req) != nil || req.ID == "" {
			err = errors.New("请选择主机")
			break
		}
		s.mu.Lock()
		if s.state.Running {
			err = errors.New("请等待当前任务结束后删除主机")
		} else {
			var hosts []SSHHost
			hosts, err = s.readHosts()
			if err == nil {
				filtered := []SSHHost{}
				for _, h := range hosts {
					if h.ID != req.ID {
						filtered = append(filtered, h)
					}
				}
				err = s.writeHosts(filtered)
			}
		}
		s.mu.Unlock()
	default:
		w.WriteHeader(405)
		return
	}
	if err != nil {
		respond(w, 400, map[string]string{"error": err.Error()})
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}

func (s *SpeedService) connectSSH(ctx context.Context, h SSHHost) (*ssh.Client, error) {
	return s.openSSH(ctx, h, true)
}

func (s *SpeedService) openSSH(ctx context.Context, h SSHHost, pin bool) (*ssh.Client, error) {
	if pin && h.Fingerprint == "" {
		s.mu.Lock()
		hosts, err := s.readHosts()
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
		for _, stored := range hosts {
			if stored.ID == h.ID {
				h.Fingerprint = stored.Fingerprint
				break
			}
		}
	}
	auth, err := h.authMethod()
	if err != nil {
		return nil, err
	}
	addr := net.JoinHostPort(h.Host, strconv.Itoa(h.Port))
	dialer := directSpeedDialer()
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, errors.New("SSH 连接失败，请检查 IP、端口和网络")
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	fingerprint := ""
	mismatch := false
	config := &ssh.ClientConfig{User: h.Username, Auth: []ssh.AuthMethod{auth}, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		fingerprint = ssh.FingerprintSHA256(key)
		if h.Fingerprint != "" && fingerprint != h.Fingerprint {
			mismatch = true
			return errors.New("host key changed")
		}
		return nil
	}}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		conn.Close()
		if mismatch {
			return nil, errors.New("SSH 主机指纹不匹配，请核实远端身份后编辑指纹")
		}
		return nil, errors.New("SSH 握手或认证失败，请检查用户名、凭据及 SSH 服务")
	}
	conn.SetDeadline(time.Time{})
	client := ssh.NewClient(c, chans, reqs)
	if pin && h.Fingerprint == "" {
		s.mu.Lock()
		hosts, e := s.readHosts()
		found := false
		if e == nil {
			for i := range hosts {
				if hosts[i].ID == h.ID && hosts[i].Host == h.Host && hosts[i].Port == h.Port {
					found = true
					if hosts[i].Fingerprint != "" && hosts[i].Fingerprint != fingerprint {
						e = errors.New("SSH 主机指纹已变化")
					} else {
						hosts[i].Fingerprint = fingerprint
						e = s.writeHosts(hosts)
					}
					break
				}
			}
		}
		s.mu.Unlock()
		if e != nil || !found {
			client.Close()
			return nil, errors.New("保存 SSH 主机指纹失败")
		}
	}
	if ctx.Err() != nil {
		client.Close()
		return nil, ctx.Err()
	}
	return client, nil
}
