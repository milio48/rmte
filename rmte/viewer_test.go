package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestBackoffDelay(t *testing.T) {
	cases := []struct {
		attempt  int
		expected time.Duration
	}{
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 3 * time.Second},
		{4, 4 * time.Second},
		{5, 5 * time.Second},
		{6, 5 * time.Second},
		{10, 5 * time.Second},
		{50, 5 * time.Second},
	}

	for _, tc := range cases {
		got := backoffDelay(tc.attempt)
		if got != tc.expected {
			t.Errorf("attempt %d: got %v, want %v", tc.attempt, got, tc.expected)
		}
	}
}

func TestViewerConnWriteAndSwap(t *testing.T) {
	upgrader := websocket.Upgrader{}
	receivedMsgs := make([]string, 0)
	var recMu sync.Mutex

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				break
			}
			recMu.Lock()
			receivedMsgs = append(receivedMsgs, string(msg))
			recMu.Unlock()
		}
	}))
	defer s.Close()

	wsURL := "ws" + strings.TrimPrefix(s.URL, "http")

	// 1. Initial connection
	rawConn1, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial 1 failed: %v", err)
	}
	vc := NewViewerConn(&SafeConn{Conn: rawConn1})
	defer vc.Close()

	if err := vc.WriteJSON(map[string]string{"msg": "first"}); err != nil {
		t.Fatalf("write to first conn failed: %v", err)
	}

	// 2. Connect new socket and swap
	rawConn2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial 2 failed: %v", err)
	}
	vc.Swap(&SafeConn{Conn: rawConn2})

	if err := vc.WriteJSON(map[string]string{"msg": "second"}); err != nil {
		t.Fatalf("write to second conn failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	recMu.Lock()
	count := len(receivedMsgs)
	recMu.Unlock()

	if count != 2 {
		t.Fatalf("expected 2 messages received across swaps, got %d", count)
	}

	// 3. Close and verify writes return error
	_ = vc.Close()
	if err := vc.WriteJSON(map[string]string{"msg": "third"}); err == nil {
		t.Fatalf("expected error writing to closed ViewerConn, got nil")
	}
}
