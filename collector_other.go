//go:build !linux

package main

func (a *App) collect() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.demo {
		a.collectorState = "demo"
	} else {
		a.collectorState = "unsupported"
		a.lastError = "NFLOG 采集只支持 Linux"
	}
}
