//go:build !windows

package main

import (
	"fmt"
	"os/exec"
	"syscall"
)

func setDetachedSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}
}

func killPid(pid int) error {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		if err == syscall.ESRCH {
			return nil
		}
		return fmt.Errorf("failed to kill PID %d: %w", pid, err)
	}
	return nil
}

func isPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil
}
