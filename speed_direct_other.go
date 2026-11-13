//go:build !linux

package main

import (
	"net"
	"time"
)

func directSpeedDialer() *net.Dialer { return &net.Dialer{Timeout: 8 * time.Second} }
