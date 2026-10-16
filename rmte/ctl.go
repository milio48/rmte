package main

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"sync"
)

// End-to-end envelope for viewer<->host control messages.
//
// Every control message between a viewer and the host travels as
//
//	{"type":"e2e","target_conn":"<routing id>","d":"<base64 nonce||ciphertext>"}
//
// The relay only sees "type" and "target_conn" (which it needs for routing).
// The ciphertext is AES-GCM over
//
//	{"s":"<sender stream id>","n":<seq>,"e":"<host epoch>","m":{...original message...}}
//
// with AAD = ctlAADPrefix | direction | session id. This gives:
//   - confidentiality and integrity: the relay can neither read nor forge
//     file operations, ai_exec, chat, etc.;
//   - replay protection: each sender picks a random stream id per process and
//     numbers its messages; the receiver rejects any (stream, seq) that is not
//     strictly increasing;
//   - cross-process replay protection: the host's stream id doubles as its
//     epoch, and viewer->host messages must name the current epoch, so
//     messages captured while an earlier host process was running are
//     rejected. Viewers learn the epoch from the first host message they
//     receive (the host answers "ctl_hello" for that purpose) and queue
//     anything else until then.
//
// Messages the relay itself originates (auth, ping/pong, host_status,
// session_closed, viewer_disconnected) stay plaintext and must only be
// trusted for what the relay already controls.

const (
	ctlAADPrefix    = "rmte-ctl-v1"
	ctlEnvelopeType = "e2e"
	ctlHelloAction  = "ctl_hello"
	ctlResyncAction = "ctl_resync"
	ctlMaxStreams   = 4096
)

var (
	errCtlDecrypt    = errors.New("control envelope failed authentication")
	errCtlReplay     = errors.New("control message replayed or out of order")
	errCtlStaleEpoch = errors.New("control message bound to a different host epoch")
	errCtlMalformed  = errors.New("malformed control envelope")
)

type ctlOuter struct {
	Type       string `json:"type"`
	TargetConn string `json:"target_conn,omitempty"`
	Data       string `json:"d"`
}

type ctlInner struct {
	Stream string          `json:"s"`
	Seq    uint64          `json:"n"`
	Epoch  string          `json:"e,omitempty"`
	Msg    json.RawMessage `json:"m"`
}

type ctlStreamState struct {
	seq  uint64
	used uint64
}

// ctlChannel holds one endpoint's envelope state. The host keeps a single
// channel for its whole process (its stream id is the epoch) and attaches it
// to every relay connection it makes; each viewer process does the same.
type ctlChannel struct {
	aead      cipher.AEAD
	isHost    bool
	sessionID string

	mu      sync.Mutex
	stream  string
	seq     uint64
	epoch   string // host: own stream id; viewer: learned host stream id
	streams map[string]*ctlStreamState
	clock   uint64
	queue   []interface{} // viewer: messages waiting for the host epoch
}

func newCtlChannel(aead cipher.AEAD, sessionID string, isHost bool) *ctlChannel {
	var id [16]byte
	if _, err := io.ReadFull(rand.Reader, id[:]); err != nil {
		panic("rmte: crypto/rand failed: " + err.Error())
	}
	c := &ctlChannel{
		aead:      aead,
		isHost:    isHost,
		sessionID: sessionID,
		stream:    hex.EncodeToString(id[:]),
		streams:   make(map[string]*ctlStreamState),
	}
	if isHost {
		c.epoch = c.stream
	}
	return c
}

func (c *ctlChannel) aad(dir byte) []byte {
	aad := make([]byte, 0, len(ctlAADPrefix)+1+len(c.sessionID))
	aad = append(aad, ctlAADPrefix...)
	aad = append(aad, dir)
	aad = append(aad, c.sessionID...)
	return aad
}

func (c *ctlChannel) sendDir() byte {
	if c.isHost {
		return dirToViewer
	}
	return dirToHost
}

func (c *ctlChannel) recvDir() byte {
	if c.isHost {
		return dirToHost
	}
	return dirToViewer
}

// seal encrypts v into an outer envelope. A viewer that does not know the host
// epoch yet queues everything except ctl_hello and returns (nil, nil).
// Callers must serialise seal with the socket write so that sequence numbers
// reach the wire in order.
func (c *ctlChannel) seal(v interface{}) ([]byte, error) {
	msg, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var head struct {
		Action     string `json:"action"`
		TargetConn string `json:"target_conn"`
	}
	_ = json.Unmarshal(msg, &head)

	c.mu.Lock()
	if !c.isHost && c.epoch == "" && head.Action != ctlHelloAction {
		c.queue = append(c.queue, v)
		c.mu.Unlock()
		return nil, nil
	}
	c.seq++
	inner := ctlInner{Stream: c.stream, Seq: c.seq, Msg: msg}
	if !c.isHost {
		inner.Epoch = c.epoch
	}
	c.mu.Unlock()

	plain, err := json.Marshal(inner)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	sealed := c.aead.Seal(nonce, nonce, plain, c.aad(c.sendDir()))

	outer := ctlOuter{Type: ctlEnvelopeType, Data: base64.StdEncoding.EncodeToString(sealed)}
	if c.isHost {
		// Only the host names a destination; the relay stamps the sender's
		// connection id on viewer->host envelopes.
		outer.TargetConn = head.TargetConn
	}
	return json.Marshal(outer)
}

// open authenticates and decodes an outer envelope. For a viewer it also
// returns any messages that were queued while the host epoch was unknown,
// which the caller must now send.
func (c *ctlChannel) open(outer *ctlOuter) (map[string]interface{}, []interface{}, error) {
	sealed, err := base64.StdEncoding.DecodeString(outer.Data)
	if err != nil || len(sealed) < c.aead.NonceSize() {
		return nil, nil, errCtlMalformed
	}
	ns := c.aead.NonceSize()
	plain, err := c.aead.Open(nil, sealed[:ns], sealed[ns:], c.aad(c.recvDir()))
	if err != nil {
		return nil, nil, errCtlDecrypt
	}

	var inner ctlInner
	if err := json.Unmarshal(plain, &inner); err != nil || inner.Stream == "" || inner.Seq == 0 {
		return nil, nil, errCtlMalformed
	}
	var msg map[string]interface{}
	if err := json.Unmarshal(inner.Msg, &msg); err != nil || msg == nil {
		return nil, nil, errCtlMalformed
	}
	action, _ := msg["action"].(string)

	c.mu.Lock()
	defer c.mu.Unlock()

	st := c.streams[inner.Stream]
	if st != nil && inner.Seq <= st.seq {
		return nil, nil, errCtlReplay
	}
	if c.isHost && inner.Epoch != c.epoch && action != ctlHelloAction {
		return msg, nil, errCtlStaleEpoch
	}

	if st == nil {
		if len(c.streams) >= ctlMaxStreams {
			c.evictOldestLocked()
		}
		st = &ctlStreamState{}
		c.streams[inner.Stream] = st
	}
	c.clock++
	st.seq = inner.Seq
	st.used = c.clock

	var flush []interface{}
	if !c.isHost && c.epoch != inner.Stream {
		c.epoch = inner.Stream
		flush = c.queue
		c.queue = nil
	}
	return msg, flush, nil
}

func (c *ctlChannel) evictOldestLocked() {
	var oldest string
	var oldestUsed uint64
	for id, st := range c.streams {
		if oldest == "" || st.used < oldestUsed {
			oldest, oldestUsed = id, st.used
		}
	}
	delete(c.streams, oldest)
}

// relayPlaintextActions are the only plaintext control messages a client
// accepts; they are generated by the relay itself, which can already forge
// them, so they carry no host authority.
func isRelayPlaintextControl(msg map[string]interface{}) bool {
	switch msg["type"] {
	case "auth_success", "error":
		return true
	}
	switch msg["action"] {
	case "pong", "host_status", "session_closed", "viewer_disconnected":
		return true
	}
	return false
}

// readControl decodes a text frame received on c. Envelopes are opened with
// c's channel; plaintext is returned with authenticated=false so the caller
// can apply isRelayPlaintextControl. Queued viewer messages are flushed on c
// once the host epoch becomes known.
func (c *SafeConn) readControl(data []byte) (msg map[string]interface{}, authenticated bool, err error) {
	var outer ctlOuter
	if err := json.Unmarshal(data, &outer); err != nil {
		return nil, false, err
	}
	if outer.Type != ctlEnvelopeType {
		var plain map[string]interface{}
		if err := json.Unmarshal(data, &plain); err != nil {
			return nil, false, err
		}
		return plain, false, nil
	}
	if c.ctl == nil {
		return nil, false, errCtlMalformed
	}

	msg, flush, err := c.ctl.open(&outer)
	if msg != nil && outer.TargetConn != "" {
		// Viewer->host: the relay-stamped sender id is how the host routes
		// its reply. It is routing only, never authority.
		msg["target_conn"] = outer.TargetConn
	}
	if err != nil {
		return msg, false, err
	}
	for _, m := range flush {
		_ = c.WriteJSON(m)
	}
	return msg, true, nil
}

// hostCtl is the host process's envelope channel; its stream id is the epoch.
var hostCtl *ctlChannel

// announceHostEpoch broadcasts a sealed ctl_hello so connected viewers learn
// the current epoch (after start-up or a host restart within the grace period).
func announceHostEpoch(conn *SafeConn) {
	_ = conn.WriteJSON(map[string]interface{}{
		"type":   "control",
		"action": ctlHelloAction,
	})
}

// hostAcceptControl decodes a text frame from the relay. Viewer messages must
// arrive in a valid envelope; the only plaintext the host acts on is the
// relay's own viewer_disconnected notice.
func hostAcceptControl(conn *SafeConn, data []byte) (map[string]interface{}, bool) {
	msg, authenticated, err := conn.readControl(data)
	if err != nil {
		if errors.Is(err, errCtlStaleEpoch) && msg != nil {
			// Sealed with an older host epoch: tell the viewer the current one.
			targetConn, _ := msg["target_conn"].(string)
			_ = conn.WriteJSON(map[string]interface{}{
				"type":        "control",
				"action":      ctlResyncAction,
				"target_conn": targetConn,
			})
		}
		log.Printf("[Security] Dropped control message: %v", err)
		return nil, false
	}
	if !authenticated {
		if action, _ := msg["action"].(string); action == "viewer_disconnected" {
			return msg, true
		}
		log.Printf("[Security] Dropped unauthenticated control message (action %q)", msg["action"])
		return nil, false
	}
	return msg, true
}

// clientAcceptControl decodes a text frame on a viewer/bot connection. Host
// messages must be sealed; relay-originated status messages are allowed in
// plaintext. Internal envelope bookkeeping (ctl_hello / ctl_resync) is
// consumed here.
func clientAcceptControl(conn *SafeConn, data []byte) (map[string]interface{}, bool) {
	msg, authenticated, err := conn.readControl(data)
	if err != nil {
		return nil, false
	}
	if !authenticated {
		return msg, isRelayPlaintextControl(msg)
	}
	switch msg["action"] {
	case ctlHelloAction, ctlResyncAction:
		return nil, false
	}
	return msg, true
}
