package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xtaci/smux/v2"
)

var refererPrefixRegex = regexp.MustCompile(`^/p/([0-9a-zA-Z_-]+)/(\d{1,5})`)

// handleWSProxy handles the secondary WebSocket connection from the Host for the preview channel.
func handleWSProxy(w http.ResponseWriter, r *http.Request) {
	rawConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	var auth struct {
		Type        string `json:"type"`
		Role        string `json:"role"`
		SessionID   string `json:"session_id"`
		ProxySecret string `json:"proxy_secret"`
	}

	if err := rawConn.ReadJSON(&auth); err != nil || auth.Type != "auth" || auth.Role != "host_proxy" {
		rawConn.Close()
		return
	}

	sessionMu.RLock()
	s, exists := sessions[auth.SessionID]
	sessionMu.RUnlock()

	if !exists || s.ProxySecret == "" || s.ProxySecret != auth.ProxySecret {
		rawConn.WriteJSON(map[string]string{
			"type":    "error",
			"message": "invalid proxy credentials or session not found",
		})
		rawConn.Close()
		return
	}

	rawConn.WriteJSON(map[string]string{
		"type":    "auth_success",
		"message": "host proxy connected",
	})

	netConn := newWSNetConn(rawConn)
	smuxClient, err := smux.Client(netConn, createSmuxConfig())
	if err != nil {
		rawConn.Close()
		return
	}

	s.SmuxMu.Lock()
	if s.SmuxSession != nil {
		s.SmuxSession.Close()
	}
	s.SmuxSession = smuxClient
	s.SmuxMu.Unlock()

	fmt.Printf("[Preview] Host proxy stream channel established for session %s\n", auth.SessionID)

	defer func() {
		s.SmuxMu.Lock()
		if s.SmuxSession == smuxClient {
			s.SmuxSession = nil
		}
		s.SmuxMu.Unlock()
		smuxClient.Close()
		netConn.Close()
		fmt.Printf("[Preview] Host proxy stream channel disconnected for session %s\n", auth.SessionID)
	}()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if smuxClient.IsClosed() {
				return
			}
		}
	}
}

// handlePreviewHTTP routes incoming browser preview requests (/p/<session>/<port>/...).
func handlePreviewHTTP(w http.ResponseWriter, r *http.Request) {
	// Pattern is /p/<session>/<port>[/path...]
	rawPath := strings.TrimPrefix(r.URL.Path, "/p/")
	parts := strings.SplitN(rawPath, "/", 3)

	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "Bad Request: missing session ID in /p/<session>/<port>/", http.StatusBadRequest)
		return
	}

	sessionID := strings.ToLower(parts[0])

	if len(parts) < 2 || parts[1] == "" {
		// Missing port
		http.Error(w, "Bad Request: missing port in /p/<session>/<port>/", http.StatusBadRequest)
		return
	}

	port, err := strconv.Atoi(parts[1])
	if err != nil || port < 1 || port > 65535 {
		http.Error(w, fmt.Sprintf("Bad Request: invalid port %q", parts[1]), http.StatusBadRequest)
		return
	}

	// Ensure trailing slash for root preview URL: /p/<session>/<port> -> /p/<session>/<port>/
	if len(parts) == 2 && !strings.HasSuffix(r.URL.Path, "/") {
		target := fmt.Sprintf("/p/%s/%d/", sessionID, port)
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently)
		return
	}

	sessionMu.RLock()
	s, exists := sessions[sessionID]
	sessionMu.RUnlock()

	if !exists {
		// Try smart referer safety net if asset request leaked
		if tryRefererRedirect(w, r) {
			return
		}
		http.Error(w, fmt.Sprintf("Session %q not found", sessionID), http.StatusNotFound)
		return
	}

	if !s.PreviewEnabled {
		http.Error(w, "Web Preview is disabled on this session by host (--web-preview flag required)", http.StatusForbidden)
		return
	}

	s.SmuxMu.RLock()
	smuxClient := s.SmuxSession
	s.SmuxMu.RUnlock()

	if smuxClient == nil {
		http.Error(w, "Host preview backend is not connected yet", http.StatusServiceUnavailable)
		return
	}

	// Detect WebSocket upgrade (e.g. Vite HMR)
	if strings.ToLower(r.Header.Get("Upgrade")) == "websocket" {
		handlePreviewWebSocketHijack(w, r, smuxClient, sessionID, port)
		return
	}

	// Standard HTTP request
	handlePreviewHTTPRequest(w, r, smuxClient, sessionID, port)
}

func handlePreviewHTTPRequest(w http.ResponseWriter, r *http.Request, smuxClient *smux.Session, sessionID string, port int) {
	stream, err := smuxClient.OpenStream()
	if err != nil {
		http.Error(w, "Failed to open preview stream to host: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer stream.Close()

	// Clone or set necessary forward headers
	clientIP := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		clientIP = host
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}

	r.Header.Del("Forwarded")
	r.Header.Set("X-Forwarded-For", clientIP)
	r.Header.Set("X-Forwarded-Proto", scheme)
	r.Header.Set("X-Forwarded-Host", r.Host)
	r.Header.Set("X-Forwarded-Prefix", fmt.Sprintf("/p/%s/%d", sessionID, port))

	// Write request to host stream
	if err := r.Write(stream); err != nil {
		http.Error(w, "Failed to send request to host: "+err.Error(), http.StatusBadGateway)
		return
	}

	// Read response from host
	resp, err := http.ReadResponse(bufio.NewReader(stream), r)
	if err != nil {
		http.Error(w, "Failed to read response from host: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy headers to client response
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func handlePreviewWebSocketHijack(w http.ResponseWriter, r *http.Request, smuxClient *smux.Session, sessionID string, port int) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Webserver does not support hijacking", http.StatusInternalServerError)
		return
	}

	clientConn, bufrw, err := hj.Hijack()
	if err != nil {
		http.Error(w, "Failed to hijack connection: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer clientConn.Close()

	stream, err := smuxClient.OpenStream()
	if err != nil {
		clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}
	defer stream.Close()

	// Write the raw upgrade request to host stream
	if err := r.Write(stream); err != nil {
		return
	}

	// Full-duplex raw pipe
	errc := make(chan error, 2)
	go func() {
		_, err := io.Copy(clientConn, stream)
		errc <- err
	}()
	go func() {
		// Read any buffered data first, then clientConn
		_, err := io.Copy(stream, bufrw)
		errc <- err
	}()

	<-errc
}

// tryRefererRedirect checks if an asset leaked to root and redirects it to the correct /p/<session>/<port>/...
func tryRefererRedirect(w http.ResponseWriter, r *http.Request) bool {
	ref := r.Header.Get("Referer")
	if ref == "" {
		return false
	}
	// Parse referer path
	match := refererPrefixRegex.FindStringSubmatch(ref)
	if len(match) < 3 {
		// Try parsing URL if full URL
		idx := strings.Index(ref, "/p/")
		if idx != -1 {
			match = refererPrefixRegex.FindStringSubmatch(ref[idx:])
		}
	}
	if len(match) >= 3 {
		sessionID := match[1]
		portStr := match[2]
		target := fmt.Sprintf("/p/%s/%s%s", sessionID, portStr, r.URL.Path)
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		w.Header().Set("Cache-Control", "no-store")
		status := http.StatusFound // 302
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			status = http.StatusTemporaryRedirect // 307
		}
		http.Redirect(w, r, target, status)
		return true
	}
	return false
}
