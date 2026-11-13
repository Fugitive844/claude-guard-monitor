package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

func peerIPerfCommand(host string, req SSHSpeedRequest, r SpeedResult) string {
	args := iperfArgs(req.Port, req, r)
	args[1] = host
	for i := range args {
		args[i] = shellQuote(args[i])
	}
	// Bound the client even if the controlling SSH connection disappears.
	script := fmt.Sprintf(`output='-f m'
if iperf3 --help 2>&1 | grep -q -- '--json-stream'; then output='--json-stream'; fi
LC_ALL=C iperf3 %s $output & pid=$!
(sleep %d; kill "$pid" 2>/dev/null) </dev/null >/dev/null 2>&1 & timer=$!
trap 'kill "$pid" "$timer" 2>/dev/null; wait 2>/dev/null' EXIT HUP INT TERM
wait "$pid"
status=$?
exit "$status"`, strings.Join(args, " "), req.Seconds+req.Omit+15)
	return "sh -c " + shellQuote(script)
}

func measurePeerIPerf(ctx context.Context, source, target *ssh.Client, host SSHHost, req SSHSpeedRequest, r SpeedResult, update func(SpeedResult)) (SpeedResult, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(req.Seconds+req.Omit+20)*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { source.Close(); target.Close() })
	defer stop()
	if _, err := sshOutput(source, "command -v iperf3 >/dev/null 2>&1"); err != nil {
		return r, errors.New("源主机未安装 iperf3 或 SSH 连接已中断，请在该主机的更多操作中安装 iperf3")
	}
	cleanup, err := startIPerfServer(ctx, target, req.Port, req.Seconds+req.Omit)
	if err != nil {
		return r, err
	}
	defer cleanup()
	session, err := source.NewSession()
	if err != nil {
		return r, errors.New("无法创建源主机测速会话")
	}
	defer session.Close()
	r.Time = time.Now().UnixMilli()
	update(r)
	output := newIPerfProgress(r, update)
	session.Stdout = output
	err = session.Run(peerIPerfCommand(host.Host, req, r))
	if ctx.Err() != nil {
		partial, _ := output.result()
		return partial, ctx.Err()
	}
	if err != nil {
		partial, _ := output.result()
		return partial, errors.New("主机间 iperf3 测试失败，请检查目标地址、测速端口、防火墙与两端版本；UDP 还需同端口的 TCP 控制连接")
	}
	return output.result()
}
