package main

import (
	"io"
	"sync"
)

// ViewerConn provides a thread-safe handle to the active connection for the viewer CLI.
// All writes (terminal raw keystrokes, menu commands, chat, resize packets) go through
// ViewerConn so that when an auto-reconnect occurs, swapping the underlying connection
// seamlessly redirects all in-flight and future I/O to the new socket.
type ViewerConn struct {
	mu   sync.RWMutex
	conn *SafeConn
}

func NewViewerConn(c *SafeConn) *ViewerConn {
	return &ViewerConn{conn: c}
}

// WriteJSON sends a JSON payload to the active connection under RLock.
func (vc *ViewerConn) WriteJSON(v interface{}) error {
	vc.mu.RLock()
	defer vc.mu.RUnlock()
	if vc.conn == nil {
		return io.ErrClosedPipe
	}
	return vc.conn.WriteJSON(v)
}

// WriteMessage sends a raw websocket message to the active connection under RLock.
func (vc *ViewerConn) WriteMessage(messageType int, data []byte) error {
	vc.mu.RLock()
	defer vc.mu.RUnlock()
	if vc.conn == nil {
		return io.ErrClosedPipe
	}
	return vc.conn.WriteMessage(messageType, data)
}

// Swap closes the old connection and atomically replaces it with newConn.
func (vc *ViewerConn) Swap(newConn *SafeConn) {
	vc.mu.Lock()
	old := vc.conn
	vc.conn = newConn
	vc.mu.Unlock()

	if old != nil {
		_ = old.Close()
	}
}

// Close closes the current active connection.
func (vc *ViewerConn) Close() error {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	if vc.conn != nil {
		err := vc.conn.Close()
		vc.conn = nil
		return err
	}
	return nil
}

// Get returns the current connection handle for read loops.
// NOTE: Must only be used for ReadMessage; all writes must go through vc.WriteJSON / vc.WriteMessage.
func (vc *ViewerConn) Get() *SafeConn {
	vc.mu.RLock()
	defer vc.mu.RUnlock()
	return vc.conn
}
