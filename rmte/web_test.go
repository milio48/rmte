package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebHandlerSecurityHeaders(t *testing.T) {
	mux := http.NewServeMux()
	setupWebHandler(mux, "/", "/ws")

	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK for root UI, got %d", rr.Code)
	}

	// 1. Verify X-Content-Type-Options
	if val := rr.Header().Get("X-Content-Type-Options"); val != "nosniff" {
		t.Errorf("Expected X-Content-Type-Options: nosniff, got %q", val)
	}

	// 2. Verify X-Frame-Options
	if val := rr.Header().Get("X-Frame-Options"); val != "SAMEORIGIN" {
		t.Errorf("Expected X-Frame-Options: SAMEORIGIN, got %q", val)
	}

	// 3. Verify Referrer-Policy
	if val := rr.Header().Get("Referrer-Policy"); val != "strict-origin-when-cross-origin" {
		t.Errorf("Expected Referrer-Policy: strict-origin-when-cross-origin, got %q", val)
	}

	// 4. Verify Content-Security-Policy directives
	csp := rr.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("Missing Content-Security-Policy header")
	}

	requiredDirectives := []string{
		"default-src 'self'",
		"img-src 'self' blob: data:",
		"media-src 'self' blob:",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-src 'self' about: http: https: blob: data:",
	}
	for _, dir := range requiredDirectives {
		if !strings.Contains(csp, dir) {
			t.Errorf("CSP missing expected directive %q. Full header: %q", dir, csp)
		}
	}
}
