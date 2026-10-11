//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	if err != nil {
		return false
	}
	// Check if process has become a zombie on Linux
	if statusBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid)); err == nil {
		content := string(statusBytes)
		if strings.Contains(content, "State:\tZ") || strings.Contains(content, "State:\tX") {
			return false
		}
	}
	return true
}

func isPidRMTE(pid int, exePath string) bool {
	if pid <= 0 || !isPidAlive(pid) {
		return false
	}
	cleanExe, _ := filepath.EvalSymlinks(exePath)
	if cleanExe == "" {
		cleanExe = exePath
	}

	// 1. Check /proc/<pid>/exe against current binary path
	if exeLink, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err == nil {
		targetExe := strings.TrimSuffix(exeLink, " (deleted)")
		cleanTarget, _ := filepath.EvalSymlinks(targetExe)
		if cleanTarget == "" {
			cleanTarget = targetExe
		}
		if cleanTarget == cleanExe || filepath.Base(cleanTarget) == filepath.Base(cleanExe) {
			return true
		}
	}

	// 2. Fallback to /proc/<pid>/comm matching base of exePath or prefix "rmte"
	if commBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid)); err == nil {
		comm := strings.TrimSpace(string(commBytes))
		expectedComm := filepath.Base(cleanExe)
		if len(expectedComm) > 15 {
			expectedComm = expectedComm[:15]
		}
		if comm == expectedComm || strings.HasPrefix(comm, "rmte") {
			return true
		}
	}

	// On non-Linux Unix without /proc (e.g. Darwin), fail-open if alive
	if _, err := os.Stat("/proc"); os.IsNotExist(err) {
		return true
	}

	return false
}
