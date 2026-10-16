package main

import (
	"time"

	"github.com/gorilla/websocket"
)

// sealPeer wraps a raw test websocket so WriteJSON seals control envelopes.
// setupCrypto must have been called first.
func sealPeer(raw *websocket.Conn, sessionID string, isHost bool) *SafeConn {
	return &SafeConn{Conn: raw, ctl: newCtlChannel(aesGCM, sessionID, isHost)}
}

// linkEpoch gives viewers the host epoch directly, standing in for the
// ctl_hello round trip in tests that drive the relay with fake peers.
func linkEpoch(host *SafeConn, viewers ...*SafeConn) {
	for _, v := range viewers {
		v.ctl.epoch = host.ctl.epoch
	}
}

// readSealed returns the next control message c accepts (sealed, or relay
// plaintext for viewers), skipping anything it would drop.
func readSealed(c *SafeConn, timeout time.Duration) (map[string]interface{}, error) {
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	defer c.SetReadDeadline(time.Time{})
	for {
		_, data, err := c.ReadMessage()
		if err != nil {
			return nil, err
		}
		var msg map[string]interface{}
		var ok bool
		if c.ctl.isHost {
			msg, ok = hostAcceptControl(c, data)
		} else {
			msg, ok = clientAcceptControl(c, data)
		}
		if ok {
			return msg, nil
		}
	}
}
