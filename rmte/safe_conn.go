package main

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// SafeConn serialises writes on a websocket connection.
//
// By default writes go straight to the socket. The relay instead calls
// startWriteQueue so that every write is a non-blocking enqueue drained by a
// dedicated goroutine with a write deadline: a peer that stops reading can
// then never stall the relay while it holds session locks. A peer that falls
// too far behind (queue full) or misses the deadline is disconnected.
type SafeConn struct {
	*websocket.Conn
	mu sync.Mutex
	// ctl, when set, seals every WriteJSON in an end-to-end control envelope.
	// It is attached only after the relay handshake, which stays plaintext.
	ctl *ctlChannel

	out       chan queuedFrame
	done      chan struct{}
	closeOnce sync.Once
}

type queuedFrame struct {
	messageType int
	data        []byte
}

var errSlowConsumer = errors.New("peer is not reading fast enough; connection closed")

// startWriteQueue switches c to queued writes. Call it before c is shared.
func (c *SafeConn) startWriteQueue(depth int, writeTimeout time.Duration) {
	c.out = make(chan queuedFrame, depth)
	c.done = make(chan struct{})
	go func() {
		write := func(f queuedFrame, deadline time.Time) error {
			c.mu.Lock()
			defer c.mu.Unlock()
			_ = c.Conn.SetWriteDeadline(deadline)
			return c.Conn.WriteMessage(f.messageType, f.data)
		}
		for {
			select {
			case f := <-c.out:
				if err := write(f, time.Now().Add(writeTimeout)); err != nil {
					c.abort()
					return
				}
			case <-c.done:
				// Graceful close: flush what is already queued (e.g. a final
				// error or session_closed notice), bounded by one timeout.
				deadline := time.Now().Add(writeTimeout)
				for {
					select {
					case f := <-c.out:
						if write(f, deadline) == nil {
							continue
						}
					default:
					}
					_ = c.Conn.Close()
					return
				}
			}
		}
	}()
}

func (c *SafeConn) enqueue(messageType int, data []byte) error {
	select {
	case <-c.done:
		return websocket.ErrCloseSent
	default:
	}
	select {
	case c.out <- queuedFrame{messageType, data}:
		return nil
	default:
		c.abort()
		return errSlowConsumer
	}
}

// abort closes the socket immediately, discarding queued frames.
func (c *SafeConn) abort() {
	c.closeOnce.Do(func() {
		if c.done != nil {
			close(c.done)
		}
	})
	_ = c.Conn.Close()
}

func (c *SafeConn) WriteMessage(messageType int, data []byte) error {
	if c.out != nil {
		return c.enqueue(messageType, data)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn.WriteMessage(messageType, data)
}

func (c *SafeConn) WriteJSON(v interface{}) error {
	if c.out != nil {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return c.enqueue(websocket.TextMessage, data)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctl == nil {
		return c.Conn.WriteJSON(v)
	}
	// Seal under the write lock so sequence numbers hit the wire in order.
	payload, err := c.ctl.seal(v)
	if err != nil || payload == nil {
		return err
	}
	return c.Conn.WriteMessage(websocket.TextMessage, payload)
}

// WriteControl may run concurrently with other writes (gorilla allows it), so
// pings are never stuck behind a full queue.
func (c *SafeConn) WriteControl(messageType int, data []byte, deadline time.Time) error {
	return c.Conn.WriteControl(messageType, data, deadline)
}

// Close closes the connection. With a write queue, frames already queued are
// flushed first (bounded by the write timeout) and the socket is closed by
// the writer goroutine.
func (c *SafeConn) Close() error {
	if c.out == nil {
		return c.Conn.Close()
	}
	c.closeOnce.Do(func() { close(c.done) })
	return nil
}
