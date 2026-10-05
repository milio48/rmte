//go:build !windows

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

func startPlatformTab(id byte, ws *SafeConn) (*TabSession, error) {
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

	tab := &TabSession{
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
