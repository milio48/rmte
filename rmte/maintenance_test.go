package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// R1: failed admin logins back off per IP, and a global cap holds even when
// the attacker forges a new client IP for every attempt.
func TestAdminLoginThrottling(t *testing.T) {
	resetAdminLoginLimiter()
	t.Cleanup(resetAdminLoginLimiter)
	now := time.Now()

	for i := 0; i < adminLoginFreeFailures-1; i++ {
		if ok, _ := adminLoginAllowed("1.1.1.1", now); !ok {
			t.Fatalf("attempt %d throttled too early", i+1)
		}
		adminLoginFailed("1.1.1.1", now)
	}
	adminLoginFailed("1.1.1.1", now)
	if ok, wait := adminLoginAllowed("1.1.1.1", now); ok || wait <= 0 {
		t.Fatal("IP not throttled after repeated failures")
	}
	if ok, _ := adminLoginAllowed("2.2.2.2", now); !ok {
		t.Fatal("other IPs should not be throttled by one IP's failures")
	}
	adminLoginSucceeded("1.1.1.1")
	if ok, _ := adminLoginAllowed("1.1.1.1", now); !ok {
		t.Fatal("successful login should clear the IP's backoff")
	}

	resetAdminLoginLimiter()
	for i := 0; i < adminLoginGlobalPerMin; i++ {
		adminLoginFailed("10.0.0."+string(rune('a'+i%26)), now)
	}
	if ok, _ := adminLoginAllowed("203.0.113.9", now); ok {
		t.Fatal("global failure cap not enforced for a fresh (forged) IP")
	}
	if ok, _ := adminLoginAllowed("203.0.113.9", now.Add(61*time.Second)); !ok {
		t.Fatal("global cap should reset after a minute")
	}
}

func TestAdminLoginEndpointThrottlesAndSetsSecureCookie(t *testing.T) {
	resetAdminLoginLimiter()
	t.Cleanup(resetAdminLoginLimiter)
	mux := http.NewServeMux()
	setupAdminHandler(mux, "/admin", "correct-admin-pass")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	post := func(pass string, extra map[string]string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/admin/api/login", strings.NewReader(`{"password":"`+pass+`"}`))
		req.Header.Set("Origin", srv.URL)
		for k, v := range extra {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	if resp := post("correct-admin-pass", map[string]string{"X-Forwarded-Proto": "https"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("login failed: %d", resp.StatusCode)
	} else if c := resp.Cookies(); len(c) == 0 || !c[0].Secure {
		t.Fatal("admin cookie must be Secure behind HTTPS")
	}

	var last *http.Response
	for i := 0; i < adminLoginFreeFailures+1; i++ {
		last = post("wrong", nil)
	}
	if last.StatusCode != http.StatusTooManyRequests || last.Header.Get("Retry-After") == "" {
		t.Fatalf("expected 429 with Retry-After, got %d", last.StatusCode)
	}
	if resp := post("correct-admin-pass", nil); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("throttled client must not be able to try even the right password, got %d", resp.StatusCode)
	}
}

// R2: browsers may only open WebSockets from the relay's own origin or an
// explicitly allowed one; non-browser clients send no Origin.
func TestWebSocketOriginCheck(t *testing.T) {
	origCfg := serverCfg
	t.Cleanup(func() { serverCfg = origCfg })
	serverCfg = &ServeConfig{WSOrigins: "https://ui.example.com/, http://localhost:5173"}

	req := func(host, origin string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://"+host+"/ws-rmte", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}
	cases := []struct {
		host, origin string
		want         bool
	}{
		{"relay.example.com", "", true},
		{"relay.example.com", "https://relay.example.com", true},
		{"relay.example.com", "https://RELAY.example.com", true},
		{"relay.example.com", "https://ui.example.com", true},
		{"relay.example.com", "http://localhost:5173", true},
		{"relay.example.com", "https://evil.example.net", false},
		{"relay.example.com", "http://ui.example.com", false},
		{"relay.example.com", "null", false},
		{"localhost:8048", "http://127.0.0.1:8048", true},
		{"127.0.0.1:8048", "http://[::1]:8048", true},
		{"localhost:8048", "http://127.0.0.1:9999", false},
		{"relay.example.com:8048", "http://localhost:8048", false},
	}
	for _, c := range cases {
		if got := checkWSOrigin(req(c.host, c.origin)); got != c.want {
			t.Errorf("origin %q on host %q: got %v want %v", c.origin, c.host, got, c.want)
		}
	}
}

// R4: a configured resolver that fails must not silently fall back to
// public DNS (queries would leave a corporate / split-horizon network).
func TestDNSNoPublicFallbackForConfiguredResolver(t *testing.T) {
	dial := dnsDialFunc(true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // now refuses connections

	conn, err := dial(ctx, "tcp", addr)
	if err == nil {
		remote := conn.RemoteAddr().String()
		conn.Close()
		t.Fatalf("failing configured resolver fell back to %s", remote)
	}
}
