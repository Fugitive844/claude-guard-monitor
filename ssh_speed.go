package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

type SSHSpeedRequest struct {
	HostID       string `json:"host_id"`
	SourceHostID string `json:"source_host_id,omitempty"`
	Mode         string `json:"mode"`
	Protocol     string `json:"protocol"`
	Direction    string `json:"direction"`
	Port         int    `json:"port"`
	Seconds      int    `json:"seconds"`
	Bandwidth    int    `json:"bandwidth"`
	Parallel     int    `json:"parallel"`
	Omit         int    `json:"omit"`
	TCPBandwidth int    `json:"tcp_bandwidth"`
	UDPLength    int    `json:"udp_length"`
}

func (r SSHSpeedRequest) validate() error {
	if r.SourceHostID != "" && (r.Mode != "iperf3" || r.SourceHostID == r.HostID) {
		return errors.New("主机互测请选择两台不同主机，使用 iperf3")
	}
	if r.Mode == "check" || r.Mode == "install" {
		return nil
	}
	if r.Mode != "ssh" && r.Mode != "iperf3" {
		return errors.New("测速模式无效")
	}
	if r.Direction != "both" && r.Direction != "download" && r.Direction != "upload" {
		return errors.New("测速方向无效")
	}
	if r.Seconds < 3 || r.Seconds > 60 {
		return errors.New("每方向时限为 3–60 秒")
	}
	if r.Mode == "iperf3" {
		if r.Parallel < 0 || r.Parallel > 8 || r.Omit < 0 || r.Omit > 10 || r.TCPBandwidth < 0 || r.TCPBandwidth > 10000 {
			return errors.New("TCP 并发为 1–8，预热为 0–10 秒，TCP 总限速为 0–10000 Mbps（0 不限速）")
		}
		if r.UDPLength != 0 && (r.UDPLength < 64 || r.UDPLength > 65507) {
			return errors.New("UDP 数据包长度为 64–65507 字节")
		}
		if r.Port < 1 || r.Port > 65535 {
			return errors.New("iperf3 端口范围为 1–65535")
		}
		if r.Protocol != "tcp" && r.Protocol != "udp" && r.Protocol != "both" {
			return errors.New("请选择 TCP、UDP 或 TCP + UDP")
		}
		if r.Bandwidth < 1 || r.Bandwidth > 10000 {
			return errors.New("UDP 目标速率范围为 1–10000 Mbps")
		}
	}
	return nil
}

func (a *App) sshStartAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	if a.speed == nil {
		respond(w, 503, map[string]string{"error": "测速服务未就绪"})
		return
	}
	var req SSHSpeedRequest
	if decodeBody(w, r, &req) != nil {
		respond(w, 400, map[string]string{"error": "SSH 测速请求格式错误"})
		return
	}
	if err := a.speed.startSSH(req); err != nil {
		respond(w, 409, map[string]string{"error": err.Error()})
		return
	}
	respond(w, 202, a.speedStatus())
}

func (s *SpeedService) startSSH(req SSHSpeedRequest) error {
	if req.Mode == "" {
		req.Mode = "iperf3"
	}
	if req.Seconds == 0 {
		req.Seconds = 20
	}
	if req.Parallel == 0 {
		req.Parallel = 1
	}
	if req.UDPLength == 0 {
		req.UDPLength = 1200
	}
	if s.app.demo || runtime.GOOS != "linux" {
		return errors.New("SSH 操作需在 Linux 软路由上运行")
	}
	if err := req.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Running {
		return errors.New("已有测速任务，请等待完成或停止")
	}
	hosts, err := s.readHosts()
	if err != nil {
		return err
	}
	var host SSHHost
	for _, h := range hosts {
		if h.ID == req.HostID {
			host = h
			break
		}
	}
	if host.ID == "" {
		return errors.New("请选择已保存的 SSH 主机")
	}
	if _, err := host.authMethod(); err != nil {
		return errors.New("请先在 SSH 管理中补充有效的密码或私钥")
	}
	var source SSHHost
	if req.SourceHostID != "" {
		for _, h := range hosts {
			if h.ID == req.SourceHostID {
				source = h
				break
			}
		}
		if source.ID == "" {
			return errors.New("请选择已保存的源主机")
		}
		if _, err := source.authMethod(); err != nil {
			return errors.New("请先补充源主机的登录凭据")
		}
		if source.Host == host.Host && source.Port == host.Port {
			return errors.New("源和目标的 SSH 地址相同，请选择两台不同主机")
		}
	}
	if req.Mode == "iperf3" && req.Port == host.Port {
		return errors.New("测速端口不能与 SSH 端口相同")
	}
	directions := []string{req.Direction}
	if req.Direction == "both" {
		directions = []string{"download", "upload"}
	}
	protocols := []string{req.Protocol}
	if req.Protocol == "both" {
		protocols = []string{"tcp", "udp"}
	}
	if req.Mode != "iperf3" {
		protocols = []string{"tcp"}
	}
	if req.Mode == "check" || req.Mode == "install" {
		directions = []string{req.Mode}
	}
	ctx, cancel := context.WithCancel(s.app.ctx)
	s.cancel = cancel
	s.state = SpeedState{Job: randomToken()[:16], Running: true, Started: time.Now().UnixMilli(), Total: len(directions) * len(protocols), Current: SpeedResult{NodeID: "ssh:" + host.ID, NodeName: host.Name, Status: "starting", Mode: req.Mode}}
	s.state.Current.SourceHostID, s.state.Current.SourceHostName = source.ID, source.Name
	if req.Mode == "iperf3" {
		s.state.Current.TestConfig = &req
	}
	cfg := s.settings
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		s.runSSH(ctx, host, source, req, cfg, directions, protocols)
	}()
	return nil
}

func (s *SpeedService) runSSH(ctx context.Context, host, source SSHHost, req SSHSpeedRequest, cfg SpeedSettings, directions, protocols []string) {
	defer func() {
		s.mu.Lock()
		s.state.Running = false
		s.state.Stopping = false
		s.state.Finished = time.Now().UnixMilli()
		s.cancel = nil
		s.mu.Unlock()
	}()
	for _, protocol := range protocols {
		for _, direction := range directions {
			if ctx.Err() != nil {
				return
			}
			s.mu.Lock()
			job := s.state.Job
			s.mu.Unlock()
			r := SpeedResult{Job: job, Time: time.Now().UnixMilli(), NodeID: "ssh:" + host.ID, NodeName: host.Name, Direction: direction, Mode: req.Mode, Protocol: protocol, Target: host.Host + " · " + req.Mode + " / " + strings.ToUpper(protocol), Status: "starting"}
			if req.Mode == "iperf3" {
				r.TestConfig = &req
			}
			if source.ID != "" {
				r.SourceHostID, r.SourceHostName = source.ID, source.Name
				from, to := source.Name+" ("+source.Host+")", host.Name+" ("+host.Host+")"
				if direction == "download" {
					from, to = to, from
				}
				r.Target = from + " → " + to + " · " + strings.ToUpper(protocol)
			}
			if req.Mode == "iperf3" {
				r.Target += " :" + strconv.Itoa(req.Port)
			}
			update := func(v SpeedResult) { s.mu.Lock(); s.state.Current = v; s.mu.Unlock() }
			update(r)
			timeout := time.Duration(req.Seconds+req.Omit+30) * time.Second
			if req.Mode == "install" {
				timeout = 3 * time.Minute
			}
			if req.Mode == "check" {
				timeout = 20 * time.Second
			}
			operationCtx, operationCancel := context.WithTimeout(ctx, timeout)
			client, err := s.connectSSH(operationCtx, host)
			if err == nil {
				stop := context.AfterFunc(operationCtx, func() { client.Close() })
				r.Status = "running"
				r.Time = time.Now().UnixMilli()
				update(r)
				switch req.Mode {
				case "check", "install":
					r.Message, err = sshSoftware(operationCtx, client, req.Mode == "install")
				case "ssh":
					r, err = measureSSH(operationCtx, client, req, cfg, r, update)
				case "iperf3":
					if source.ID == "" {
						r, err = measureIPerf(operationCtx, client, host, req, r, update)
					} else {
						var sender *ssh.Client
						sender, err = s.connectSSH(operationCtx, source)
						if err == nil {
							r, err = measurePeerIPerf(operationCtx, sender, client, host, req, r, update)
							sender.Close()
						} else {
							err = fmt.Errorf("源主机：%w", err)
						}
					}
				}
				stop()
				client.Close()
			}
			if operationCtx.Err() != nil && ctx.Err() == nil {
				err = errors.New("SSH 操作超时，请检查远端连接或软件源")
			}
			operationCancel()
			if err != nil {
				r.Status = "error"
				r.Message = err.Error()
			} else if r.Status == "running" {
				r.Status = "complete"
			}
			if ctx.Err() != nil {
				r.Status = "cancelled"
				r.Message = "任务已停止"
			}
			if req.Mode == "check" || req.Mode == "install" {
				r.Seconds = float64(time.Now().UnixMilli()-r.Time) / 1000
			}
			if err := s.app.db.saveSpeed(&r); err != nil {
				s.mu.Lock()
				s.state.Error = "测速记录保存失败"
				s.mu.Unlock()
			}
			s.mu.Lock()
			s.state.Current = r
			s.state.Results = append(s.state.Results, r)
			s.state.Done++
			s.mu.Unlock()
		}
	}
}

type sshSpeedCounter struct{ n atomic.Int64 }

func (c *sshSpeedCounter) Write(p []byte) (int, error) { c.n.Add(int64(len(p))); return len(p), nil }

type timedSpeedPayload struct {
	until   time.Time
	payload speedPayload
}

func (p *timedSpeedPayload) Read(b []byte) (int, error) {
	if time.Now().After(p.until) {
		return 0, io.EOF
	}
	return p.payload.Read(b)
}

func measureSSH(ctx context.Context, client *ssh.Client, req SSHSpeedRequest, cfg SpeedSettings, r SpeedResult, update func(SpeedResult)) (SpeedResult, error) {
	session, err := client.NewSession()
	if err != nil {
		return r, errors.New("无法创建 SSH 测速会话")
	}
	defer session.Close()
	start := time.Now()
	limit := int64(cfg.DownloadMiB) * 1024 * 1024
	var count sshSpeedCounter
	var reply boundedSpeedOutput
	command := ""
	if r.Direction == "upload" {
		limit = int64(cfg.UploadMiB) * 1024 * 1024
		session.Stdin = io.LimitReader(&timedSpeedPayload{until: start.Add(time.Duration(req.Seconds) * time.Second)}, limit)
		session.Stdout = &reply
		command = "LC_ALL=C wc -c"
	} else {
		session.Stdout = &count
		command = fmt.Sprintf("head -c %d /dev/zero", limit)
	}
	if err = session.Start(command); err != nil {
		return r, errors.New("远端无法启动原生 SSH 测速")
	}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	duration := time.Duration(req.Seconds) * time.Second
	if r.Direction == "upload" {
		duration += 3 * time.Second
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	timedOut := false
loop:
	for {
		select {
		case err = <-done:
			break loop
		case <-ctx.Done():
			client.Close()
			<-done
			err = ctx.Err()
			break loop
		case <-timer.C:
			timedOut = true
			client.Close()
			err = <-done
			break loop
		case <-tick.C:
			r.Bytes = count.n.Load()
			r.Seconds = time.Since(start).Seconds()
			r.Mbps = float64(r.Bytes) * 8 / r.Seconds / 1e6
			update(r)
		}
	}
	r.Seconds = time.Since(start).Seconds()
	r.Bytes = count.n.Load()
	if r.Direction == "upload" {
		if err != nil {
			return r, errors.New("SSH 上传未取得远端接收确认，请缩短上传流量或检查网络")
		}
		n, e := strconv.ParseInt(strings.TrimSpace(reply.String()), 10, 64)
		if e != nil || n < 0 || n > limit {
			return r, errors.New("远端上传确认无效（需要 wc 命令）")
		}
		r.Bytes = n
	} else if err != nil && !(timedOut && r.Bytes > 0) {
		return r, errors.New("SSH 下载失败（需要 head 命令及 /dev/zero）")
	}
	if r.Bytes == 0 {
		return r, errors.New("未接收到有效测速数据")
	}
	r.Mbps = float64(r.Bytes) * 8 / r.Seconds / 1e6
	if timedOut || r.Bytes < limit {
		r.Status = "limited"
	} else {
		r.Status = "complete"
	}
	return r, nil
}

const installIPerfScript = `set -e
if command -v iperf3 >/dev/null 2>&1; then iperf3 --version; exit; fi
if [ "$(id -u)" != 0 ]; then
  if command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then exec sudo -n sh -c "$1" sh "$1"; fi
  exit 77
fi
if command -v opkg >/dev/null 2>&1; then opkg update && opkg install iperf3
elif command -v apk >/dev/null 2>&1; then apk add --no-cache iperf3
elif command -v apt-get >/dev/null 2>&1; then
  printf 'iperf3 iperf3/start_daemon boolean false\n' | debconf-set-selections
  apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y iperf3
elif command -v dnf >/dev/null 2>&1; then dnf install -y iperf3
elif command -v yum >/dev/null 2>&1; then yum install -y iperf3
else exit 78
fi
iperf3 --version`

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func sshSoftware(ctx context.Context, client *ssh.Client, install bool) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { client.Close() })
	defer stop()
	if !install {
		remote, e := sshOutput(client, "command -v iperf3 >/dev/null 2>&1 && iperf3 --version")
		_, le := exec.LookPath("iperf3")
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		msg := "SSH 连接成功"
		if e != nil {
			msg += "；远端未检测到 iperf3"
		} else {
			msg += "；远端 " + firstLine(remote)
		}
		if le != nil {
			msg += "；软路由未安装 iperf3"
		} else {
			msg += "；软路由 iperf3 已就绪"
		}
		return msg, nil
	}
	command := "sh -c " + shellQuote(installIPerfScript) + " sh " + shellQuote(installIPerfScript)
	if _, err := sshOutput(client, command); err != nil {
		return "", errors.New("远端安装 iperf3 失败：需要 root 或免密 sudo、受支持的包管理器及软件源连接；也可手动安装")
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", installIPerfScript, "sh", installIPerfScript)
	speedProcess(cmd)
	if err := cmd.Run(); err != nil {
		return "", errors.New("远端 iperf3 已就绪，软路由安装失败，请检查软件源或手动安装 iperf3")
	}
	return "远端及软路由 iperf3 已就绪", nil
}

type boundedSpeedOutput struct{ bytes.Buffer }

func (b *boundedSpeedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < 1024*1024 {
		_, _ = b.Buffer.Write(p[:min(n, 1024*1024-b.Len())])
	}
	return n, nil
}
func sshOutput(client *ssh.Client, command string) ([]byte, error) {
	s, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var out boundedSpeedOutput
	s.Stdout = &out
	err = s.Run(command)
	return out.Bytes(), err
}
func firstLine(b []byte) string { return strings.SplitN(strings.TrimSpace(string(b)), "\n", 2)[0] }
