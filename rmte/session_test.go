package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestDNSFallbackLookup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(ctx, "one.one.one.one")
	if err != nil {
		t.Logf("DNS lookup warning (network/firewall): %v", err)
		return
	}
	if len(addrs) == 0 {
		t.Errorf("expected at least 1 IP address for one.one.one.one")
	}
}

func TestShouldBypassPhantomDNS(t *testing.T) {
	cases := []struct {
		address             string
		hasSystemResolvConf bool
		expectedBypass      bool
	}{
		// When /etc/resolv.conf is absent or empty (Android/Termux/minimal containers):
		{"[::1]:53", false, true},
		{"127.0.0.1:53", false, true},
		{"127.0.0.10:53", false, false}, // Subnet loopback is not a phantom
		{"127.0.0.53:53", false, false}, // systemd-resolved is not a phantom
		{"1.1.1.1:53", false, false},    // Public IP is not a phantom
		{"8.8.8.8:53", false, false},    // Public IP is not a phantom

		// When /etc/resolv.conf exists and configures nameservers (Linux with local dnsmasq/Pi-hole):
		{"[::1]:53", true, false},      // Respected as system-configured
		{"127.0.0.1:53", true, false},  // Respected as system-configured
		{"127.0.0.10:53", true, false}, // Respected
		{"127.0.0.53:53", true, false}, // Respected
	}

	for _, tc := range cases {
		got := shouldBypassPhantomDNS(tc.address, tc.hasSystemResolvConf)
		if got != tc.expectedBypass {
			t.Errorf("shouldBypassPhantomDNS(%q, %v): got %v, want %v",
				tc.address, tc.hasSystemResolvConf, got, tc.expectedBypass)
		}
	}
}

func TestHasConfiguredNameservers(t *testing.T) {
	// 1. Non-existent file
	if hasConfiguredNameservers("non_existent_resolv_file.conf") {
		t.Errorf("expected false for non-existent file")
	}

	// 2. Empty file
	tmpEmpty, err := os.CreateTemp("", "resolv_empty_*")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpEmpty.Name())
	tmpEmpty.Close()
	if hasConfiguredNameservers(tmpEmpty.Name()) {
		t.Errorf("expected false for empty file")
	}

	// 3. File with only comments and options
	tmpComment, err := os.CreateTemp("", "resolv_comment_*")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpComment.Name())
	tmpComment.WriteString("# Generated by network-manager\noptions edns0\n; search local\n")
	tmpComment.Close()
	if hasConfiguredNameservers(tmpComment.Name()) {
		t.Errorf("expected false for comment-only file")
	}

	// 4. File with invalid non-IP nameserver directive
	tmpGarbage, err := os.CreateTemp("", "resolv_garbage_*")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpGarbage.Name())
	tmpGarbage.WriteString("nameserver not_an_ip\nnameserver garbage.local\n")
	tmpGarbage.Close()
	if hasConfiguredNameservers(tmpGarbage.Name()) {
		t.Errorf("expected false for invalid non-IP nameserver")
	}

	// 5. File with valid nameserver
	tmpValid, err := os.CreateTemp("", "resolv_valid_*")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpValid.Name())
	tmpValid.WriteString("# Nameservers\nnameserver 1.1.1.1\nnameserver 8.8.8.8\n")
	tmpValid.Close()
	if !hasConfiguredNameservers(tmpValid.Name()) {
		t.Errorf("expected true for file with valid nameserver")
	}
}

func TestDNSFallbackBypassesPhantomLoopback(t *testing.T) {
	setupDNSFallback()
	resolver := net.DefaultResolver
	if resolver.Dial == nil {
		t.Fatalf("expected DefaultResolver.Dial to be configured")
	}

	hasSystemResolv := hasConfiguredNameservers("/etc/resolv.conf")

	// If system has no /etc/resolv.conf (Windows, Termux, minimal containers),
	// dialing [::1]:53 or 127.0.0.1:53 must be bypassed to trusted public DNS.
	// If system HAS /etc/resolv.conf (Linux server/desktop), it should NOT be bypassed.
	for _, phantom := range []string{"[::1]:53", "127.0.0.1:53"} {
		conn, err := resolver.Dial(context.Background(), "udp", phantom)
		if err != nil {
			t.Fatalf("failed to dial phantom %s: %v", phantom, err)
		}
		defer conn.Close()

		remote := conn.RemoteAddr().String()
		if !hasSystemResolv {
			if remote != "1.1.1.1:53" && remote != "8.8.8.8:53" {
				t.Errorf("phantom resolver %s was not bypassed to public DNS on system without resolv.conf, got remote %s", phantom, remote)
			}
		} else {
			// On Linux with real resolv.conf, phantom host should not be bypassed
			if remote == "1.1.1.1:53" || remote == "8.8.8.8:53" {
				t.Errorf("system resolver %s was incorrectly bypassed despite valid resolv.conf", phantom)
			}
		}
	}

	// Negative cases: valid system loopbacks (systemd-resolved on 127.0.0.53, or local subnet 127.0.0.10)
	// must NOT be falsely identified as phantoms across any OS.
	for _, valid := range []string{"127.0.0.10:53", "127.0.0.53:53"} {
		conn, err := resolver.Dial(context.Background(), "udp", valid)
		if err != nil {
			t.Fatalf("failed to dial valid resolver %s: %v", valid, err)
		}
		defer conn.Close()

		remote := conn.RemoteAddr().String()
		if remote != valid {
			t.Errorf("valid resolver %s was incorrectly bypassed, got remote %s", valid, remote)
		}
	}
}

func TestEmbeddedRootCAs(t *testing.T) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(embeddedRootCAs)) {
		t.Fatalf("failed to parse embeddedRootCAs into x509.CertPool")
	}

	// Hermetically verify every embedded certificate block without external network egress
	rest := []byte(embeddedRootCAs)
	var certCount int
	now := time.Now()
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("failed to parse certificate %d: %v", certCount, err)
		}
		certCount++
		if !cert.IsCA {
			t.Errorf("expected cert %q to be CA", cert.Subject.CommonName)
		}
		if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
			t.Errorf("cert %q is not currently valid (%v to %v)", cert.Subject.CommonName, cert.NotBefore, cert.NotAfter)
		}
		// Verify root certificate self-signature hermetically
		if err := cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature); err != nil {
			t.Errorf("cert %q failed self-signature check: %v", cert.Subject.CommonName, err)
		}
	}
	if certCount < 6 {
		t.Errorf("expected at least 6 embedded root CAs, parsed %d", certCount)
	}
}

func TestSetupTLSConfigInsecure(t *testing.T) {
	setupTLSConfig(true)
	if websocket.DefaultDialer.TLSClientConfig == nil || !websocket.DefaultDialer.TLSClientConfig.InsecureSkipVerify {
		t.Errorf("expected InsecureSkipVerify to be true")
	}
	// Restore standard secure config
	setupTLSConfig(false)
	if websocket.DefaultDialer.TLSClientConfig == nil || websocket.DefaultDialer.TLSClientConfig.InsecureSkipVerify {
		t.Errorf("expected InsecureSkipVerify to be false after restore")
	}
}

func TestSanitizeTerminalOutput(t *testing.T) {
	// Reset carry buffer before test
	sanitizerCarryMu.Lock()
	sanitizerCarry = ""
	sanitizerCarryMu.Unlock()

	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{"No sequences", "hello world", "hello world"},
		{"Win32 input mode enable suppressed", "\x1b[?9001hHello\x1b[?9001h", "Hello"},
		{"Mixed ANSI colors kept", "\x1b[?9001h\x1b[32mOK\x1b[0m", "\x1b[32mOK\x1b[0m"},
	}

	for _, tc := range cases {
		got := sanitizeTerminalOutput(tc.input)
		if got != tc.expected {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.expected)
		}
	}

	// Test chunked sequence split across two frames
	sanitizerCarryMu.Lock()
	sanitizerCarry = ""
	sanitizerCarryMu.Unlock()

	chunk1 := "Banner: \x1b[?90"
	chunk2 := "01hWelcome!"
	res1 := sanitizeTerminalOutput(chunk1)
	res2 := sanitizeTerminalOutput(chunk2)
	if res1+res2 != "Banner: Welcome!" {
		t.Errorf("chunked sanitization failed: got %q + %q, want 'Banner: Welcome!'", res1, res2)
	}
}

func TestIsExitKeySequence(t *testing.T) {
	cases := []struct {
		name     string
		input    []byte
		expected bool
	}{
		{"Raw Ctrl+Q", []byte{17}, true},
		{"Raw Ctrl+]", []byte{29}, true},
		{"Raw normal 'a'", []byte{'a'}, false},
		{"Raw normal 'q'", []byte{'q'}, false},
		{"Raw normal ']'", []byte{']'}, false},
		{"Raw Ctrl+C", []byte{3}, false},
		{"CSI-u Ctrl+]", []byte("\x1b[93;5u"), true},
		{"CSI-u Ctrl+Q", []byte("\x1b[113;5u"), true},
		{"Win32 Ctrl+Q (Uc=17, Vk=81, LeftCtrl)", []byte("\x1b[81;16;17;1;8;1_"), true},
		{"Win32 Ctrl+] (Uc=29)", []byte("\x1b[221;27;29;1;8;1_"), true},
		{"Win32 Ctrl+] (Uc=93, Vk=221, LeftCtrl)", []byte("\x1b[221;27;93;1;8;1_"), true},
		{"Win32 Ctrl+] (Uc=0, Vk=221, RightCtrl)", []byte("\x1b[221;27;0;1;4;1_"), true},
		{"Win32 Ctrl+] KeyUp (kd=0)", []byte("\x1b[221;27;29;0;8;1_"), false},
		{"Win32 normal ']' (no ctrl)", []byte("\x1b[221;27;93;1;0;1_"), false},
		{"Win32 normal 'q' (no ctrl)", []byte("\x1b[81;16;113;1;0;1_"), false},
		{"Win32 Ctrl+C", []byte("\x1b[67;46;3;1;8;1_"), false},
	}

	for _, tc := range cases {
		got := isExitKeySequence(tc.input)
		if got != tc.expected {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.expected)
		}
	}
}

