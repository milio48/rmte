package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
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
	_, sessExists := sessions["rcbox"]
	sessionMu.RUnlock()
	if !sessExists {
		t.Fatalf("session rcbox was destroyed immediately instead of entering grace period!")
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
		// Without IPv6 (some containers) the plain dial fails and the resolver
		// falls back to public DNS for an unrelated reason; skip that case.
		if hasSystemResolv {
			probe, perr := net.Dial("udp", phantom)
			if perr != nil {
				t.Logf("skipping %s: loopback not dialable here: %v", phantom, perr)
				continue
			}
			probe.Close()
		}
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

func TestPresenceLobbyInitialAndFocus(t *testing.T) {
	serverCfg = &ServeConfig{
		Mode:    modeHybrid,
		Port:    0,
		WebPath: "/",
		WSPath:  "/ws-rmte",
	}

	sessionMu.Lock()
	sessions = make(map[string]*Session)
	sessionMu.Unlock()

	s := httptest.NewServer(http.HandlerFunc(handleWS))
	defer s.Close()

	wsURL := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := websocket.DefaultDialer

	// 1. Host connects
	hostWS, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("host dial failed: %v", err)
	}
	defer hostWS.Close()

	token := generateAuthToken("prespass")
	authHost := map[string]interface{}{
		"type":             "auth",
		"role":             "host",
		"session_id":       "presbox",
		"auth_token":       token,
		"protocol_version": protocolVersion,
	}
	if err := hostWS.WriteJSON(authHost); err != nil {
		t.Fatalf("host auth write failed: %v", err)
	}
	var hostAuthResp map[string]interface{}
	if err := hostWS.ReadJSON(&hostAuthResp); err != nil || hostAuthResp["type"] != "auth_success" {
		t.Fatalf("host auth failed: %v, resp: %+v", err, hostAuthResp)
	}

	// 2. Viewer connects
	viewerWS, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("viewer dial failed: %v", err)
	}
	defer viewerWS.Close()

	authViewer := map[string]interface{}{
		"type":             "auth",
		"role":             "viewer",
		"session_id":       "presbox",
		"auth_token":       token,
		"viewer_id":        "v-test-1",
		"viewer_name":      "fzclitui",
		"protocol_version": protocolVersion,
	}
	if err := viewerWS.WriteJSON(authViewer); err != nil {
		t.Fatalf("viewer auth write failed: %v", err)
	}
	var viewerAuthResp map[string]interface{}
	if err := viewerWS.ReadJSON(&viewerAuthResp); err != nil || viewerAuthResp["type"] != "auth_success" {
		t.Fatalf("viewer auth failed: %v, resp: %+v", err, viewerAuthResp)
	}

	if err := setupCrypto("prespass"); err != nil {
		t.Fatal(err)
	}
	host := sealPeer(hostWS, "presbox", true)
	viewer := sealPeer(viewerWS, "presbox", false)
	linkEpoch(host, viewer)

	// 3. Viewer sends initial unfocus (announcing presence in Lobby before joining any tab)
	unfocusMsg := map[string]interface{}{
		"type":        "control",
		"action":      "unfocus",
		"viewer_id":   "v-test-1",
		"viewer_name": "fzclitui",
	}
	if err := viewer.WriteJSON(unfocusMsg); err != nil {
		t.Fatalf("viewer write unfocus failed: %v", err)
	}

	// Host reads the unfocus control message and handles it like host.go does
	hostCtrl, err := readSealed(host, 2*time.Second)
	if err != nil {
		t.Fatalf("host read unfocus control message failed: %v", err)
	}
	if hostCtrl["action"] != "unfocus" {
		t.Fatalf("expected host to receive unfocus, got: %+v", hostCtrl)
	}

	// Host responds with presence broadcast having Lobby (255)
	presenceBroad := map[string]interface{}{
		"type":   "control",
		"action": "presence",
		"tabs": map[string][]string{
			"255": {"fzclitui"},
		},
	}
	if err := host.WriteJSON(presenceBroad); err != nil {
		t.Fatalf("host broadcast presence failed: %v", err)
	}

	// Viewer receives presence broadcast with Lobby (255)
	viewerPres, err := readSealed(viewer, 2*time.Second)
	if err != nil {
		t.Fatalf("viewer read presence broadcast failed: %v", err)
	}
	if viewerPres["action"] != "presence" {
		t.Fatalf("expected presence action, got: %+v", viewerPres)
	}
	tabs, ok := viewerPres["tabs"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected tabs map in presence, got: %+v", viewerPres["tabs"])
	}
	lobbyUsers, ok := tabs["255"].([]interface{})
	if !ok || len(lobbyUsers) != 1 || lobbyUsers[0] != "fzclitui" {
		t.Fatalf("expected fzclitui in Lobby (255), got: %+v", tabs["255"])
	}

	// 4. Viewer now joins Tab 0 (set_focus)
	setFocusMsg := map[string]interface{}{
		"type":        "control",
		"action":      "set_focus",
		"viewer_id":   "v-test-1",
		"viewer_name": "fzclitui",
		"tab_id":      0,
	}
	if err := viewer.WriteJSON(setFocusMsg); err != nil {
		t.Fatalf("viewer write set_focus failed: %v", err)
	}

	if hostCtrl, err = readSealed(host, 2*time.Second); err != nil {
		t.Fatalf("host read set_focus failed: %v", err)
	}
	if hostCtrl["action"] != "set_focus" {
		t.Fatalf("expected host to receive set_focus, got: %+v", hostCtrl)
	}

	// Host broadcasts updated presence on Tab 0
	presenceTab0 := map[string]interface{}{
		"type":   "control",
		"action": "presence",
		"tabs": map[string][]string{
			"0": {"fzclitui"},
		},
	}
	if err := host.WriteJSON(presenceTab0); err != nil {
		t.Fatalf("host broadcast presence tab 0 failed: %v", err)
	}

	if viewerPres, err = readSealed(viewer, 2*time.Second); err != nil {
		t.Fatalf("viewer read presence tab 0 failed: %v", err)
	}
	tabs0, _ := viewerPres["tabs"].(map[string]interface{})
	tab0Users, _ := tabs0["0"].([]interface{})
	if len(tab0Users) != 1 || tab0Users[0] != "fzclitui" {
		t.Fatalf("expected fzclitui on Tab 0, got: %+v", tabs0["0"])
	}
}

func TestAdaptiveBufferClamping(t *testing.T) {
	serverCfg = &ServeConfig{
		Mode:      modeHybrid,
		Port:      0,
		WebPath:   "/",
		WSPath:    "/ws-rmte",
		MaxBuffer: 5,
	}

	sessionMu.Lock()
	sessions = make(map[string]*Session)
	sessionMu.Unlock()

	s := httptest.NewServer(http.HandlerFunc(handleWS))
	defer s.Close()

	wsURL := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := websocket.DefaultDialer

	// Case 1: Host requests 20 MB buffer, server clamps to 5 MB
	conn1, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn1.Close()

	auth1 := map[string]interface{}{
		"type":             "auth",
		"role":             "host",
		"session_id":       "clamp1",
		"auth_token":       "token1",
		"protocol_version": "0.5",
		"buffer":           20,
	}
	if err := conn1.WriteJSON(auth1); err != nil {
		t.Fatalf("write auth failed: %v", err)
	}

	var resp1 map[string]interface{}
	if err := conn1.ReadJSON(&resp1); err != nil {
		t.Fatalf("read auth resp failed: %v", err)
	}
	if resp1["type"] != "auth_success" {
		t.Fatalf("expected auth_success, got: %+v", resp1)
	}
	clamped1, ok := resp1["clamped_buffer"].(float64)
	if !ok || int(clamped1) != 5 {
		t.Fatalf("expected clamped_buffer 5, got: %v", resp1["clamped_buffer"])
	}

	// Case 2: Host requests 3 MB buffer (< MaxBuffer 5), server allows without clamping (clamped_buffer = 0)
	conn2, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial 2 failed: %v", err)
	}
	defer conn2.Close()

	auth2 := map[string]interface{}{
		"type":             "auth",
		"role":             "host",
		"session_id":       "clamp2",
		"auth_token":       "token2",
		"protocol_version": "0.5",
		"buffer":           3,
	}
	if err := conn2.WriteJSON(auth2); err != nil {
		t.Fatalf("write auth 2 failed: %v", err)
	}

	var resp2 map[string]interface{}
	if err := conn2.ReadJSON(&resp2); err != nil {
		t.Fatalf("read auth resp 2 failed: %v", err)
	}
	if resp2["type"] != "auth_success" {
		t.Fatalf("expected auth_success, got: %+v", resp2)
	}
	clamped2, _ := resp2["clamped_buffer"].(float64)
	if int(clamped2) != 0 {
		t.Fatalf("expected clamped_buffer 0, got: %v", resp2["clamped_buffer"])
	}
}

func TestSessionCapacityLimitAndReconnect(t *testing.T) {
	serverCfg = &ServeConfig{
		Mode:        modeHybrid,
		Port:        0,
		WebPath:     "/",
		WSPath:      "/ws-rmte",
		MaxSessions: 2,
		MaxBuffer:   0,
	}

	sessionMu.Lock()
	sessions = make(map[string]*Session)
	sessionMu.Unlock()

	s := httptest.NewServer(http.HandlerFunc(handleWS))
	defer s.Close()

	wsURL := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := websocket.DefaultDialer

	// Connect Session 1
	conn1, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("conn 1 dial failed: %v", err)
	}
	defer conn1.Close()

	if err := conn1.WriteJSON(map[string]interface{}{
		"type":             "auth",
		"role":             "host",
		"session_id":       "sess1",
		"auth_token":       "token1",
		"protocol_version": "0.5",
	}); err != nil {
		t.Fatalf("conn 1 write auth failed: %v", err)
	}
	var resp1 map[string]interface{}
	if err := conn1.ReadJSON(&resp1); err != nil || resp1["type"] != "auth_success" {
		t.Fatalf("conn 1 auth failed: %v, resp: %+v", err, resp1)
	}

	// Connect Session 2 (relay now at capacity 2/2)
	conn2, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("conn 2 dial failed: %v", err)
	}
	defer conn2.Close()

	if err := conn2.WriteJSON(map[string]interface{}{
		"type":             "auth",
		"role":             "host",
		"session_id":       "sess2",
		"auth_token":       "token2",
		"protocol_version": "0.5",
	}); err != nil {
		t.Fatalf("conn 2 write auth failed: %v", err)
	}
	var resp2 map[string]interface{}
	if err := conn2.ReadJSON(&resp2); err != nil || resp2["type"] != "auth_success" {
		t.Fatalf("conn 2 auth failed: %v, resp: %+v", err, resp2)
	}

	// Connect Session 3 (NEW session -> MUST be rejected)
	conn3, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("conn 3 dial failed: %v", err)
	}
	defer conn3.Close()

	if err := conn3.WriteJSON(map[string]interface{}{
		"type":             "auth",
		"role":             "host",
		"session_id":       "sess3",
		"auth_token":       "token3",
		"protocol_version": "0.5",
	}); err != nil {
		t.Fatalf("conn 3 write auth failed: %v", err)
	}
	var resp3 map[string]interface{}
	if err := conn3.ReadJSON(&resp3); err != nil {
		t.Fatalf("conn 3 read failed: %v", err)
	}
	if resp3["type"] != "error" || !strings.Contains(resp3["message"].(string), "relay session capacity reached") {
		t.Fatalf("expected capacity error, got: %+v", resp3)
	}

	// Reconnect Session 1 while server is FULL (2/2 sessions active).
	// Host 1 reconnects with matching auth_token -> MUST BE ALLOWED!
	reconn1, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("reconn 1 dial failed: %v", err)
	}
	defer reconn1.Close()

	if err := reconn1.WriteJSON(map[string]interface{}{
		"type":             "auth",
		"role":             "host",
		"session_id":       "sess1",
		"auth_token":       "token1",
		"protocol_version": "0.5",
	}); err != nil {
		t.Fatalf("reconn 1 write auth failed: %v", err)
	}
	var reconnResp map[string]interface{}
	if err := reconn1.ReadJSON(&reconnResp); err != nil {
		t.Fatalf("reconn 1 read failed: %v", err)
	}
	if reconnResp["type"] != "auth_success" {
		t.Fatalf("expected reconnect auth_success while server is full, got: %+v", reconnResp)
	}
}

func TestViewerConnectionLimitTotalConns(t *testing.T) {
	serverCfg = &ServeConfig{
		Mode:       modeHybrid,
		Port:       0,
		WebPath:    "/",
		WSPath:     "/ws-rmte",
		MaxViewers: 2, // Max 2 active connections total
	}

	sessionMu.Lock()
	sessions = make(map[string]*Session)
	sessionMu.Unlock()

	s := httptest.NewServer(http.HandlerFunc(handleWS))
	defer s.Close()

	wsURL := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := websocket.DefaultDialer

	// Connect Host
	hConn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("host dial failed: %v", err)
	}
	defer hConn.Close()

	if err := hConn.WriteJSON(map[string]interface{}{
		"type":             "auth",
		"role":             "host",
		"session_id":       "vlimit",
		"auth_token":       "vpass",
		"protocol_version": "0.5",
	}); err != nil {
		t.Fatalf("host write auth failed: %v", err)
	}
	var hResp map[string]interface{}
	if err := hConn.ReadJSON(&hResp); err != nil || hResp["type"] != "auth_success" {
		t.Fatalf("host auth failed: %v, resp: %+v", err, hResp)
	}

	// Viewer Connection 1 (Viewer "userA" tab 1)
	vConn1, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("viewer 1 dial failed: %v", err)
	}
	defer vConn1.Close()

	if err := vConn1.WriteJSON(map[string]interface{}{
		"type":             "auth",
		"role":             "viewer",
		"session_id":       "vlimit",
		"viewer_id":        "userA",
		"auth_token":       "vpass",
		"protocol_version": "0.5",
	}); err != nil {
		t.Fatalf("viewer 1 auth write failed: %v", err)
	}
	var vResp1 map[string]interface{}
	if err := vConn1.ReadJSON(&vResp1); err != nil || vResp1["type"] != "auth_success" {
		t.Fatalf("viewer 1 auth failed: %v, resp: %+v", err, vResp1)
	}

	// Viewer Connection 2 (SAME Viewer "userA" tab 2) -> Total conns = 2
	vConn2, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("viewer 2 dial failed: %v", err)
	}
	defer vConn2.Close()

	if err := vConn2.WriteJSON(map[string]interface{}{
		"type":             "auth",
		"role":             "viewer",
		"session_id":       "vlimit",
		"viewer_id":        "userA",
		"auth_token":       "vpass",
		"protocol_version": "0.5",
	}); err != nil {
		t.Fatalf("viewer 2 auth write failed: %v", err)
	}
	var vResp2 map[string]interface{}
	if err := vConn2.ReadJSON(&vResp2); err != nil || vResp2["type"] != "auth_success" {
		t.Fatalf("viewer 2 auth failed: %v, resp: %+v", err, vResp2)
	}

	// Viewer Connection 3 (Viewer "userB" or tab 3) -> Exceeds MaxViewers=2!
	vConn3, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("viewer 3 dial failed: %v", err)
	}
	defer vConn3.Close()

	if err := vConn3.WriteJSON(map[string]interface{}{
		"type":             "auth",
		"role":             "viewer",
		"session_id":       "vlimit",
		"viewer_id":        "userB",
		"auth_token":       "vpass",
		"protocol_version": "0.5",
	}); err != nil {
		t.Fatalf("viewer 3 auth write failed: %v", err)
	}
	var vResp3 map[string]interface{}
	if err := vConn3.ReadJSON(&vResp3); err != nil {
		t.Fatalf("viewer 3 read failed: %v", err)
	}
	if vResp3["type"] != "error" || vResp3["message"] != "session full" {
		t.Fatalf("expected session full error, got: %+v", vResp3)
	}
}

func TestSetReadLimitEnforcement(t *testing.T) {
	serverCfg = &ServeConfig{
		Mode:      modeHybrid,
		Port:      0,
		WebPath:   "/",
		WSPath:    "/ws-rmte",
		MaxBuffer: 1, // ReadLimit = (1 + 1) * 1024 * 1024 = 2 MB
	}

	sessionMu.Lock()
	sessions = make(map[string]*Session)
	sessionMu.Unlock()

	s := httptest.NewServer(http.HandlerFunc(handleWS))
	defer s.Close()

	wsURL := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := websocket.DefaultDialer

	// Connect Host
	hConn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("host dial failed: %v", err)
	}
	defer hConn.Close()

	if err := hConn.WriteJSON(map[string]interface{}{
		"type":             "auth",
		"role":             "host",
		"session_id":       "limitcheck",
		"auth_token":       "pass",
		"protocol_version": "0.5",
	}); err != nil {
		t.Fatalf("host write auth failed: %v", err)
	}
	var hResp map[string]interface{}
	if err := hConn.ReadJSON(&hResp); err != nil || hResp["type"] != "auth_success" {
		t.Fatalf("host auth failed: %v, resp: %+v", err, hResp)
	}

	// Connect Viewer
	vConn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("viewer dial failed: %v", err)
	}
	defer vConn.Close()

	if err := vConn.WriteJSON(map[string]interface{}{
		"type":             "auth",
		"role":             "viewer",
		"session_id":       "limitcheck",
		"viewer_id":        "attacker",
		"auth_token":       "pass",
		"protocol_version": "0.5",
	}); err != nil {
		t.Fatalf("viewer auth write failed: %v", err)
	}
	var vResp map[string]interface{}
	if err := vConn.ReadJSON(&vResp); err != nil || vResp["type"] != "auth_success" {
		t.Fatalf("viewer auth failed: %v, resp: %+v", err, vResp)
	}

	// Send an oversized binary frame from Viewer (2.5 MB, which exceeds 2 MB limit)
	oversized := make([]byte, int(2.5*1024*1024))
	err = vConn.WriteMessage(websocket.BinaryMessage, oversized)
	if err != nil {
		// Socket was closed immediately, test passes
		return
	}

	// Attempting to read should now fail because the connection was killed by relay's SetReadLimit
	_ = vConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, rerr := vConn.ReadMessage()
	if rerr == nil {
		t.Fatalf("expected read error due to SetReadLimit enforcement, but read succeeded")
	}
}

func TestViewerConnectionUnlimitedWhenZero(t *testing.T) {
	serverCfg = &ServeConfig{
		Mode:       modeHybrid,
		Port:       0,
		WebPath:    "/",
		WSPath:     "/ws-rmte",
		MaxViewers: 0, // 0 = unlimited
	}

	sessionMu.Lock()
	sessions = make(map[string]*Session)
	sessionMu.Unlock()

	s := httptest.NewServer(http.HandlerFunc(handleWS))
	defer s.Close()

	wsURL := "ws" + strings.TrimPrefix(s.URL, "http")
	dialer := websocket.DefaultDialer

	// Connect Host
	hConn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("host dial failed: %v", err)
	}
	defer hConn.Close()

	if err := hConn.WriteJSON(map[string]interface{}{
		"type":             "auth",
		"role":             "host",
		"session_id":       "vzero",
		"auth_token":       "vpass",
		"protocol_version": "0.5",
	}); err != nil {
		t.Fatalf("host write auth failed: %v", err)
	}
	var hResp map[string]interface{}
	if err := hConn.ReadJSON(&hResp); err != nil || hResp["type"] != "auth_success" {
		t.Fatalf("host auth failed: %v, resp: %+v", err, hResp)
	}

	// Connect 3 Viewers - all should succeed without being capped
	for i := 1; i <= 3; i++ {
		vConn, _, err := dialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("viewer %d dial failed: %v", i, err)
		}
		defer vConn.Close()

		if err := vConn.WriteJSON(map[string]interface{}{
			"type":             "auth",
			"role":             "viewer",
			"session_id":       "vzero",
			"viewer_id":        fmt.Sprintf("user-%d", i),
			"auth_token":       "vpass",
			"protocol_version": "0.5",
		}); err != nil {
			t.Fatalf("viewer %d auth write failed: %v", i, err)
		}
		var vResp map[string]interface{}
		if err := vConn.ReadJSON(&vResp); err != nil || vResp["type"] != "auth_success" {
			t.Fatalf("viewer %d expected auth_success with MaxViewers=0 (unlimited), got: %+v", i, vResp)
		}
	}

	// Connect 4th connection from SAME viewer "user-1" (multi-tab scenario with MaxViewers=0)
	vConnMulti, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("viewer multi-tab dial failed: %v", err)
	}
	defer vConnMulti.Close()

	if err := vConnMulti.WriteJSON(map[string]interface{}{
		"type":             "auth",
		"role":             "viewer",
		"session_id":       "vzero",
		"viewer_id":        "user-1",
		"auth_token":       "vpass",
		"protocol_version": "0.5",
	}); err != nil {
		t.Fatalf("viewer multi-tab auth write failed: %v", err)
	}
	var vRespMulti map[string]interface{}
	if err := vConnMulti.ReadJSON(&vRespMulti); err != nil || vRespMulti["type"] != "auth_success" {
		t.Fatalf("viewer multi-tab expected auth_success with MaxViewers=0, got: %+v", vRespMulti)
	}
}

func TestHostManagedChatHistory(t *testing.T) {
	serverCfg = &ServeConfig{
		Mode:    modeHybrid,
		Port:    0,
		WebPath: "/",
		WSPath:  "/ws-rmte",
	}

	sessionMu.Lock()
	sessions = make(map[string]*Session)
	sessionMu.Unlock()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleWS(w, r)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws-rmte"
	dialer := websocket.Dialer{}

	// 1. Connect Host
	hostConn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("host dial failed: %v", err)
	}
	defer hostConn.Close()

	if err := hostConn.WriteJSON(map[string]interface{}{
		"type":             "auth",
		"role":             "host",
		"session_id":       "chathost",
		"auth_token":       "testpass",
		"protocol_version": "0.5",
	}); err != nil {
		t.Fatalf("host auth write failed: %v", err)
	}
	var hResp map[string]interface{}
	if err := hostConn.ReadJSON(&hResp); err != nil || hResp["type"] != "auth_success" {
		t.Fatalf("host auth failed: %v, resp: %+v", err, hResp)
	}

	// 2. Connect Viewer 1
	vConn1, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("viewer 1 dial failed: %v", err)
	}
	defer vConn1.Close()

	if err := vConn1.WriteJSON(map[string]interface{}{
		"type":             "auth",
		"role":             "viewer",
		"session_id":       "chathost",
		"viewer_id":        "v1",
		"auth_token":       "testpass",
		"protocol_version": "0.5",
	}); err != nil {
		t.Fatalf("viewer 1 auth failed: %v", err)
	}
	var vResp1 map[string]interface{}
	if err := vConn1.ReadJSON(&vResp1); err != nil || vResp1["type"] != "auth_success" {
		t.Fatalf("viewer 1 auth failed: %v", err)
	}

	if err := setupCrypto("testpass"); err != nil {
		t.Fatal(err)
	}
	host := sealPeer(hostConn, "chathost", true)
	v1 := sealPeer(vConn1, "chathost", false)
	linkEpoch(host, v1)

	// Viewer 1 sends a chat message
	testChat := map[string]interface{}{
		"type":    "control",
		"action":  "chat",
		"sender":  "Alice",
		"message": "Hello from Alice",
		"time":    "12:00",
	}
	if err := v1.WriteJSON(testChat); err != nil {
		t.Fatalf("viewer 1 send chat failed: %v", err)
	}

	// Host should receive the sealed chat message from relay
	hostChatMsg, err := readSealed(host, 2*time.Second)
	if err != nil {
		t.Fatalf("host expected chat message forwarded by relay, got: %v", err)
	}
	if hostChatMsg["action"] != "chat" || hostChatMsg["message"] != "Hello from Alice" {
		t.Fatalf("host unexpected chat message: %+v", hostChatMsg)
	}

	// 3. Connect Viewer 2 and request chat history
	vConn2, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("viewer 2 dial failed: %v", err)
	}
	defer vConn2.Close()

	if err := vConn2.WriteJSON(map[string]interface{}{
		"type":             "auth",
		"role":             "viewer",
		"session_id":       "chathost",
		"viewer_id":        "v2",
		"auth_token":       "testpass",
		"protocol_version": "0.5",
	}); err != nil {
		t.Fatalf("viewer 2 auth failed: %v", err)
	}
	var vResp2 map[string]interface{}
	if err := vConn2.ReadJSON(&vResp2); err != nil || vResp2["type"] != "auth_success" {
		t.Fatalf("viewer 2 auth failed: %v", err)
	}
	v2 := sealPeer(vConn2, "chathost", false)
	linkEpoch(host, v2)

	// Viewer 2 requests chat history
	if err := v2.WriteJSON(map[string]interface{}{
		"type":   "control",
		"action": "get_chat_history",
	}); err != nil {
		t.Fatalf("viewer 2 get_chat_history failed: %v", err)
	}

	// Host receives get_chat_history with target_conn injected by relay
	hostGetChat, err := readSealed(host, 2*time.Second)
	if err != nil {
		t.Fatalf("host expected get_chat_history, got: %v", err)
	}
	if hostGetChat["action"] != "get_chat_history" {
		t.Fatalf("expected action get_chat_history, got: %+v", hostGetChat)
	}
	targetConn, ok := hostGetChat["target_conn"].(string)
	if !ok || targetConn == "" {
		t.Fatalf("relay failed to inject target_conn into get_chat_history: %+v", hostGetChat)
	}

	// Host sends chat_history back to Viewer 2 via target_conn
	if err := host.WriteJSON(map[string]interface{}{
		"type":        "control",
		"action":      "chat_history",
		"target_conn": targetConn,
		"history": []map[string]interface{}{
			{
				"sender":  "Alice",
				"message": "Hello from Alice",
				"time":    "12:00",
			},
		},
	}); err != nil {
		t.Fatalf("host send chat_history failed: %v", err)
	}

	// Viewer 2 receives chat_history!
	v2ChatHistory, err := readSealed(v2, 2*time.Second)
	if err != nil {
		t.Fatalf("viewer 2 failed to read chat_history: %v", err)
	}
	if v2ChatHistory["action"] != "chat_history" {
		t.Fatalf("viewer 2 expected action chat_history, got: %+v", v2ChatHistory)
	}
	hist, _ := v2ChatHistory["history"].([]interface{})
	if len(hist) != 1 {
		t.Fatalf("viewer 2 expected 1 history item, got: %d", len(hist))
	}
}

func TestChatNoDuplicateDelivery(t *testing.T) {
	serverCfg = &ServeConfig{
		Mode:    modeHybrid,
		Port:    0,
		WebPath: "/",
		WSPath:  "/ws-rmte",
	}

	sessionMu.Lock()
	sessions = make(map[string]*Session)
	sessionMu.Unlock()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleWS(w, r)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws-rmte"
	dialer := websocket.Dialer{}

	// 1. Host
	hostConn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("host dial failed: %v", err)
	}
	defer hostConn.Close()

	if err := hostConn.WriteJSON(map[string]interface{}{
		"type":             "auth",
		"role":             "host",
		"session_id":       "nodup",
		"auth_token":       "noduppass",
		"protocol_version": "0.5",
	}); err != nil {
		t.Fatalf("host auth failed: %v", err)
	}
	var hResp map[string]interface{}
	if err := hostConn.ReadJSON(&hResp); err != nil || hResp["type"] != "auth_success" {
		t.Fatalf("host auth failed: %v", err)
	}

	// 2. Viewer 1
	vConn1, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("v1 dial failed: %v", err)
	}
	defer vConn1.Close()
	if err := vConn1.WriteJSON(map[string]interface{}{
		"type":             "auth",
		"role":             "viewer",
		"session_id":       "nodup",
		"viewer_id":        "v1",
		"auth_token":       "noduppass",
		"protocol_version": "0.5",
	}); err != nil {
		t.Fatalf("v1 auth failed: %v", err)
	}
	var vResp1 map[string]interface{}
	if err := vConn1.ReadJSON(&vResp1); err != nil || vResp1["type"] != "auth_success" {
		t.Fatalf("v1 auth failed: %v", err)
	}

	// 3. Viewer 2
	vConn2, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("v2 dial failed: %v", err)
	}
	defer vConn2.Close()
	if err := vConn2.WriteJSON(map[string]interface{}{
		"type":             "auth",
		"role":             "viewer",
		"session_id":       "nodup",
		"viewer_id":        "v2",
		"auth_token":       "noduppass",
		"protocol_version": "0.5",
	}); err != nil {
		t.Fatalf("v2 auth failed: %v", err)
	}
	var vResp2 map[string]interface{}
	if err := vConn2.ReadJSON(&vResp2); err != nil || vResp2["type"] != "auth_success" {
		t.Fatalf("v2 auth failed: %v", err)
	}

	if err := setupCrypto("noduppass"); err != nil {
		t.Fatal(err)
	}
	host := sealPeer(hostConn, "nodup", true)
	v1 := sealPeer(vConn1, "nodup", false)
	v2 := sealPeer(vConn2, "nodup", false)
	linkEpoch(host, v1, v2)

	// Viewer 1 sends a single chat message
	testMsg := map[string]interface{}{
		"type":    "control",
		"action":  "chat",
		"sender":  "v1",
		"message": "Only-Once-Test",
		"time":    "12:34",
	}
	if err := v1.WriteJSON(testMsg); err != nil {
		t.Fatalf("v1 write chat failed: %v", err)
	}

	// The relay cannot read chat, so it must not fan it out itself; the host
	// receives it and broadcasts it once (as host.go does).
	hostChat, err := readSealed(host, 2*time.Second)
	if err != nil || hostChat["action"] != "chat" {
		t.Fatalf("host expected sealed chat, got %+v (%v)", hostChat, err)
	}
	delete(hostChat, "target_conn")
	if err := host.WriteJSON(hostChat); err != nil {
		t.Fatalf("host broadcast chat failed: %v", err)
	}

	// Viewer 2 must receive the chat message EXACTLY ONCE
	v2Msg1, err := readSealed(v2, 1*time.Second)
	if err != nil {
		t.Fatalf("viewer 2 expected to receive chat message: %v", err)
	}
	if v2Msg1["action"] != "chat" || v2Msg1["message"] != "Only-Once-Test" {
		t.Fatalf("viewer 2 received unexpected message: %+v", v2Msg1)
	}

	// Viewer 2 should NOT receive a duplicate delivery
	dupMsg, err := readSealed(v2, 250*time.Millisecond)
	if err == nil {
		t.Fatalf("DUPLICATE CHAT DETECTED: viewer 2 received chat a second time: %+v", dupMsg)
	}
}
