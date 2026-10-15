//go:build windows

package main

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

func executeAICommandWindows(tab *TabSession, command string, timeoutSec int) (string, int, error) {
	if tab == nil || tab.WriteCloser == nil {
		return "", 1, fmt.Errorf("AI tab canvas not available")
	}

	// 1. Write visual prompt header to canvas (Web UI viewer only, not rawBuf)
	header := fmt.Sprintf("\r\n\x1b[36m[AI Exec]\x1b[0m \x1b[1m%s\x1b[0m\r\n", command)
	_, _ = tab.WriteCloser.Write([]byte(header))

	// 2. Resolve shell executable: pwsh -> powershell -> cmd
	shell := "powershell.exe"
	var args []string
	if p, err := exec.LookPath("pwsh.exe"); err == nil && p != "" {
		shell = p
		psCmd := fmt.Sprintf("try { [Console]::OutputEncoding = [System.Text.Encoding]::UTF8 } catch {}; $OutputEncoding = [System.Text.Encoding]::UTF8; %s", command)
		args = []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", psCmd}
	} else if p, err := exec.LookPath("powershell.exe"); err == nil && p != "" {
		shell = p
		psCmd := fmt.Sprintf("try { [Console]::OutputEncoding = [System.Text.Encoding]::UTF8 } catch {}; $OutputEncoding = [System.Text.Encoding]::UTF8; %s", command)
		args = []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", psCmd}
	} else {
		shell = "cmd.exe"
		args = []string{"/c", "chcp 65001>nul & " + command}
	}

	c := exec.Command(shell, args...)
	if hostWorkDir != "" {
		c.Dir = hostWorkDir
	}

	pr, pw := io.Pipe()
	c.Stdout = pw
	c.Stderr = pw

	if err := c.Start(); err != nil {
		_ = pw.Close()
		_ = pr.Close()
		footer := fmt.Sprintf("\r\n\x1b[31m[Failed to start: %v]\x1b[0m\r\n", err)
		_, _ = tab.WriteCloser.Write([]byte(footer))
		return "", 1, fmt.Errorf("failed to start command: %w", err)
	}

	if timeoutSec <= 0 {
		timeoutSec = 60
	}

	timedOut := false
	var timeoutMu sync.Mutex
	timer := time.AfterFunc(time.Duration(timeoutSec)*time.Second, func() {
		timeoutMu.Lock()
		timedOut = true
		timeoutMu.Unlock()
		if c.Process != nil {
			killProcessTree(uint32(c.Process.Pid))
		}
	})
	defer timer.Stop()

	var rawBuf bytes.Buffer
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer pr.Close()
		buf := make([]byte, 4096)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				rawBuf.Write(chunk)
				_, _ = tab.WriteCloser.Write(chunk)
			}
			if err != nil {
				break
			}
		}
	}()

	waitErr := c.Wait()
	_ = pw.Close()
	<-readDone

	timeoutMu.Lock()
	wasTimedOut := timedOut
	timeoutMu.Unlock()

	cleanOutput := stripANSI(rawBuf.Bytes())
	if len(cleanOutput) > 1024*1024 {
		cleanOutput = cleanOutput[:1024*1024] + "\n[Output truncated by RMTE buffer limit]"
	}

	if wasTimedOut {
		footer := fmt.Sprintf("\r\n\x1b[31m[Command timed out after %ds (exit 124)]\x1b[0m\r\n", timeoutSec)
		_, _ = tab.WriteCloser.Write([]byte(footer))
		return cleanOutput + fmt.Sprintf("\n[RMTE Error: Command timed out after %ds (exit 124). For background jobs/servers, set background: true]", timeoutSec), 124, nil
	}

	exitCode := 0
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	footer := fmt.Sprintf("\r\n\x1b[90m[Process exited with code %d]\x1b[0m\r\n", exitCode)
	_, _ = tab.WriteCloser.Write([]byte(footer))

	return cleanOutput, exitCode, nil
}
