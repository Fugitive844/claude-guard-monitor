//go:build linux

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"time"
)

func (a *App) collectSyslog() {
	for a.ctx.Err() == nil {
		err := a.followKernelLog()
		if a.ctx.Err() != nil {
			return
		}
		a.mu.Lock()
		a.collectorState = "error"
		a.lastError = fmt.Sprintf("内核日志采集中断: %v", err)
		a.lost++
		a.mu.Unlock()
		select {
		case <-a.ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}
func (a *App) followKernelLog() error {
	since := time.Now()
	cmd := exec.CommandContext(a.ctx, "/sbin/logread", "-f", "-l", "1", "-e", "CG_DROP ")
	cmd.Env = append(os.Environ(), "TZ=UTC0")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	defer cmd.Process.Kill()
	a.mu.Lock()
	a.collectorState = "syslog"
	a.lastError = ""
	a.mu.Unlock()
	a.audit("collector", "内核日志已连接；每地址族限速 5 包/秒，日志可能丢失，DROP 不限速")
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 4096), 65536)
	for scanner.Scan() {
		event, err := parseKernelLog(scanner.Text(), since)
		if err == nil {
			a.enqueue(event)
		}
	}
	scanErr := scanner.Err()
	if scanErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if scanErr != nil {
		return scanErr
	}
	return waitErr
}
