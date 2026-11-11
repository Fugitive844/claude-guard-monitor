package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/proxy"
)

type SpeedSettings struct {
	Target       string `json:"target"`
	UploadTarget string `json:"upload_target"`
	DownloadURL  string `json:"download_url"`
	UploadURL    string `json:"upload_url"`
	UploadMethod string `json:"upload_method"`
	Seconds      int    `json:"seconds"`
	DownloadMiB  int    `json:"download_mib"`
	UploadMiB    int    `json:"upload_mib"`
}

func defaultSpeedSettings() SpeedSettings {
	return SpeedSettings{Target: "cloudflare", UploadTarget: "cloudflare", UploadMethod: "PUT", Seconds: 10, DownloadMiB: 100, UploadMiB: 50}
}

func (s SpeedSettings) validate() error {
	if _, ok := speedTarget(s.Target); !ok {
		return errors.New("测速目标无效")
	}
	if target, ok := speedTarget(s.UploadTarget); !ok || !target.Upload {
		return errors.New("上传目标无效或不支持上传")
	}
	if s.Seconds < 3 || s.Seconds > 60 || s.DownloadMiB < 1 || s.DownloadMiB > 500 || s.UploadMiB < 1 || s.UploadMiB > 200 {
		return errors.New("每方向时限为 3–60 秒，下载 1–500 MiB，上传 1–200 MiB")
	}
	if s.UploadMethod != "PUT" && s.UploadMethod != "POST" {
		return errors.New("上传方式应为 PUT 或 POST")
	}
	for _, raw := range []string{s.DownloadURL, s.UploadURL} {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || len(raw) > 4096 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			return errors.New("自定义地址需为完整 HTTPS URL，可包含预签名参数")
		}
	}
	return nil
}

type SpeedNode struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Host      string `json:"host"`
	Group     string `json:"group"`
	Core      string `json:"core"`
	Protocol  string `json:"protocol"`
	Transport string `json:"transport"`
	Supported bool   `json:"supported"`
	Reason    string `json:"reason"`
}
type SpeedRequest struct {
	Nodes        []string `json:"nodes"`
	Direction    string   `json:"direction"`
	Target       string   `json:"target"`
	UploadTarget string   `json:"upload_target"`
}
type SpeedResult struct {
	ID         int64   `json:"id"`
	Time       int64   `json:"time"`
	Job        string  `json:"job"`
	NodeID     string  `json:"node_id"`
	NodeName   string  `json:"node_name"`
	Direction  string  `json:"direction"`
	Target     string  `json:"target"`
	Status     string  `json:"status"`
	Bytes      int64   `json:"bytes"`
	Seconds    float64 `json:"seconds"`
	Mbps       float64 `json:"mbps"`
	HTTPStatus int     `json:"http_status"`
	Edge       string  `json:"edge"`
	Message    string  `json:"message"`
}
type SpeedState struct {
	Job      string        `json:"job"`
	Running  bool          `json:"running"`
	Stopping bool          `json:"stopping"`
	Started  int64         `json:"started"`
	Finished int64         `json:"finished"`
	Total    int           `json:"total"`
	Done     int           `json:"done"`
	Current  SpeedResult   `json:"current"`
	Results  []SpeedResult `json:"results,omitempty"`
	Error    string        `json:"error,omitempty"`
}
type SpeedService struct {
	app      *App
	mu       sync.Mutex
	settings SpeedSettings
	state    SpeedState
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

func newSpeedService(a *App) *SpeedService {
	s := &SpeedService{app: a, settings: defaultSpeedSettings()}
	if b, e := os.ReadFile(s.settingsPath()); e == nil {
		if json.Unmarshal(b, &s.settings) != nil || s.settings.validate() != nil {
			s.settings = defaultSpeedSettings()
		}
	}
	return s
}
func (s *SpeedService) settingsPath() string {
	return filepath.Join(filepath.Dir(s.app.configPath), "speedtest.json")
}
func (s *SpeedService) wait() { s.stop(); s.wg.Wait() }
func (s *SpeedService) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.state.Stopping = true
		s.cancel()
	}
}
func (a *App) speedStatus() SpeedState {
	if a.speed == nil {
		return SpeedState{}
	}
	a.speed.mu.Lock()
	defer a.speed.mu.Unlock()
	state := a.speed.state
	if state.Running && state.Current.Status == "running" {
		state.Current.Seconds = float64(time.Now().UnixMilli()-state.Current.Time) / 1000
		if state.Current.Seconds > 0 {
			state.Current.Mbps = float64(state.Current.Bytes) * 8 / state.Current.Seconds / 1e6
		}
	}
	return state
}

func speedLua(ctx context.Context, args ...string) ([]byte, error) {
	script, err := assets.ReadFile("bundle/speed.lua")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "lua", append([]string{"-"}, args...)...)
	cmd.Stdin = bytes.NewReader(script)
	// Generator errors may contain credentials, so only return a fixed message.
	b, err := cmd.Output()
	if err != nil {
		return nil, errors.New("读取 PassWall 节点或生成临时配置失败")
	}
	return b, nil
}
func (s *SpeedService) nodes(ctx context.Context) ([]SpeedNode, error) {
	if s.app.demo {
		return []SpeedNode{{ID: "example", Name: "示例节点", Core: "Xray", Protocol: "vless", Transport: "xhttp", Reason: "演示环境"}}, nil
	}
	b, err := speedLua(ctx, "list")
	if err != nil {
		return nil, err
	}
	nodes := []SpeedNode{}
	if err = json.Unmarshal(b, &nodes); err != nil {
		return nil, errors.New("节点列表格式无法识别")
	}
	return nodes, nil
}

func (s *SpeedService) start(req SpeedRequest, available []SpeedNode) error {
	if s.app.demo || runtime.GOOS != "linux" {
		return errors.New("测速需在 Linux 软路由上运行")
	}
	if req.Direction != "both" && req.Direction != "download" && req.Direction != "upload" {
		return errors.New("测速方向无效")
	}
	if len(req.Nodes) == 0 || len(req.Nodes) > 100 {
		return errors.New("请选择 1–100 个节点")
	}
	byID := map[string]SpeedNode{}
	for _, n := range available {
		byID[n.ID] = n
	}
	selected := []SpeedNode{}
	seen := map[string]bool{}
	for _, id := range req.Nodes {
		n, ok := byID[id]
		if !ok || !n.Supported {
			return errors.New("所选节点已删除或暂不支持，请刷新列表")
		}
		if !seen[id] {
			selected = append(selected, n)
			seen[id] = true
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Running {
		return errors.New("已有测速任务，请等待完成或停止")
	}
	cfg := s.settings
	if req.Target != "" {
		cfg.Target = req.Target
	}
	_, ok := speedTarget(cfg.Target)
	if !ok {
		return errors.New("测速目标无效")
	}
	if req.UploadTarget != "" {
		cfg.UploadTarget = req.UploadTarget
	}
	if target, ok := speedTarget(cfg.UploadTarget); !ok || !target.Upload {
		return errors.New("请选择支持上传的目标")
	}
	if (cfg.Target == "custom" && req.Direction != "upload" && cfg.DownloadURL == "") || (cfg.UploadTarget == "custom" && req.Direction != "download" && cfg.UploadURL == "") {
		return errors.New("请先填写对应方向的自定义 URL")
	}
	directions := []string{req.Direction}
	if req.Direction == "both" {
		directions = []string{"download", "upload"}
	}
	ctx, cancel := context.WithCancel(s.app.ctx)
	s.cancel = cancel
	s.state = SpeedState{Job: randomToken()[:16], Running: true, Started: time.Now().UnixMilli(), Total: len(selected) * len(directions), Current: SpeedResult{NodeID: selected[0].ID, NodeName: selected[0].Name, Status: "starting"}}
	s.wg.Add(1)
	go func() { defer s.wg.Done(); defer cancel(); s.run(ctx, selected, directions, cfg) }()
	return nil
}

func (s *SpeedService) run(ctx context.Context, nodes []SpeedNode, directions []string, cfg SpeedSettings) {
	defer func() {
		s.mu.Lock()
		s.state.Running = false
		s.state.Stopping = false
		s.state.Finished = time.Now().UnixMilli()
		s.cancel = nil
		s.mu.Unlock()
	}()
	for _, node := range nodes {
		if ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		s.state.Current = SpeedResult{NodeID: node.ID, NodeName: node.Name, Status: "starting"}
		s.mu.Unlock()
		client, closeProxy, err := temporarySpeedProxy(ctx, node)
		for _, direction := range directions {
			if ctx.Err() != nil {
				break
			}
			s.mu.Lock()
			job := s.state.Job
			s.mu.Unlock()
			sampleCfg := cfg
			if direction == "upload" {
				sampleCfg.Target = cfg.UploadTarget
			}
			target, _ := speedTarget(sampleCfg.Target)
			r := SpeedResult{Job: job, Time: time.Now().UnixMilli(), NodeID: node.ID, NodeName: node.Name, Direction: direction, Target: target.Name, Status: "running"}
			if sampleCfg.Target == "custom" {
				raw := cfg.DownloadURL
				if direction == "upload" {
					raw = cfg.UploadURL
				}
				u, _ := url.Parse(raw)
				r.Target = u.Host
			}
			update := func(v SpeedResult) { s.mu.Lock(); s.state.Current = v; s.mu.Unlock() }
			update(r)
			if err != nil {
				r.Status = "error"
				r.Message = err.Error()
			} else {
				r = measureSpeed(ctx, client, sampleCfg, r, update)
			}
			if e := s.app.db.saveSpeed(&r); e != nil {
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
		if closeProxy != nil {
			closeProxy()
		}
	}
}

func temporarySpeedProxy(ctx context.Context, node SpeedNode) (*http.Client, func(), error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, nil, errors.New("无法分配临时代理端口")
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	password := randomToken()
	b, err := speedLua(ctx, "config", node.ID, strconv.Itoa(port), password)
	if err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp("", "cg-speed-")
	if err != nil {
		return nil, nil, errors.New("无法创建临时测速目录")
	}
	remove := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "config.json")
	if err = os.WriteFile(path, b, 0600); err != nil {
		remove()
		return nil, nil, errors.New("无法写入临时测速配置")
	}
	bin := "/usr/bin/sing-box"
	if node.Core == "Xray" {
		bin = "/usr/bin/xray"
	}
	procCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(procCtx, bin, "run", "-c", path)
	speedProcess(cmd)
	if err = cmd.Start(); err != nil {
		cancel()
		remove()
		return nil, nil, errors.New("临时代理核心无法启动")
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	cleanup := func() { cancel(); <-done; remove() }
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	dialer, err := proxy.SOCKS5("tcp", addr, &proxy.Auth{User: "speed", Password: password}, &net.Dialer{Timeout: 8 * time.Second})
	if err != nil {
		cleanup()
		return nil, nil, errors.New("无法创建 SOCKS 测速连接")
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			cleanup()
			return nil, nil, errors.New("测速已停止")
		case <-done:
			cleanup()
			return nil, nil, errors.New("临时代理配置不被当前核心接受")
		case <-deadline.C:
			cleanup()
			return nil, nil, errors.New("临时代理监听超时")
		case <-tick.C:
			c, e := net.DialTimeout("tcp", addr, 100*time.Millisecond)
			if e != nil {
				continue
			}
			c.Close()
			transport := &http.Transport{DialContext: dialer.(proxy.ContextDialer).DialContext, DisableCompression: true, ForceAttemptHTTP2: true, TLSHandshakeTimeout: 8 * time.Second, ResponseHeaderTimeout: 15 * time.Second, MaxIdleConnsPerHost: 2}
			client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			return client, func() { transport.CloseIdleConnections(); cleanup() }, nil
		}
	}
}

type speedCounter struct {
	n      int64
	update func(int64)
}

func (c *speedCounter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	c.update(c.n)
	return len(p), nil
}

func measureSpeed(parent context.Context, client *http.Client, cfg SpeedSettings, r SpeedResult, update func(SpeedResult)) SpeedResult {
	ctx, cancel := context.WithTimeout(parent, time.Duration(cfg.Seconds)*time.Second)
	defer cancel()
	start := time.Now()
	last := time.Time{}
	progress := func(n int64) {
		if time.Since(last) < 200*time.Millisecond {
			return
		}
		last = time.Now()
		v := r
		v.Bytes = n
		v.Seconds = time.Since(start).Seconds()
		v.Mbps = float64(n) * 8 / v.Seconds / 1e6
		update(v)
	}
	var err error
	if r.Direction == "download" {
		err = downloadSpeed(ctx, client, cfg, &r, progress)
	} else {
		err = uploadSpeed(ctx, client, cfg, &r, progress)
	}
	r.Seconds = time.Since(start).Seconds()
	if r.Bytes > 0 {
		r.Mbps = float64(r.Bytes) * 8 / r.Seconds / 1e6
	}
	switch {
	case parent.Err() != nil:
		r.Status = "cancelled"
		r.Message = "已停止"
	case err == nil:
		r.Status = "complete"
	case errors.Is(ctx.Err(), context.DeadlineExceeded) && r.Bytes > 0:
		r.Status = "limited"
		r.Message = "达到时限，按已完成传输计算"
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		r.Status = "error"
		r.Message = "达到时限，未完成有效传输"
	default:
		r.Status = "error"
		r.Message = safeSpeedError(err)
	}
	if r.Status == "error" || r.Status == "cancelled" {
		r.Mbps = 0
	}
	return r
}
func safeSpeedError(err error) string {
	if err == nil {
		return ""
	}
	var u *url.Error
	if errors.As(err, &u) {
		return "代理连接、TLS 握手或目标请求失败"
	}
	return err.Error()
}

func downloadSpeed(ctx context.Context, client *http.Client, cfg SpeedSettings, r *SpeedResult, progress func(int64)) error {
	limit := int64(cfg.DownloadMiB) * 1024 * 1024
	for r.Bytes < limit {
		if err := ctx.Err(); err != nil {
			return err
		}
		sampleSize := limit - r.Bytes
		raw := cfg.DownloadURL
		if cfg.Target == "cloudflare" {
			sampleSize = min(sampleSize, 8*1024*1024)
			raw = "https://speed.cloudflare.com/__down?bytes=" + strconv.FormatInt(sampleSize, 10) + "&nonce=" + randomToken()[:12]
		} else if cfg.Target != "custom" {
			target, ok := speedTarget(cfg.Target)
			if !ok || target.DownloadURL == "" {
				return errors.New("下载目标无效")
			}
			raw = target.DownloadURL
		}
		req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
		if err != nil {
			return errors.New("下载地址无效")
		}
		req.Header.Set("Accept-Encoding", "identity")
		req.Header.Set("Cache-Control", "no-cache")
		if cfg.Target != "cloudflare" {
			req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", limit-1))
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		r.HTTPStatus = resp.StatusCode
		r.Edge = resp.Header.Get("CF-Ray")
		if resp.StatusCode != 200 && resp.StatusCode != 206 {
			resp.Body.Close()
			return fmt.Errorf("下载目标返回 HTTP %d", resp.StatusCode)
		}
		if strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
			resp.Body.Close()
			return errors.New("下载目标返回 HTML 页面，请使用文件地址")
		}
		base := r.Bytes
		c := &speedCounter{update: func(n int64) { progress(base + n) }}
		n, err := io.CopyBuffer(c, io.LimitReader(resp.Body, sampleSize), make([]byte, 64*1024))
		resp.Body.Close()
		r.Bytes += n
		if err != nil {
			return err
		}
		if err == nil && n == 0 {
			return errors.New("下载内容为空")
		}
		if cfg.Target == "cloudflare" && n != sampleSize {
			return errors.New("下载响应提前结束")
		}
		if cfg.Target != "cloudflare" {
			return nil
		}
	}
	return nil
}

// Public targets use acknowledged, bounded POST samples.
func uploadSpeed(ctx context.Context, client *http.Client, cfg SpeedSettings, r *SpeedResult, progress func(int64)) error {
	limit := int64(cfg.UploadMiB) * 1024 * 1024
	size := int64(256 * 1024)
	if cfg.Target == "custom" {
		size = limit
	}
	for r.Bytes < limit {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := min(size, limit-r.Bytes)
		raw, method := cfg.UploadURL, cfg.UploadMethod
		if cfg.Target == "cloudflare" {
			raw = "https://speed.cloudflare.com/__up"
			method = "POST"
		} else if target, ok := speedTarget(cfg.Target); ok && target.UploadURL != "" {
			raw, method = target.UploadURL, "POST"
		}
		payload := &speedPayload{}
		body := io.LimitReader(payload, n)
		req, err := http.NewRequestWithContext(ctx, method, raw, body)
		if err != nil {
			return errors.New("上传地址无效")
		}
		req.ContentLength = n
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		r.HTTPStatus = resp.StatusCode
		r.Edge = resp.Header.Get("CF-Ray")
		_, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("上传目标返回 HTTP %d", resp.StatusCode)
		}
		if readErr != nil {
			return readErr
		}
		if payload.sent.Load() != n {
			return errors.New("上传目标提前响应，未传完请求体")
		}
		r.Bytes += n
		progress(r.Bytes)
		if cfg.Target == "custom" {
			break
		}
		size = min(size*2, 4*1024*1024)
	}
	return nil
}

type speedPayload struct{ sent atomic.Int64 }

func (s *speedPayload) Read(p []byte) (int, error) {
	clear(p)
	s.sent.Add(int64(len(p)))
	return len(p), nil
}
