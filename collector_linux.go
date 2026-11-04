//go:build linux

package main

import (
	"github.com/florianl/go-nflog/v2"
	"time"
)

func (a *App) collect() {
	if a.demo {
		a.mu.Lock()
		a.collectorState = "demo"
		a.mu.Unlock()
		return
	}
	if a.logBackend == "syslog" {
		a.collectSyslog()
		return
	}
	for {
		select {
		case <-a.ctx.Done():
			return
		default:
		}
		cfg := nflog.Config{Group: a.cfg.Group, Copymode: nflog.CopyPacket, Bufsize: 96}
		nf, e := nflog.Open(&cfg)
		if e != nil {
			a.mu.Lock()
			a.collectorState = "error"
			a.lastError = "NFLOG: " + e.Error()
			a.mu.Unlock()
			time.Sleep(5 * time.Second)
			continue
		}
		_ = nf.Con.SetReadBuffer(512 * 1024)
		e = nf.RegisterWithErrorFunc(a.ctx, func(v nflog.Attribute) int {
			if v.Payload == nil {
				return 0
			}
			event, err := parsePacket(*v.Payload)
			if err != nil {
				a.mu.Lock()
				a.lost++
				a.mu.Unlock()
				return 0
			}
			if v.Timestamp != nil {
				event.First = v.Timestamp.UnixMilli()
			}
			a.enqueue(event)
			return 0
		}, func(err error) int {
			a.mu.Lock()
			a.lost++
			a.lastError = "NFLOG 采集出现缺口: " + err.Error()
			a.mu.Unlock()
			return 0
		})
		if e != nil {
			_ = nf.Close()
			a.mu.Lock()
			a.collectorState = "error"
			a.lastError = e.Error()
			a.mu.Unlock()
			time.Sleep(5 * time.Second)
			continue
		}
		a.mu.Lock()
		a.collectorState = "running"
		a.mu.Unlock()
		a.audit("collector", "NFLOG 已连接；详细日志限速 30 包/秒/地址族，DROP 不限速")
		<-a.ctx.Done()
		_ = nf.Close()
		return
	}
}
