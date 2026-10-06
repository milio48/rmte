package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const daemonEnvVar = "_RMTE_DAEMON_CHILD"

func isDaemonChild() bool {
	return os.Getenv(daemonEnvVar) == "1"
}

// markDaemonReady is called by the background child process once its startup banner
// is fully printed and it is listening. It signals the parent process to display the PID
// and exit.
func markDaemonReady(sessionID string) {
	if !isDaemonChild() {
		return
	}

	if sessionID != "" {
		pidFile := fmt.Sprintf("rmte-%s.pid", sessionID)
		if hostWorkDir != "" {
			pidFile = filepath.Join(hostWorkDir, pidFile)
		}
		_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0644)
	}

	fmt.Println("__RMTE_READY__")
	_ = os.Stdout.Sync()

	// Redirect stdout and stderr to devnull
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err == nil {
		os.Stdout = devNull
		os.Stderr = devNull
	}
}

// runAsDaemon launches the current rmte command as a detached background process,
// streams its initial startup output to the user's terminal, displays the background PID,
// and exits immediately.
func runAsDaemon() {
	exe, err := os.Executable()
	if err != nil {
		fatalf("Error determining executable path: %v", err)
	}

	// Create a temporary file to capture startup banner output without handle coupling
	tmpFile, err := os.CreateTemp("", "rmte-startup-*.log")
	if err != nil {
		fatalf("Error creating temporary log file: %v", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	// Prepare child process args (pass all original arguments)
	cmd := exec.Command(exe, origArgs[1:]...)
	cmd.Env = append(os.Environ(), daemonEnvVar+"=1")
	cmd.Stdout = tmpFile
	cmd.Stderr = tmpFile
	setDetachedSysProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		fatalf("Error starting background process: %v", err)
	}
	// Close parent's write handle to tmpFile so we only read
	_ = tmpFile.Close()

	// Open read handle for parent
	r, err := os.Open(tmpPath)
	if err != nil {
		fatalf("Error opening startup log: %v", err)
	}
	defer r.Close()

	reader := bufio.NewReader(r)
	ready := false
	startTime := time.Now()

	for time.Since(startTime) < 15*time.Second {
		line, err := reader.ReadString('\n')
		if err == nil {
			trimmed := strings.TrimRight(line, "\r\n")
			if trimmed == "__RMTE_READY__" {
				ready = true
				break
			}
			fmt.Println(trimmed)
			continue
		}
		if err == io.EOF {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		break
	}

	if !ready {
		fatalf("Error: background process failed to start within timeout")
	}

	pid := cmd.Process.Pid
	fmt.Printf("\nBackground Process:\n")
	fmt.Printf("  PID:             %d\n", pid)
	fmt.Printf("  Status:          ✅ Running in background\n")
	fmt.Printf("  Stop command:    rmte stop %d\n\n", pid)

	os.Exit(0)
}

func cmdStop() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: rmte stop <session_id | pid>")
		os.Exit(1)
	}

	target := strings.TrimSpace(os.Args[1])
	if target == "" {
		fmt.Println("Usage: rmte stop <session_id | pid>")
		os.Exit(1)
	}

	// Case 1: Target is a numeric PID
	if pid, err := strconv.Atoi(target); err == nil && pid > 0 {
		if err := killPid(pid); err != nil {
			fatalf("Error stopping process %d: %v", pid, err)
		}
		fmt.Printf("✅ RMTE background process (PID %d) stopped.\n", pid)
		return
	}

	// Case 2: Target is a session ID (look for rmte-<session_id>.pid)
	pidFilename := target
	if !strings.HasSuffix(pidFilename, ".pid") {
		pidFilename = fmt.Sprintf("rmte-%s.pid", target)
	}

	data, err := os.ReadFile(pidFilename)
	if err != nil {
		// Also try without rmte- prefix if user typed whole filename
		data, err = os.ReadFile(target)
		if err != nil {
			fatalf("Error: could not find PID file %q for session %q: %v", pidFilename, target, err)
		}
	}

	pidStr := strings.TrimSpace(string(data))
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		fatalf("Error: invalid PID %q in file %s", pidStr, pidFilename)
	}

	if err := killPid(pid); err != nil {
		fatalf("Error stopping process %d: %v", pid, err)
	}

	_ = os.Remove(pidFilename)
	sessionID := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(pidFilename), "rmte-"), ".pid")
	removeSessionMeta(sessionID)
	fmt.Printf("✅ RMTE session %q (PID %d) stopped.\n", target, pid)
}
