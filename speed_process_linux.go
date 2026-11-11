package main

import (
	"os/exec"
	"syscall"
)

func speedProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
