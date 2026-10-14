//go:build !windows

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"

	"github.com/creack/pty"
	"golang.org/x/term"
)

func disablePTYEcho(f *os.File) {
	if f != nil {
		_, _ = term.MakeRaw(int(f.Fd()))
	}
}

func startPlatformTab(id byte, ws *SafeConn) (*TabSession, error) {
	return startPlatformTabWithTitle(id, ws, fmt.Sprintf("Terminal %d", id), false)
}

func startPlatformTabWithTitle(id byte, ws *SafeConn, title string, isAI bool) (*TabSession, error) {
	shell := "bash"
	if os.Getenv("SHELL") != "" {
		shell = os.Getenv("SHELL")
	}

	c := exec.Command(shell)
	if hostWorkDir != "" {
		c.Dir = hostWorkDir
	}

	f, err := pty.Start(c)
	if err != nil {
		log.Printf("Failed to start PTY for tab %d: %v", id, err)
		return nil, fmt.Errorf("pty.Start failed: %w", err)
	}

	if isAI {
		disablePTYEcho(f)
	}

	tab := &TabSession{
		Title:       title,
		IsAI:        isAI,
		Cmd:         c,
		ReadCloser:  f,
		WriteCloser: f,
		IsPipe:      false,
		Resizer: func(cols, rows int) error {
			return pty.Setsize(f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
		},
		CloseFn: func() error {
			f.Close()
			if c.Process != nil {
				c.Process.Kill()
			}
			return nil
		},
	}
	return tab, nil
}
