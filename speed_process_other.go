//go:build !linux

package main

import "os/exec"

func speedProcess(cmd *exec.Cmd) {}
