//go:build !windows

package main

import (
	"os"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/term"
)

type unixResizeWatcher struct {
	sigChan chan os.Signal
	done    chan struct{}
	once    sync.Once
}

// startResizeWatcher begins listening for SIGWINCH on Unix/Linux/macOS/Termux.
func startResizeWatcher(vc *ViewerConn, tabID byte, fd int) ResizeWatcher {
	w := &unixResizeWatcher{
		sigChan: make(chan os.Signal, 1),
		done:    make(chan struct{}),
	}

	signal.Notify(w.sigChan, syscall.SIGWINCH)

	go func() {
		lastW, lastH, _ := term.GetSize(fd)

		for {
			select {
			case <-w.done:
				return
			case <-w.sigChan:
				curW, curH, err := term.GetSize(fd)
				if err == nil && curW > 0 && curH > 0 && (curW != lastW || curH != lastH) {
					lastW, lastH = curW, curH
					_ = vc.WriteJSON(map[string]interface{}{
						"type":   "control",
						"action": "resize",
						"tab_id": tabID,
						"cols":   curW,
						"rows":   curH,
					})
				}
			}
		}
	}()

	return w
}

func (w *unixResizeWatcher) Stop() {
	w.once.Do(func() {
		signal.Stop(w.sigChan)
		close(w.done)
	})
}
