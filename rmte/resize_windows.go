//go:build windows

package main

import (
	"sync"
	"time"

	"golang.org/x/term"
)

type windowsResizeWatcher struct {
	ticker *time.Ticker
	done   chan struct{}
	once   sync.Once
}

// startResizeWatcher monitors console dimensions periodically on Windows.
func startResizeWatcher(vc *ViewerConn, tabID byte, fd int) ResizeWatcher {
	w := &windowsResizeWatcher{
		ticker: time.NewTicker(250 * time.Millisecond),
		done:   make(chan struct{}),
	}

	go func() {
		lastW, lastH, _ := term.GetSize(fd)

		for {
			select {
			case <-w.done:
				return
			case <-w.ticker.C:
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

func (w *windowsResizeWatcher) Stop() {
	w.once.Do(func() {
		w.ticker.Stop()
		close(w.done)
	})
}
