package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xtaci/smux/v2"
)

var (
	hostProxySecretMu sync.RWMutex
	hostProxySecret   string
)

func setHostProxySecret(sec string) {
	hostProxySecretMu.Lock()
	hostProxySecret = sec
	hostProxySecretMu.Unlock()
}

func getHostProxySecret() string {
	hostProxySecretMu.RLock()
	defer hostProxySecretMu.RUnlock()
	return hostProxySecret
}

func resetHostProxySecret() {
	hostProxySecretMu.Lock()
	hostProxySecret = ""
	hostProxySecretMu.Unlock()
}

// runHostProxy manages the outbound WebSocket connection to the relay for the preview data channel.
func runHostProxy(opts HostOptions, sessionID, proxySecret, wsProxyPath string) {
	setHostProxySecret(proxySecret)

	u, err := url.Parse(opts.DialURL)
	if err != nil {
		logEvent(nil, "PREVIEW_ERROR", "host", fmt.Sprintf("Invalid dial URL for proxy: %v", err))
		return
	}
	u.Path = wsProxyPath

	for {
		rawConn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
		if err != nil {
			time.Sleep(3 * time.Second)
			continue
		}

		currentSecret := getHostProxySecret()
		auth := map[string]string{
			"type":         "auth",
			"role":         "host_proxy",
			"session_id":   sessionID,
			"proxy_secret": currentSecret,
		}
		if err := rawConn.WriteJSON(auth); err != nil {
			rawConn.Close()
			time.Sleep(3 * time.Second)
			continue
		}

		var authResp struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if err := rawConn.ReadJSON(&authResp); err != nil || authResp.Type != "auth_success" {
			rawConn.Close()
			time.Sleep(3 * time.Second)
			continue
		}

		netConn := newWSNetConn(rawConn)
		smuxServer, err := smux.Server(netConn, createSmuxConfig())
		if err != nil {
			rawConn.Close()
			time.Sleep(3 * time.Second)
			continue
		}

		logEvent(nil, "PREVIEW_READY", "host", fmt.Sprintf("Preview proxy established for session %s", sessionID))

		// Accept incoming preview streams
		for {
			stream, err := smuxServer.AcceptStream()
			if err != nil {
				break
			}
			go handleHostPreviewStream(stream, sessionID)
		}

		smuxServer.Close()
		netConn.Close()
		time.Sleep(2 * time.Second)
	}
}

func handleHostPreviewStream(stream *smux.Stream, sessionID string) {
	defer stream.Close()

	reader := bufio.NewReader(stream)
	req, err := http.ReadRequest(reader)
	if err != nil {
		return
	}

	// Parse /p/<session>/<port>[/subpath...]
	rawPath := strings.TrimPrefix(req.URL.Path, "/p/")
	parts := strings.SplitN(rawPath, "/", 3)
	if len(parts) < 2 {
		return
	}
	targetPort, err := strconv.Atoi(parts[1])
	if err != nil || targetPort < 1 || targetPort > 65535 {
		return
	}

	subPath := "/"
	if len(parts) == 3 {
		subPath = "/" + parts[2]
	}

	// 1. Ticket validation
	ticket := req.URL.Query().Get("ticket")
	if ticket != "" {
		if validatePreviewTicket(sessionID, targetPort, ticket) {
			// Strip ticket from URL and issue session cookie
			q := req.URL.Query()
			q.Del("ticket")
			cleanURL := req.URL.Path
			if len(q) > 0 {
				cleanURL += "?" + q.Encode()
			}
			cookieVal := generatePreviewCookieValue(sessionID, targetPort)
			resp := &http.Response{
				StatusCode: http.StatusFound,
				Header:     make(http.Header),
			}
			resp.Header.Set("Location", cleanURL)
			resp.Header.Add("Set-Cookie", fmt.Sprintf("%s=%s; Path=/p/%s/%d/; HttpOnly; SameSite=Strict; Max-Age=1800",
				previewCookieName(sessionID, targetPort), cookieVal, sessionID, targetPort))
			resp.Write(stream)
			return
		}
	}

	// 2. Cookie validation
	cookie, err := req.Cookie(previewCookieName(sessionID, targetPort))
	if err != nil || !validatePreviewCookie(sessionID, targetPort, cookie.Value) {
		resp := &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("403 Forbidden: Invalid or missing preview authorization ticket/cookie")),
		}
		resp.Header.Set("Content-Type", "text/plain")
		resp.Write(stream)
		return
	}

	// 3. Filter internal cookies so local app cannot steal RMTE IDE authentication
	filterInternalCookies(req)

	// 4. Handle WebSocket Upgrade (e.g. Vite HMR)
	if strings.ToLower(req.Header.Get("Upgrade")) == "websocket" {
		localConn, err := dialPreview(targetPort)
		if err != nil {
			resp := &http.Response{
				StatusCode: http.StatusBadGateway,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("502 Bad Gateway: " + err.Error())),
			}
			resp.Header.Set("Content-Type", "text/plain")
			resp.Write(stream)
			return
		}
		defer localConn.Close()

		req.RequestURI = ""
		req.URL.Scheme = "http"
		req.URL.Host = fmt.Sprintf("127.0.0.1:%d", targetPort)
		req.URL.Path = subPath
		req.Host = fmt.Sprintf("127.0.0.1:%d", targetPort)
		if req.Header.Get("Origin") != "" {
			req.Header.Set("Origin", fmt.Sprintf("http://127.0.0.1:%d", targetPort))
		}

		if err := req.Write(localConn); err != nil {
			return
		}

		errc := make(chan error, 2)
		go func() { _, err := io.Copy(localConn, stream); errc <- err }()
		go func() { _, err := io.Copy(stream, localConn); errc <- err }()
		<-errc
		return
	}

	// 5. Standard HTTP Request execution
	req.RequestURI = ""
	req.URL.Scheme = "http"
	req.URL.Host = fmt.Sprintf("127.0.0.1:%d", targetPort)
	req.URL.Path = subPath
	req.Host = fmt.Sprintf("127.0.0.1:%d", targetPort)
	if req.Header.Get("Origin") != "" {
		req.Header.Set("Origin", fmt.Sprintf("http://127.0.0.1:%d", targetPort))
	}
	req.Header.Set("Accept-Encoding", "identity")

	localTransport := &http.Transport{
		DisableCompression: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialPreview(targetPort)
		},
	}

	resp, err := localTransport.RoundTrip(req)
	if err != nil {
		isDoc := strings.Contains(req.Header.Get("Accept"), "text/html") || req.Header.Get("Sec-Fetch-Dest") == "document"
		if isDoc {
			html := renderFriendlyErrorHTML(sessionID, targetPort, err)
			errResp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(html)),
			}
			errResp.Header.Set("Content-Type", "text/html; charset=utf-8")
			errResp.Write(stream)
			return
		}
		errResp := &http.Response{
			StatusCode: http.StatusBadGateway,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("502 Bad Gateway: " + err.Error())),
		}
		errResp.Header.Set("Content-Type", "text/plain")
		errResp.Write(stream)
		return
	}
	defer resp.Body.Close()

	// Strip frame-busting headers and CSP
	resp.Header.Del("X-Frame-Options")
	resp.Header.Del("Content-Security-Policy")

	// Rewrite Location header for redirects
	loc := resp.Header.Get("Location")
	if loc != "" {
		basePrefix := fmt.Sprintf("/p/%s/%d", sessionID, targetPort)
		if strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, basePrefix) {
			resp.Header.Set("Location", basePrefix+loc)
		} else if strings.HasPrefix(loc, fmt.Sprintf("http://127.0.0.1:%d", targetPort)) {
			trimmed := strings.TrimPrefix(loc, fmt.Sprintf("http://127.0.0.1:%d", targetPort))
			resp.Header.Set("Location", basePrefix+trimmed)
		}
	}

	// Rewrite Set-Cookie Path
	cookies := resp.Header.Values("Set-Cookie")
	if len(cookies) > 0 {
		resp.Header.Del("Set-Cookie")
		cookiePathRegex := regexp.MustCompile(`(?i)Path=/([^;]*)`)
		cookieDomainRegex := regexp.MustCompile(`(?i)Domain=[^;]*;?\s*`)
		for _, cVal := range cookies {
			rewritten := cookiePathRegex.ReplaceAllString(cVal, fmt.Sprintf("Path=/p/%s/%d/$1", sessionID, targetPort))
			rewritten = cookieDomainRegex.ReplaceAllString(rewritten, "")
			resp.Header.Add("Set-Cookie", rewritten)
		}
	}

	// HTML shim injection
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(strings.ToLower(ct), "text/html") {
		bodyBytes, err := io.ReadAll(resp.Body)
		if err == nil {
			bodyStr := string(bodyBytes)
			shim := buildPreviewShimHTML(sessionID, targetPort)
			lower := strings.ToLower(bodyStr)
			if idx := strings.Index(lower, "<head>"); idx != -1 {
				bodyStr = bodyStr[:idx+6] + "\n" + shim + "\n" + bodyStr[idx+6:]
			} else {
				bodyStr = shim + "\n" + bodyStr
			}
			resp.Header.Del("Content-Length")
			resp.ContentLength = int64(len(bodyStr))
			resp.Body = io.NopCloser(strings.NewReader(bodyStr))
		}
	}

	resp.Write(stream)
}
