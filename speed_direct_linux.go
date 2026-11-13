package main

import (
	"net"
	"syscall"
	"time"
)

func directSpeedDialer() *net.Dialer {
	return &net.Dialer{Timeout: 8 * time.Second, Control: func(_, _ string, c syscall.RawConn) error {
		var sockErr error
		err := c.Control(func(fd uintptr) { sockErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, 255) })
		if err != nil {
			return err
		}
		return sockErr
	}}
}
