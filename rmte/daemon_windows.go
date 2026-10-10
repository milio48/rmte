//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"strconv"
	"syscall"
)

func setDetachedSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x08000000, // CREATE_NEW_PROCESS_GROUP + CREATE_NO_WINDOW
	}
}

func killPid(pid int) error {
	// First try taskkill /T /F to kill the process and its child tree
	cmd := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid))
	if err := cmd.Run(); err == nil {
		return nil
	}

	// Fallback to direct process termination
	h, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		if errno, ok := err.(syscall.Errno); ok && (errno == 87 || errno == 2) {
			// Process does not exist (already terminated)
			return nil
		}
		return fmt.Errorf("failed to open process %d: %w", pid, err)
	}
	defer syscall.CloseHandle(h)

	if err := syscall.TerminateProcess(h, 1); err != nil {
		return fmt.Errorf("failed to terminate process %d: %w", pid, err)
	}
	return nil
}

func isPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(0x1000, false, uint32(pid)) // PROCESS_QUERY_LIMITED_INFORMATION
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var exitCode uint32
	if err := syscall.GetExitCodeProcess(h, &exitCode); err != nil {
		return false
	}
	return exitCode == 259 // STILL_ACTIVE
}
