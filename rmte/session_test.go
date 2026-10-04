package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestValidateSessionID(t *testing.T) {
	valid := []string{"a", "abc", "myvps", "box123", "0123456789", "z9"}
	for _, id := range valid {
		if err := validateSessionID(id); err != nil {
			t.Errorf("expected %q to be valid, got: %v", id, err)
		}
	}

	invalid := []struct {
		id  string
		msg string
	}{
		{"", "empty"},
		{"abcdefghijk", "exceeds maximum"},
		{"MyVps", "only contain lowercase"},
		{"my-vps", "only contain lowercase"},
		{"my_vps", "only contain lowercase"},
		{"my vps", "only contain lowercase"},
		{"../box", "only contain lowercase"},
		{"@test", "only contain lowercase"},
	}

	for _, tc := range invalid {
		err := validateSessionID(tc.id)
		if err == nil {
			t.Errorf("expected %q to be invalid, but got nil error", tc.id)
		} else if !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("expected error for %q to contain %q, got: %v", tc.id, tc.msg, err)
		}
	}
}

func TestServerRejectsDuplicateSessionID(t *testing.T) {
	serverCfg = &ServeConfig{
		Mode:    modeHybrid,
		Port:    0,
		WebPath: "/",
		WSPath:  "/ws-rmte",
	}

	// Reset sessions map for testing
	sessionMu.Lock()
	sessions = make(map[string]*Session)
	sessionMu.Unlock()

	s := httptest.NewServer(http.HandlerFunc(handleWS))
	defer s.Close()

	wsURL := "ws" + strings.TrimPrefix(s.URL, "http")

	dialer := websocket.DefaultDialer

	// Connect Host 1 with custom session ID "box1"
	conn1, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("host 1 dial failed: %v", err)
	}
	defer conn1.Close()

	auth1 := map[string]string{
		"type":             "auth",
		"role":             "host",
		"session_id":       "box1",
		"auth_token":       generateAuthToken("secret1"),
		"protocol_version": protocolVersion,
	}
	if err := conn1.WriteJSON(auth1); err != nil {
		t.Fatalf("host 1 auth send failed: %v", err)
	}

	var resp1 struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
	}
	if err := conn1.ReadJSON(&resp1); err != nil {
		t.Fatalf("host 1 read resp failed: %v", err)
	}
	if resp1.Type != "auth_success" || resp1.SessionID != "box1" {
		t.Fatalf("host 1 auth failed: %+v", resp1)
	}

	// Connect Host 2 with DUPLICATE session ID "box1"
	conn2, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("host 2 dial failed: %v", err)
	}
	defer conn2.Close()

	auth2 := map[string]string{
		"type":             "auth",
		"role":             "host",
		"session_id":       "box1",
		"auth_token":       generateAuthToken("secret2"),
		"protocol_version": protocolVersion,
	}
	if err := conn2.WriteJSON(auth2); err != nil {
		t.Fatalf("host 2 auth send failed: %v", err)
	}

	_, rawResp2, err := conn2.ReadMessage()
	if err != nil {
		t.Fatalf("host 2 read message failed: %v", err)
	}
	var resp2 map[string]string
	if err := json.Unmarshal(rawResp2, &resp2); err != nil {
		t.Fatalf("host 2 unmarshal failed: %v, raw: %s", err, string(rawResp2))
	}
	if resp2["type"] != "error" {
		t.Fatalf("expected error response for duplicate session ID, got: %+v", resp2)
	}
	if !strings.Contains(resp2["message"], "already in use") {
		t.Fatalf("expected error message to contain 'already in use', got: %s", resp2["message"])
	}
}
