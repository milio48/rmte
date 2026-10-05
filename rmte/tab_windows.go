//go:build windows

package main

import (
	"log"
	"os"
	"os/exec"
	"sync"

	"github.com/UserExistsError/conpty"
)

func startPlatformTab(id byte, ws *SafeConn) (*TabSession, error) {
	shell := "cmd.exe"
	if os.Getenv("COMSPEC") != "" {
		shell = os.Getenv("COMSPEC")
	}

	// Try Windows native ConPTY (Windows 10 build 17763+ / Windows 11)
	if conpty.IsConPtyAvailable() {
		opts := []conpty.ConPtyOption{
			conpty.ConPtyDimensions(80, 24),
		}
		if hostWorkDir != "" {
			opts = append(opts, conpty.ConPtyWorkDir(hostWorkDir))
		}
		cpty, err := conpty.Start(shell, opts...)
		if err == nil {
			log.Printf("[Host] ConPTY pseudo-terminal initialized for Tab %d (PID %d)", id, cpty.Pid())
			var cptyOnce sync.Once
			tab := &TabSession{
				ReadCloser:  cpty,
				WriteCloser: cpty,
				IsPipe:      false,
				Resizer: func(cols, rows int) error {
					return cpty.Resize(cols, rows)
				},
				CloseFn: func() error {
					var closeErr error
					cptyOnce.Do(func() {
						closeErr = cpty.Close()
					})
					return closeErr
				},
			}
			return tab, nil
		}
		log.Printf("[Host] ConPTY failed to start: %v, falling back to Pipes", err)
	}

	// Fallback to anonymous pipes for older Windows without ConPTY
	c := exec.Command(shell, "/q")
	if hostWorkDir != "" {
		c.Dir = hostWorkDir
	}
	log.Printf("PTY not supported on Windows, falling back to Pipes for Tab %d", id)
	runWithPipes(id, c, ws)
	return nil, nil
}
