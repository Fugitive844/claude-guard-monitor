package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// The loopback relay marks all outbound sockets for PassWall bypass, including
// iperf3's TCP control connection and UDP data. No firewall rules are changed.
func iperfRelay(ctx context.Context, target string, udp bool) (int, func(), error) {
	ctx, cancel := context.WithCancel(ctx)
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		cancel()
		return 0, nil, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	var packet *net.UDPConn
	if udp {
		packet, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
		if err != nil {
			l.Close()
			cancel()
			return 0, nil, err
		}
	}
	var wg sync.WaitGroup
	stop := context.AfterFunc(ctx, func() {
		l.Close()
		if packet != nil {
			packet.Close()
		}
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			local, e := l.Accept()
			if e != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer local.Close()
				closeLocal := context.AfterFunc(ctx, func() { local.Close() })
				defer closeLocal()
				remote, e := directSpeedDialer().DialContext(ctx, "tcp", target)
				if e != nil {
					return
				}
				defer remote.Close()
				closeRemote := context.AfterFunc(ctx, func() { remote.Close() })
				defer closeRemote()
				done := make(chan struct{})
				go func() {
					io.Copy(remote, local)
					if c, ok := remote.(*net.TCPConn); ok {
						c.CloseWrite()
					}
					close(done)
				}()
				io.Copy(local, remote)
				local.Close()
				remote.Close()
				<-done
			}()
		}
	}()
	if packet != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buffer := make([]byte, 65536)
			n, peer, e := packet.ReadFromUDP(buffer)
			if e != nil {
				return
			}
			remote, e := directSpeedDialer().DialContext(ctx, "udp", target)
			if e != nil {
				return
			}
			defer remote.Close()
			closeRemote := context.AfterFunc(ctx, func() { remote.Close() })
			defer closeRemote()
			if _, e = remote.Write(buffer[:n]); e != nil {
				return
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				reply := make([]byte, 65536)
				for {
					n, e := remote.Read(reply)
					if e != nil {
						return
					}
					if _, e = packet.WriteToUDP(reply[:n], peer); e != nil {
						return
					}
				}
			}()
			for {
				n, addr, e := packet.ReadFromUDP(buffer)
				if e != nil {
					break
				}
				if addr.String() != peer.String() {
					continue
				}
				if _, e = remote.Write(buffer[:n]); e != nil {
					break
				}
			}
			remote.Close()
			<-done
		}()
	}
	cleanup := func() {
		cancel()
		stop()
		l.Close()
		if packet != nil {
			packet.Close()
		}
		wg.Wait()
	}
	return port, cleanup, nil
}

func startIPerfServer(ctx context.Context, client *ssh.Client, port, seconds int) (func(), error) {
	s, err := client.NewSession()
	if err != nil {
		return nil, errors.New("无法创建远端 iperf3 会话")
	}
	input, err := s.StdinPipe()
	if err != nil {
		s.Close()
		return nil, err
	}
	output, err := s.StdoutPipe()
	if err != nil {
		s.Close()
		return nil, err
	}
	// The input pipe controls the server lifetime; a watchdog also bounds it if
	// the SSH transport disappears before cleanup can reach the remote process.
	command := fmt.Sprintf(`sh -c 'LC_ALL=C iperf3 -s -1 -p %d --forceflush & pid=$!
(sleep %d; kill "$pid" 2>/dev/null) </dev/null >/dev/null 2>&1 & timer=$!
trap '\''kill "$pid" "$timer" 2>/dev/null; wait 2>/dev/null'\'' EXIT HUP INT TERM
read -r stop
'`, port, seconds+20)
	if err = s.Start(command); err != nil {
		s.Close()
		return nil, errors.New("远端 iperf3 无法启动，请先安装")
	}
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(output)
		found := false
		for scanner.Scan() {
			if !found && strings.Contains(scanner.Text(), "Server listening on") {
				found = true
				ready <- true
			}
		}
		if !found {
			ready <- false
		}
	}()
	done := make(chan struct{})
	go func() { _ = s.Wait(); close(done) }()
	cleanup := func() {
		input.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			s.Close()
			client.Close()
			<-done
		}
		s.Close()
	}
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	select {
	case ok := <-ready:
		if ok {
			return cleanup, nil
		}
	case <-ctx.Done():
	case <-timer.C:
	}
	cleanup()
	return nil, errors.New("远端 iperf3 未就绪，请检查安装、--forceflush 支持、端口占用和监听权限（低于 1024 的端口可能需要 root）")
}

func iperfArgs(port int, req SSHSpeedRequest, r SpeedResult) []string {
	args := []string{"-c", "127.0.0.1", "-p", strconv.Itoa(port), "-t", strconv.Itoa(req.Seconds), "-i", "1", "--forceflush", "--connect-timeout", "5000", "--get-server-output"}
	if req.Omit > 0 {
		args = append(args, "-O", strconv.Itoa(req.Omit))
	}
	if r.Direction == "download" {
		args = append(args, "-R")
	}
	if r.Protocol == "udp" {
		length := req.UDPLength
		if length == 0 {
			length = 1200
		}
		args = append(args, "-u", "-b", strconv.Itoa(req.Bandwidth)+"M", "-l", strconv.Itoa(length))
	} else {
		parallel := req.Parallel
		if parallel == 0 {
			parallel = 1
		}
		args = append(args, "-P", strconv.Itoa(parallel))
		if req.TCPBandwidth > 0 {
			args = append(args, "-b", strconv.FormatFloat(float64(req.TCPBandwidth)/float64(parallel), 'f', 3, 64)+"M")
		}
	}
	return args
}

func measureIPerf(ctx context.Context, client *ssh.Client, host SSHHost, req SSHSpeedRequest, r SpeedResult, update func(SpeedResult)) (SpeedResult, error) {
	if _, err := exec.LookPath("iperf3"); err != nil {
		return r, errors.New("软路由未安装 iperf3，请先点击安装 iperf3")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(req.Seconds+req.Omit+20)*time.Second)
	defer cancel()
	stopClient := context.AfterFunc(ctx, func() { client.Close() })
	defer stopClient()
	stopServer, err := startIPerfServer(ctx, client, req.Port, req.Seconds+req.Omit)
	if err != nil {
		return r, err
	}
	defer stopServer()
	port, cleanup, err := iperfRelay(ctx, net.JoinHostPort(host.Host, strconv.Itoa(req.Port)), r.Protocol == "udp")
	if err != nil {
		return r, errors.New("无法创建本地 iperf3 直连通道")
	}
	defer cleanup()
	r.Time = time.Now().UnixMilli()
	update(r)
	args := iperfArgs(port, req, r)
	help, _ := exec.CommandContext(ctx, "iperf3", "--help").Output()
	if strings.Contains(string(help), "--json-stream") {
		args = append(args, "--json-stream")
	} else {
		args = append(args, "-f", "m")
	}
	cmd := exec.CommandContext(ctx, "iperf3", args...)
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")
	speedProcess(cmd)
	out := newIPerfProgress(r, update)
	cmd.Stdout = out
	var diagnostic boundedSpeedOutput
	cmd.Stderr = &diagnostic
	err = cmd.Run()
	if err != nil {
		detail := out.errorMessage
		if detail == "" {
			detail = firstLine(diagnostic.Bytes())
		}
		if detail == "" {
			detail = err.Error()
		}
		partial, _ := out.result()
		return partial, fmt.Errorf("iperf3 测速失败：%s", detail)
	}
	return out.result()
}

type iperfSummary struct {
	Bytes       int64   `json:"bytes"`
	Seconds     float64 `json:"seconds"`
	Mbps        float64 `json:"bits_per_second"`
	Jitter      float64 `json:"jitter_ms"`
	Lost        int64   `json:"lost_packets"`
	Packets     int64   `json:"packets"`
	LostPercent float64 `json:"lost_percent"`
	Retransmits *int64  `json:"retransmits"`
}

func parseIPerf(b []byte, r SpeedResult) (SpeedResult, error) {
	var result struct {
		Error string `json:"error"`
		End   struct {
			Received *iperfSummary `json:"sum_received"`
			Sent     *iperfSummary `json:"sum_sent"`
			Sum      *iperfSummary `json:"sum"`
			Streams  []struct {
				Sender struct {
					MeanRTT *float64 `json:"mean_rtt"`
					MinRTT  *float64 `json:"min_rtt"`
					MaxRTT  *float64 `json:"max_rtt"`
				} `json:"sender"`
			} `json:"streams"`
		} `json:"end"`
	}
	if json.Unmarshal(b, &result) != nil || result.Error != "" {
		return r, errors.New("iperf3 未返回有效结果，请检查连接和两端版本")
	}
	summary := result.End.Received
	if summary == nil && r.Protocol == "udp" {
		summary = result.End.Sum
	}
	if summary == nil || summary.Seconds <= 0 || summary.Bytes < 0 || summary.Mbps < 0 {
		return r, errors.New("iperf3 缺少接收端统计")
	}
	r.Bytes = summary.Bytes
	r.Seconds = summary.Seconds
	r.Mbps = summary.Mbps / 1e6
	r.Status = "complete"
	r.RateBasis = "receiver"
	if result.End.Sent != nil {
		rate := result.End.Sent.Mbps / 1e6
		r.SenderMbps = &rate
		if r.Protocol == "tcp" && (r.SourceHostID != "" || r.Direction == "download") {
			r.Retransmits = result.End.Sent.Retransmits
		}
	}
	// Client-side RTT on the router's loopback relay is not a path measurement.
	if r.Protocol == "tcp" && r.SourceHostID != "" {
		var total, low, high float64
		n := 0
		for _, stream := range result.End.Streams {
			s := stream.Sender
			if s.MeanRTT != nil && s.MinRTT != nil && s.MaxRTT != nil {
				if n == 0 || *s.MinRTT < low {
					low = *s.MinRTT
				}
				if *s.MaxRTT > high {
					high = *s.MaxRTT
				}
				total += *s.MeanRTT
				n++
			}
		}
		if n > 0 {
			mean := total / float64(n) / 1000
			low /= 1000
			high /= 1000
			r.RTTMeanMS = &mean
			r.RTTMinMS = &low
			r.RTTMaxMS = &high
		}
	}
	if r.Protocol == "udp" {
		r.JitterMS = summary.Jitter
		r.LostPackets = summary.Lost
		r.Packets = summary.Packets
		r.LostPercent = summary.LostPercent
	}
	return r, nil
}
