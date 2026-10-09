package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestServerAllowsHostReconnectWithSameAuthToken(t *testing.T) {
	origCfg := serverCfg
	origGrace := hostReconnectGracePeriod
	t.Cleanup(func() {
		serverCfg = origCfg
		hostReconnectGracePeriod = origGrace
		sessionMu.Lock()
		sessions = make(map[string]*Session)
		sessionMu.Unlock()
	})

	serverCfg = &ServeConfig{
		Mode:    modeHybrid,
		Port:    0,
		WebPath: "/",
		WSPath:  "/ws-rmte",
	}
	hostReconnectGracePeriod = 2 * time.Second

	sessionMu.Lock()
	sessions = make(map[string]*Session)
	sessionMu.Unlock()

	s := httptest.NewServer(http.HandlerFunc(handleWS))
	defer s.Close()

	wsURL := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := websocket.DefaultDialer

	// 1. Host 1 connects with preview enabled and password "mypass123"
	conn1, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("host 1 dial failed: %v", err)
	}

	token := generateAuthToken("mypass123")
	auth1 := map[string]interface{}{
		"type":             "auth",
		"role":             "host",
		"session_id":       "rcbox",
		"auth_token":       token,
		"protocol_version": protocolVersion,
		"preview":          true,
	}
	if err := conn1.WriteJSON(auth1); err != nil {
		t.Fatalf("host 1 auth send failed: %v", err)
	}

	var resp1 struct {
		Type        string `json:"type"`
		SessionID   string `json:"session_id"`
		ProxySecret string `json:"proxy_secret"`
	}
	if err := conn1.ReadJSON(&resp1); err != nil {
		t.Fatalf("host 1 read resp failed: %v", err)
	}
	if resp1.Type != "auth_success" || resp1.SessionID != "rcbox" || resp1.ProxySecret == "" {
		t.Fatalf("host 1 auth failed or empty proxy secret: %+v", resp1)
	}
	initialProxySecret := resp1.ProxySecret

	// 2. Viewer connects
	viewerConn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("viewer dial failed: %v", err)
	}
	defer viewerConn.Close()

	viewerAuth := map[string]string{
		"type":             "auth",
		"role":             "viewer",
		"session_id":       "rcbox",
		"auth_token":       token,
		"viewer_id":        "viewer_alice",
		"protocol_version": protocolVersion,
	}
	if err := viewerConn.WriteJSON(viewerAuth); err != nil {
		t.Fatalf("viewer auth send failed: %v", err)
	}

	var viewerResp map[string]interface{}
	if err := viewerConn.ReadJSON(&viewerResp); err != nil || viewerResp["type"] != "auth_success" {
		t.Fatalf("viewer auth failed: %v, resp: %+v", err, viewerResp)
	}
	// Read chat_history initial message
	var chatHistMsg map[string]interface{}
	if err := viewerConn.ReadJSON(&chatHistMsg); err != nil {
		t.Fatalf("viewer read chat history failed: %v", err)
	}

	// Viewer sends a chat message
	testChat := map[string]interface{}{
		"type":    "control",
		"action":  "chat",
		"user":    "Alice",
		"message": "Hello from viewer",
	}
	if err := viewerConn.WriteJSON(testChat); err != nil {
		t.Fatalf("viewer send chat failed: %v", err)
	}
	// Drain the chat broadcast on viewer
	var chatEcho map[string]interface{}
	_ = viewerConn.ReadJSON(&chatEcho)

	// 3. Host 1 suddenly drops (TCP disconnect)
	_ = conn1.Close()

	// 4. Viewer should receive host_status "disconnected"
	var statusMsg map[string]interface{}
	_ = viewerConn.SetReadDeadline(time.Now().Add(1 * time.Second))
	if err := viewerConn.ReadJSON(&statusMsg); err != nil {
		t.Fatalf("viewer expected host_status disconnected message, got error: %v", err)
	}
	if statusMsg["action"] != "host_status" || statusMsg["status"] != "disconnected" {
		t.Fatalf("expected host_status disconnected, got: %+v", statusMsg)
	}

	// 5. Verify that session "rcbox" STILL EXISTS on the relay (grace period active)
	sessionMu.RLock()
	activeSess, sessExists := sessions["rcbox"]
	sessionMu.RUnlock()
	if !sessExists {
		t.Fatalf("session rcbox was destroyed immediately instead of entering grace period!")
	}
	if len(activeSess.ChatHistory) == 0 {
		t.Fatalf("expected chat history to be preserved in session")
	}

	// 6. Imposter tries to hijack "rcbox" with wrong token -> Must be rejected!
	imposterConn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("imposter dial failed: %v", err)
	}
	defer imposterConn.Close()

	imposterAuth := map[string]interface{}{
		"type":             "auth",
		"role":             "host",
		"session_id":       "rcbox",
		"auth_token":       generateAuthToken("wrongpassword"),
		"protocol_version": protocolVersion,
	}
	_ = imposterConn.WriteJSON(imposterAuth)
	var imposterResp map[string]interface{}
	_ = imposterConn.ReadJSON(&imposterResp)
	if imposterResp["type"] != "error" {
		t.Fatalf("expected imposter to be rejected, got: %+v", imposterResp)
	}

	// 7. Legitimate Host reconnects with correct token
	conn2, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("host reconnect dial failed: %v", err)
	}
	defer conn2.Close()

	authReconnect := map[string]interface{}{
		"type":             "auth",
		"role":             "host",
		"session_id":       "rcbox",
		"auth_token":       token,
		"protocol_version": protocolVersion,
		"preview":          true,
	}
	if err := conn2.WriteJSON(authReconnect); err != nil {
		t.Fatalf("host reconnect auth send failed: %v", err)
	}

	var respReconnect struct {
		Type        string `json:"type"`
		SessionID   string `json:"session_id"`
		ProxySecret string `json:"proxy_secret"`
	}
	if err := conn2.ReadJSON(&respReconnect); err != nil {
		t.Fatalf("host reconnect read resp failed: %v", err)
	}
	if respReconnect.Type != "auth_success" || respReconnect.SessionID != "rcbox" {
		t.Fatalf("host reconnect expected auth_success, got: %+v", respReconnect)
	}
	if respReconnect.ProxySecret != initialProxySecret {
		t.Fatalf("expected proxy secret to be preserved across reconnect (%s != %s)", respReconnect.ProxySecret, initialProxySecret)
	}

	// 8. Viewer receives host_status "connected" notification
	var reconnectStatusMsg map[string]interface{}
	_ = viewerConn.SetReadDeadline(time.Now().Add(1 * time.Second))
	if err := viewerConn.ReadJSON(&reconnectStatusMsg); err != nil {
		t.Fatalf("viewer expected host_status connected message, got: %v", err)
	}
	if reconnectStatusMsg["action"] != "host_status" || reconnectStatusMsg["status"] != "connected" {
		t.Fatalf("expected host_status connected, got: %+v", reconnectStatusMsg)
	}
}

func TestHostGracePeriodExpiration(t *testing.T) {
	origCfg := serverCfg
	origGrace := hostReconnectGracePeriod
	t.Cleanup(func() {
		serverCfg = origCfg
		hostReconnectGracePeriod = origGrace
		sessionMu.Lock()
		sessions = make(map[string]*Session)
		sessionMu.Unlock()
	})

	serverCfg = &ServeConfig{
		Mode:    modeHybrid,
		Port:    0,
		WebPath: "/",
		WSPath:  "/ws-rmte",
	}
	// Short grace period for fast testing
	hostReconnectGracePeriod = 150 * time.Millisecond

	sessionMu.Lock()
	sessions = make(map[string]*Session)
	sessionMu.Unlock()

	s := httptest.NewServer(http.HandlerFunc(handleWS))
	defer s.Close()

	wsURL := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := websocket.DefaultDialer

	// Connect Host
	hostConn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("host dial failed: %v", err)
	}

	token := generateAuthToken("mypass123")
	authHost := map[string]interface{}{
		"type":             "auth",
		"role":             "host",
		"session_id":       "expsess",
		"auth_token":       token,
		"protocol_version": protocolVersion,
	}
	if err := hostConn.WriteJSON(authHost); err != nil {
		t.Fatalf("host auth write failed: %v", err)
	}
	var hostResp map[string]interface{}
	_ = hostConn.ReadJSON(&hostResp)

	// Connect Viewer
	viewerConn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("viewer dial failed: %v", err)
	}
	defer viewerConn.Close()

	viewerAuth := map[string]string{
		"type":             "auth",
		"role":             "viewer",
		"session_id":       "expsess",
		"auth_token":       token,
		"viewer_id":        "viewer_bob",
		"protocol_version": protocolVersion,
	}
	_ = viewerConn.WriteJSON(viewerAuth)
	var viewerResp map[string]interface{}
	_ = viewerConn.ReadJSON(&viewerResp)
	var chatHist map[string]interface{}
	_ = viewerConn.ReadJSON(&chatHist)

	// Host drops
	_ = hostConn.Close()

	// Read host_status disconnected
	var discMsg map[string]interface{}
	_ = viewerConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_ = viewerConn.ReadJSON(&discMsg)

	// Read session_closed message after grace period (150ms) expires
	var closedMsg map[string]interface{}
	_ = viewerConn.SetReadDeadline(time.Now().Add(1 * time.Second))
	if err := viewerConn.ReadJSON(&closedMsg); err != nil {
		t.Fatalf("expected viewer to receive session_closed message on expiry, got error: %v", err)
	}
	if closedMsg["action"] != "session_closed" {
		t.Fatalf("expected action session_closed, got: %+v", closedMsg)
	}

	// Verify session is deleted from sessions map
	time.Sleep(50 * time.Millisecond)
	sessionMu.RLock()
	_, exists := sessions["expsess"]
	sessionMu.RUnlock()
	if exists {
		t.Fatalf("expected session expsess to be deleted after grace period expired")
	}
}
