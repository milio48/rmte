package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// queuedServer returns a websocket URL whose server side is a queued
// SafeConn handed to onConn.
func queuedServer(t *testing.T, depth int, timeout time.Duration, onConn func(*SafeConn)) string {
	t.Helper()
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		c := &SafeConn{Conn: raw}
		c.startWriteQueue(depth, timeout)
		onConn(c)
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// S3: a peer that stops reading must not block writers (which the relay
// calls while holding session locks); it is disconnected instead.
func TestQueuedWritesNeverBlockOnStalledPeer(t *testing.T) {
	result := make(chan error, 1)
	elapsed := make(chan time.Duration, 1)
	url := queuedServer(t, 8, 2*time.Second, func(c *SafeConn) {
		payload := make([]byte, 1<<20)
		start := time.Now()
		var err error
		for i := 0; i < 200 && err == nil; i++ {
			err = c.WriteMessage(websocket.BinaryMessage, payload)
		}
		elapsed <- time.Since(start)
		result <- err
	})
	client, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close() // never reads

	select {
	case d := <-elapsed:
		if d > time.Second {
			t.Errorf("writes to a stalled peer blocked for %v", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writer blocked on a peer that does not read")
	}
	if err := <-result; err == nil {
		t.Error("expected the stalled peer to be disconnected as a slow consumer")
	}
}

func TestQueuedCloseFlushesPendingFrames(t *testing.T) {
	url := queuedServer(t, 16, 2*time.Second, func(c *SafeConn) {
		_ = c.WriteJSON(map[string]string{"type": "error", "message": "session not found"})
		_ = c.Close()
	})
	client, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var msg map[string]string
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := client.ReadJSON(&msg); err != nil || msg["message"] != "session not found" {
		t.Fatalf("final message lost on close: %v %v", msg, err)
	}
}

// S3: oversized frames are rejected before authentication.
func TestRelayRejectsLargePreAuthFrame(t *testing.T) {
	origCfg := serverCfg
	t.Cleanup(func() { serverCfg = origCfg })
	serverCfg = &ServeConfig{Mode: modeHybrid, WebPath: "/", WSPath: "/ws-rmte"}
	srv := httptest.NewServer(http.HandlerFunc(handleWS))
	defer srv.Close()

	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	big := `{"type":"auth","role":"viewer","pad":"` + strings.Repeat("x", 2<<20) + `"}`
	_ = c.WriteMessage(websocket.TextMessage, []byte(big))
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := c.ReadMessage(); err == nil {
		t.Fatal("relay accepted a 2 MB pre-auth frame")
	}
	if relayFrameReadLimit() <= 0 {
		t.Fatal("post-auth frame limit must be bounded by default")
	}
}

// S4: preview cookies expire server-side and are renewed while in use.
func TestPreviewCookieExpiryAndRenewal(t *testing.T) {
	if err := setupCrypto("preview-cookie-test"); err != nil {
		t.Fatal(err)
	}
	fresh := generatePreviewCookieValue("s", 3000)
	if !validatePreviewCookie("s", 3000, fresh) {
		t.Fatal("fresh cookie rejected")
	}
	if previewCookieNeedsRenewal("s", 3000, fresh) {
		t.Error("fresh cookie should not need renewal")
	}
	if validatePreviewCookie("s", 3001, fresh) || validatePreviewCookie("other", 3000, fresh) {
		t.Error("cookie must be bound to session and port")
	}

	mk := func(exp int64) string {
		return fmt.Sprintf("%d.%s.%x", exp, "abcd", previewCookieMAC("s", 3000, exp, "abcd"))
	}
	if validatePreviewCookie("s", 3000, mk(time.Now().Add(-time.Second).Unix())) {
		t.Error("expired cookie accepted")
	}
	if !previewCookieNeedsRenewal("s", 3000, mk(time.Now().Add(5*time.Minute).Unix())) {
		t.Error("cookie close to expiry should be renewed")
	}

	parts := strings.Split(fresh, ".")
	extended := fmt.Sprintf("%d.%s.%s", time.Now().Add(24*time.Hour).Unix(), parts[1], parts[2])
	if validatePreviewCookie("s", 3000, extended) {
		t.Error("cookie with a forged expiry accepted")
	}
}

// S4: only explicitly opened ports are forwarded.
func TestPreviewPortPolicy(t *testing.T) {
	t.Cleanup(func() { setPreviewPorts(nil) })

	ports, err := parsePreviewPorts("3000, 5173,8000-8002")
	if err != nil || fmt.Sprint(ports) != "[3000 5173 8000 8001 8002]" {
		t.Fatalf("parse: %v %v", ports, err)
	}
	for _, bad := range []string{"0", "70000", "abc", "9-1", "1-5000"} {
		if _, err := parsePreviewPorts(bad); err == nil {
			t.Errorf("accepted invalid spec %q", bad)
		}
	}

	setPreviewPorts(nil)
	if previewPortAllowed(6379) {
		t.Fatal("unopened port allowed by default")
	}
	if err := openPreviewPort(5173); err != nil || !previewPortAllowed(5173) {
		t.Fatalf("on-demand open failed: %v", err)
	}

	setPreviewPorts([]int{3000})
	if !previewPortAllowed(3000) {
		t.Fatal("allowlisted port refused")
	}
	if err := openPreviewPort(6379); err == nil || previewPortAllowed(6379) {
		t.Fatal("fixed allowlist must not be extended by viewers")
	}
}

// Tab ids must never collide with live tabs or the reserved 255.
func TestAllocTabIDSkipsUsedAndReserved(t *testing.T) {
	tabsMu.Lock()
	origTabs, origNext, origReserved := tabs, nextTabID, reservedTabIDs
	tabs = map[byte]*TabSession{0: {}, 1: {}, 2: {}}
	reservedTabIDs = map[byte]bool{}
	nextTabID = 254
	tabsMu.Unlock()
	t.Cleanup(func() {
		tabsMu.Lock()
		tabs, nextTabID, reservedTabIDs = origTabs, origNext, origReserved
		tabsMu.Unlock()
	})

	id, ok := allocTabID()
	if !ok || id != 254 {
		t.Fatalf("first id = %d (%v), want 254", id, ok)
	}
	id, ok = allocTabID()
	if !ok || id != 3 {
		t.Fatalf("after wrap got %d (%v), want 3 (skipping 255 and live tabs 0-2)", id, ok)
	}

	seen := map[byte]bool{254: true, 3: true}
	for {
		id, ok := allocTabID()
		if !ok {
			break
		}
		if id == dataChannelTabID || seen[id] || id <= 2 {
			t.Fatalf("allocated colliding id %d", id)
		}
		seen[id] = true
	}
	if len(seen) != 252 {
		t.Fatalf("expected 252 free ids, got %d", len(seen))
	}
}
