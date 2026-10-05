package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDialPreviewValidation(t *testing.T) {
	// Invalid port ranges
	if _, err := dialPreview(0); err == nil {
		t.Fatal("expected error for port 0")
	}
	if _, err := dialPreview(-10); err == nil {
		t.Fatal("expected error for negative port")
	}
	if _, err := dialPreview(70000); err == nil {
		t.Fatal("expected error for port > 65535")
	}

	// Anti-self-loop
	serverCfg = &ServeConfig{Port: 8048}
	defer func() { serverCfg = nil }()
	if _, err := dialPreview(8048); err == nil {
		t.Fatal("expected anti-self-loop error when dialing relay port")
	}
}

func TestDialPreviewConnect(t *testing.T) {
	// Start a local TCP listener on loopback
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port

	go func() {
		conn, err := ln.Accept()
		if err == nil {
			conn.Write([]byte("PONG"))
			conn.Close()
		}
	}()

	clientConn, err := dialPreview(port)
	if err != nil {
		t.Fatalf("failed to dialPreview(%d): %v", port, err)
	}
	defer clientConn.Close()

	buf := make([]byte, 4)
	n, err := io.ReadFull(clientConn, buf)
	if err != nil || string(buf[:n]) != "PONG" {
		t.Fatalf("unexpected read: %s, %v", string(buf[:n]), err)
	}
}

func TestFilterInternalCookies(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	req.Header.Set("Cookie", "rmte_sess=secret123; user_auth=validToken; rmte_pvw_main_8080=sig; theme=dark")

	filterInternalCookies(req)

	filtered := req.Header.Get("Cookie")
	if strings.Contains(filtered, "rmte_sess") || strings.Contains(filtered, "rmte_pvw") {
		t.Fatalf("internal cookies were not filtered: %q", filtered)
	}
	if !strings.Contains(filtered, "user_auth=validToken") || !strings.Contains(filtered, "theme=dark") {
		t.Fatalf("legitimate user cookies were removed: %q", filtered)
	}
}

func TestPreviewTicketAndCookie(t *testing.T) {
	if err := setupCrypto("testpassword123"); err != nil {
		t.Fatal(err)
	}

	sessionID := "sess999"
	port := 3000
	expiresAt := time.Now().Add(60 * time.Second).Unix()
	nonce := "random123"

	ticket := generatePreviewTicket(sessionID, port, expiresAt, nonce)
	if ticket == "" {
		t.Fatal("ticket generation returned empty string")
	}

	// Valid ticket
	if !validatePreviewTicket(sessionID, port, ticket) {
		t.Fatal("valid ticket failed validation")
	}

	// Wrong port
	if validatePreviewTicket(sessionID, 3001, ticket) {
		t.Fatal("ticket validated against wrong port")
	}

	// Wrong session
	if validatePreviewTicket("other_sess", port, ticket) {
		t.Fatal("ticket validated against wrong session")
	}

	// Expired ticket
	expiredTicket := generatePreviewTicket(sessionID, port, time.Now().Add(-10*time.Second).Unix(), nonce)
	if validatePreviewTicket(sessionID, port, expiredTicket) {
		t.Fatal("expired ticket passed validation")
	}

	// Cookie validation
	cookieVal := generatePreviewCookieValue(sessionID, port)
	if !validatePreviewCookie(sessionID, port, cookieVal) {
		t.Fatal("valid cookie value failed validation")
	}
	if validatePreviewCookie(sessionID, port, "tampered_value") {
		t.Fatal("tampered cookie passed validation")
	}
}

func TestPreviewEndToEndIntegration(t *testing.T) {
	// 1. Start a local mock HTTP server that simulates the user's dev server (e.g. Bun / Vite / Dufs)
	localMux := http.NewServeMux()
	localMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<!DOCTYPE html><html><head><title>App</title></head><body><h1>Hello RMTE Preview</h1></body></html>`))
	})
	localMux.HandleFunc("/api/data", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	localLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer localLn.Close()
	localPort := localLn.Addr().(*net.TCPAddr).Port
	go http.Serve(localLn, localMux)

	// 2. Start an in-process Relay server
	relayPortLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	relayPort := relayPortLn.Addr().(*net.TCPAddr).Port
	relayPortLn.Close()

	serveCfg := &ServeConfig{
		Mode:          modeHybrid,
		Port:          relayPort,
		Pass:          "previewpass",
		WSPath:        "/ws-rmte",
		WebPath:       "/",
		Preview:       true,
		InternalToken: "internal123",
	}

	go runServer(serveCfg)

	// Poll until relay is ready
	for i := 0; i < 50; i++ {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", relayPort), 50*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 3. Connect Host with Preview enabled
	hostOpts := HostOptions{
		DialURL:       fmt.Sprintf("ws://127.0.0.1:%d/ws-rmte", relayPort),
		PublicURL:     fmt.Sprintf("ws://127.0.0.1:%d/ws-rmte", relayPort),
		Pass:          "previewpass",
		Buffer:        1,
		Mode:          modeHybrid,
		ID:            "testsess",
		Preview:       true,
		InternalToken: "internal123",
	}

	go runHost(hostOpts)

	// Wait for host & proxy channel to connect
	var sess *Session
	for i := 0; i < 50; i++ {
		sessionMu.RLock()
		s, ok := sessions["testsess"]
		sessionMu.RUnlock()
		if ok {
			s.SmuxMu.RLock()
			smuxConnected := s.SmuxSession != nil
			s.SmuxMu.RUnlock()
			if smuxConnected {
				sess = s
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	if sess == nil {
		t.Fatal("host session or smux proxy connection timed out")
	}

	// 4. Test accessing preview without ticket/cookie -> should be 403 Forbidden
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // do not follow redirect automatically
		},
	}

	targetURL := fmt.Sprintf("http://127.0.0.1:%d/p/testsess/%d/", relayPort, localPort)
	resp, err := client.Get(targetURL)
	if err != nil {
		t.Fatalf("GET /p/... failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden without ticket, got %d", resp.StatusCode)
	}

	// 5. Generate ticket and test ticket bootstrap -> should be 302 Found with Set-Cookie
	ticket := generatePreviewTicket("testsess", localPort, time.Now().Add(60*time.Second).Unix(), "nonce99")
	ticketURL := fmt.Sprintf("http://127.0.0.1:%d/p/testsess/%d/?ticket=%s", relayPort, localPort, ticket)
	respTicket, err := client.Get(ticketURL)
	if err != nil {
		t.Fatalf("GET /p/... with ticket failed: %v", err)
	}
	respTicket.Body.Close()
	if respTicket.StatusCode != http.StatusFound {
		t.Fatalf("expected 302 Found for ticket bootstrap, got %d", respTicket.StatusCode)
	}

	// Check that cookie was set in jar
	u, _ := url.Parse(targetURL)
	cookies := jar.Cookies(u)
	foundCookie := false
	expectedCookieName := previewCookieName("testsess", localPort)
	for _, c := range cookies {
		if c.Name == expectedCookieName {
			foundCookie = true
			break
		}
	}
	if !foundCookie {
		t.Fatalf("expected cookie %q to be set in jar, got %v", expectedCookieName, cookies)
	}

	// 6. Make subsequent request using the cookie -> should be 200 OK with HTML shim injected
	respApp, err := client.Get(targetURL)
	if err != nil {
		t.Fatalf("GET with cookie failed: %v", err)
	}
	defer respApp.Body.Close()
	if respApp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", respApp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(respApp.Body)
	if err != nil {
		t.Fatal(err)
	}
	bodyStr := string(bodyBytes)

	if !strings.Contains(bodyStr, "Hello RMTE Preview") {
		t.Fatalf("body does not contain original content: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, `<script id="rmte-preview-shim">`) {
		t.Fatalf("body does not contain injected shim script: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, fmt.Sprintf(`<base href="/p/testsess/%d/">`, localPort)) {
		t.Fatalf("body does not contain injected base href: %s", bodyStr)
	}

	// 7. Test Friendly Error View when querying an offline port
	offlinePort := 59876
	offlineTicket := generatePreviewTicket("testsess", offlinePort, time.Now().Add(60*time.Second).Unix(), "nonce_offline")
	// Bootstrap ticket for offline port
	respOfflineTicket, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/p/testsess/%d/?ticket=%s", relayPort, offlinePort, offlineTicket))
	if err == nil {
		respOfflineTicket.Body.Close()
	}

	reqOffline, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/p/testsess/%d/", relayPort, offlinePort), nil)
	reqOffline.Header.Set("Accept", "text/html")
	respOffline, err := client.Do(reqOffline)
	if err != nil {
		t.Fatalf("offline port request failed: %v", err)
	}
	defer respOffline.Body.Close()

	offlineBody, _ := io.ReadAll(respOffline.Body)
	offlineStr := string(offlineBody)
	if !strings.Contains(offlineStr, fmt.Sprintf("Port %d is not listening", offlinePort)) {
		t.Fatalf("expected friendly error view for offline port, got: %s", offlineStr)
	}
}
