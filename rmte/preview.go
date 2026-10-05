package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xtaci/smux/v2"
)

// wsNetConn wraps *websocket.Conn to implement net.Conn for smux.
type wsNetConn struct {
	ws     *websocket.Conn
	reader io.Reader
	mu     sync.Mutex
}

func newWSNetConn(ws *websocket.Conn) *wsNetConn {
	return &wsNetConn{ws: ws}
}

func (c *wsNetConn) Read(b []byte) (int, error) {
	for {
		if c.reader == nil {
			msgType, r, err := c.ws.NextReader()
			if err != nil {
				return 0, err
			}
			if msgType != websocket.BinaryMessage {
				continue
			}
			c.reader = r
		}
		n, err := c.reader.Read(b)
		if err == io.EOF {
			c.reader = nil
			continue
		}
		return n, err
	}
}

func (c *wsNetConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	w, err := c.ws.NextWriter(websocket.BinaryMessage)
	if err != nil {
		return 0, err
	}
	if _, err := w.Write(b); err != nil {
		w.Close()
		return 0, err
	}
	return len(b), w.Close()
}

func (c *wsNetConn) Close() error {
	return c.ws.Close()
}

func (c *wsNetConn) LocalAddr() net.Addr {
	return c.ws.LocalAddr()
}

func (c *wsNetConn) RemoteAddr() net.Addr {
	return c.ws.RemoteAddr()
}

func (c *wsNetConn) SetDeadline(t time.Time) error {
	if err := c.ws.SetReadDeadline(t); err != nil {
		return err
	}
	return c.ws.SetWriteDeadline(t)
}

func (c *wsNetConn) SetReadDeadline(t time.Time) error {
	return c.ws.SetReadDeadline(t)
}

func (c *wsNetConn) SetWriteDeadline(t time.Time) error {
	return c.ws.SetWriteDeadline(t)
}

// createSmuxConfig returns a tuned smux v2 configuration for high-throughput streaming.
func createSmuxConfig() *smux.Config {
	cfg := smux.DefaultConfig()
	cfg.MaxStreamBuffer = 2 * 1024 * 1024   // 2 MB per stream
	cfg.MaxReceiveBuffer = 32 * 1024 * 1024 // 32 MB aggregate buffer
	cfg.KeepAliveInterval = 10 * time.Second
	cfg.KeepAliveTimeout = 30 * time.Second
	return cfg
}

// isSelfRelayPort returns true if port matches the relay's own listening port.
func isSelfRelayPort(port int) bool {
	if serverCfg != nil && serverCfg.Port == port {
		return true
	}
	return false
}

// dialPreview connects to a local loopback port using fast concurrent dual-stack dialing.
func dialPreview(port int) (net.Conn, error) {
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid port range: %d", port)
	}
	if isSelfRelayPort(port) {
		return nil, fmt.Errorf("anti-self-loop: cannot dial relay port %d", port)
	}

	type dialResult struct {
		conn net.Conn
		err  error
	}

	resCh := make(chan dialResult, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var d net.Dialer
	// IPv4 goroutine
	go func() {
		c, err := d.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", port))
		resCh <- dialResult{c, err}
	}()
	// IPv6 goroutine with 100ms offset
	go func() {
		time.Sleep(100 * time.Millisecond)
		c, err := d.DialContext(ctx, "tcp", fmt.Sprintf("[::1]:%d", port))
		resCh <- dialResult{c, err}
	}()

	var firstErr error
	for i := 0; i < 2; i++ {
		r := <-resCh
		if r.err == nil {
			return r.conn, nil
		}
		if firstErr == nil {
			firstErr = r.err
		}
	}
	return nil, firstErr
}

// filterInternalCookies removes internal RMTE cookies before forwarding request to local app.
func filterInternalCookies(req *http.Request) {
	cookieHeader := req.Header.Get("Cookie")
	if cookieHeader == "" {
		return
	}
	parts := strings.Split(cookieHeader, ";")
	var kept []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if strings.HasPrefix(trimmed, "rmte_") {
			continue
		}
		kept = append(kept, trimmed)
	}
	if len(kept) == 0 {
		req.Header.Del("Cookie")
	} else {
		req.Header.Set("Cookie", strings.Join(kept, "; "))
	}
}

// generatePreviewTicket creates an HMAC preview ticket using the derived preview subkey.
func generatePreviewTicket(sessionID string, port int, expiresAt int64, nonce string) string {
	if rawEncryptionKey == nil {
		return ""
	}
	// Derive subkey: HMAC(rawEncryptionKey, "rmte-preview-subauth")
	hKey := hmac.New(sha256.New, rawEncryptionKey)
	hKey.Write([]byte("rmte-preview-subauth"))
	subKey := hKey.Sum(nil)

	h := hmac.New(sha256.New, subKey)
	msg := fmt.Sprintf("%s:%d:%d:%s", sessionID, port, expiresAt, nonce)
	h.Write([]byte(msg))
	sig := hex.EncodeToString(h.Sum(nil))
	return fmt.Sprintf("%d:%s:%s", expiresAt, nonce, sig)
}

// validatePreviewTicket validates a ticket string formatted as "expiresAt:nonce:sig".
func validatePreviewTicket(sessionID string, port int, ticket string) bool {
	if rawEncryptionKey == nil || ticket == "" {
		return false
	}
	parts := strings.Split(ticket, ":")
	if len(parts) != 3 {
		return false
	}
	expiresAt, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() > expiresAt {
		return false // expired
	}
	nonce := parts[1]
	providedSig, err := hex.DecodeString(parts[2])
	if err != nil {
		return false
	}

	hKey := hmac.New(sha256.New, rawEncryptionKey)
	hKey.Write([]byte("rmte-preview-subauth"))
	subKey := hKey.Sum(nil)

	h := hmac.New(sha256.New, subKey)
	msg := fmt.Sprintf("%s:%d:%d:%s", sessionID, port, expiresAt, nonce)
	h.Write([]byte(msg))
	expectedSig := h.Sum(nil)

	return hmac.Equal(providedSig, expectedSig)
}

// previewCookieName generates the scoped session cookie name for a preview session and port.
func previewCookieName(sessionID string, port int) string {
	return fmt.Sprintf("rmte_pvw_%s_%d", sessionID, port)
}

// generatePreviewCookieValue generates a verification hash for the cookie.
func generatePreviewCookieValue(sessionID string, port int) string {
	if rawEncryptionKey == nil {
		return "ok"
	}
	h := hmac.New(sha256.New, rawEncryptionKey)
	h.Write([]byte(fmt.Sprintf("rmte-cookie:%s:%d", sessionID, port)))
	return hex.EncodeToString(h.Sum(nil))
}

// validatePreviewCookie validates the cookie value.
func validatePreviewCookie(sessionID string, port int, val string) bool {
	if rawEncryptionKey == nil {
		return true
	}
	expected := generatePreviewCookieValue(sessionID, port)
	return hmac.Equal([]byte(val), []byte(expected))
}

// renderFriendlyErrorHTML returns a clean HTML error page when local port is unreachable.
func renderFriendlyErrorHTML(sessionID string, port int, dialErr error) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Port %d Not Reachable · RMTE Preview</title>
<style>
  body {
    margin: 0; padding: 2rem; font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif;
    background: #0f172a; color: #f8fafc; display: flex; flex-direction: column; align-items: center; justify-content: center; min-height: 80vh;
  }
  .card {
    background: #1e293b; border: 1px solid #334155; border-radius: 12px; padding: 2rem; max-width: 520px; width: 100%%; box-shadow: 0 10px 25px -5px rgba(0,0,0,0.5);
  }
  h2 { margin-top: 0; color: #38bdf8; display: flex; align-items: center; gap: 0.5rem; font-size: 1.3rem; }
  p { line-height: 1.6; color: #94a3b8; font-size: 0.95rem; }
  .code { background: #090d16; padding: 0.75rem 1rem; border-radius: 6px; font-family: monospace; color: #a5f3fc; font-size: 0.9rem; margin: 1rem 0; border: 1px solid #1e293b; }
  .btn {
    display: inline-block; background: #0284c7; color: white; padding: 0.5rem 1.2rem; border-radius: 6px; text-decoration: none; font-weight: 500; font-size: 0.9rem; transition: background 0.2s; border: none; cursor: pointer;
  }
  .btn:hover { background: #0369a1; }
  .err-detail { font-size: 0.8rem; color: #ef4444; font-family: monospace; margin-top: 0.5rem; }
</style>
</head>
<body>
<div class="card">
  <h2>🔌 Port %d is not listening</h2>
  <p>RMTE cannot connect to your local application on port <strong>%d</strong>. Please make sure your server is running inside the RMTE terminal (Tab 0).</p>
  <div class="code">
    # Example commands to start your app:<br>
    bun run dev --port %d<br>
    # or<br>
    dufs -p %d
  </div>
  <div class="err-detail">Diagnostic: %s</div>
  <p style="margin-top: 1.5rem;"><button class="btn" onclick="location.reload()">⟳ Retry Connection</button></p>
</div>
</body>
</html>`, port, port, port, port, port, dialErr.Error())
}

// buildPreviewShimHTML returns the injected <base> and JavaScript shim script.
func buildPreviewShimHTML(sessionID string, port int) string {
	basePath := fmt.Sprintf("/p/%s/%d/", sessionID, port)
	basePrefix := fmt.Sprintf("/p/%s/%d", sessionID, port)

	return fmt.Sprintf(`<base href="%s">
<script id="rmte-preview-shim">
(function() {
  const BASE = %q;
  function qualify(url) {
    if (typeof url !== 'string') return url;
    if (url.startsWith('//')) return url;
    if (url.startsWith('/') && !url.startsWith(BASE)) {
      return BASE + url;
    }
    return url;
  }

  // 1. Intercept Click & Submit
  document.addEventListener('click', e => {
    const a = e.target.closest('a');
    if (a && a.getAttribute('href')?.startsWith('/') && !a.getAttribute('href')?.startsWith('//')) {
      a.href = qualify(a.getAttribute('href'));
    }
  }, true);

  document.addEventListener('submit', e => {
    const f = e.target;
    const act = f.getAttribute('action');
    if (act && act.startsWith('/') && !act.startsWith('//')) {
      f.action = qualify(act);
    }
  }, true);

  // 2. Fetch API
  const _fetch = window.fetch;
  window.fetch = function(input, init) {
    if (typeof input === 'string') {
      input = qualify(input);
    } else if (input instanceof Request) {
      try {
        const u = new URL(input.url);
        if (u.origin === window.location.origin && u.pathname.startsWith('/') && !u.pathname.startsWith(BASE)) {
          input = new Request(BASE + u.pathname + u.search, input);
        }
      } catch(e) {}
    }
    return _fetch.call(this, input, init);
  };

  // 3. XMLHttpRequest
  const _open = XMLHttpRequest.prototype.open;
  XMLHttpRequest.prototype.open = function(method, url, ...args) {
    return _open.call(this, method, qualify(url), ...args);
  };

  // 4. WebSocket Proxy
  const _WS = window.WebSocket;
  window.WebSocket = new Proxy(_WS, {
    construct(target, args) {
      let url = args[0];
      if (typeof url === 'string') {
        const u = new URL(url, window.location.href);
        if (u.host === window.location.host && u.pathname.startsWith('/') && !u.pathname.startsWith(BASE)) {
          u.pathname = BASE + u.pathname;
          args[0] = u.toString();
        }
      }
      return new target(...args);
    }
  });

  // 5. Navigation & History API
  const _wopen = window.open;
  window.open = function(url, target, features) {
    return _wopen.call(window, qualify(url), target, features);
  };

  try {
    const _assign = window.location.assign.bind(window.location);
    window.location.assign = function(url) { return _assign(qualify(url)); };
    const _replace = window.location.replace.bind(window.location);
    window.location.replace = function(url) { return _replace(qualify(url)); };
  } catch(e) {}

  const _pushState = history.pushState;
  history.pushState = function(state, unused, url) {
    return _pushState.call(this, state, unused, qualify(url));
  };
  const _replaceState = history.replaceState;
  history.replaceState = function(state, unused, url) {
    return _replaceState.call(this, state, unused, qualify(url));
  };
})();
</script>
`, basePath, basePrefix)
}
