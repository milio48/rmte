package main

import (
	"net/url"
	"strings"
	"testing"
)

func TestBuildShareLinkPassInFragment(t *testing.T) {
	link := buildShareLink("wss://relay.example.com/ws-rmte", "/", "box1", "s3cr&t =x")
	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.Scheme != "https" {
		t.Errorf("scheme = %q, want https", u.Scheme)
	}
	if strings.Contains(u.RawQuery, "pass") {
		t.Errorf("password leaked into query string: %q", u.RawQuery)
	}
	if got := u.Query().Get("session"); got != "box1" {
		t.Errorf("session = %q, want box1", got)
	}
	frag, err := url.ParseQuery(u.EscapedFragment())
	if err != nil {
		t.Fatalf("parse fragment: %v", err)
	}
	if got := frag.Get("pass"); got != "s3cr&t =x" {
		t.Errorf("fragment pass = %q, want %q", got, "s3cr&t =x")
	}
}

func TestBuildShareLinkNoPass(t *testing.T) {
	link := buildShareLink("ws://127.0.0.1:8048/ws-rmte", "/", "box1", "")
	if strings.Contains(link, "#") {
		t.Errorf("expected no fragment when pass is empty, got %q", link)
	}
}

func TestResolvePublicRelayURL(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		port         int
		wsPath       string
		wantRelay    string
		wantDisplay  string
	}{
		{
			name:        "Full HTTPS URL (Cloudflare/Proxy)",
			input:       "https://my.rmte.biz.id",
			port:        2638,
			wsPath:      "/ws-rmte",
			wantRelay:   "wss://my.rmte.biz.id/ws-rmte",
			wantDisplay: "https://my.rmte.biz.id",
		},
		{
			name:        "Full HTTP URL with custom port",
			input:       "http://serverku.net:8041",
			port:        8041,
			wsPath:      "/ws-rmte",
			wantRelay:   "ws://serverku.net:8041/ws-rmte",
			wantDisplay: "http://serverku.net:8041",
		},
		{
			name:        "Domain only without scheme",
			input:       "my.rmte.biz.id",
			port:        2638,
			wsPath:      "/ws-rmte",
			wantRelay:   "wss://my.rmte.biz.id/ws-rmte",
			wantDisplay: "my.rmte.biz.id",
		},
		{
			name:        "Domain with port without scheme",
			input:       "serverku.net:8041",
			port:        8041,
			wsPath:      "/ws-rmte",
			wantRelay:   "ws://serverku.net:8041/ws-rmte",
			wantDisplay: "serverku.net:8041",
		},
		{
			name:        "Direct WSS URL",
			input:       "wss://my.rmte.biz.id/ws-rmte",
			port:        2638,
			wsPath:      "/ws-rmte",
			wantRelay:   "wss://my.rmte.biz.id/ws-rmte",
			wantDisplay: "wss://my.rmte.biz.id/ws-rmte",
		},
		{
			name:        "Empty input (default loopback)",
			input:       "",
			port:        8048,
			wsPath:      "/ws-rmte",
			wantRelay:   "ws://localhost:8048/ws-rmte",
			wantDisplay: "",
		},
		{
			name:        "URL with non-root subpath",
			input:       "https://my.domain.com/app",
			port:        8048,
			wsPath:      "/ws-rmte",
			wantRelay:   "wss://my.domain.com/app/ws-rmte",
			wantDisplay: "https://my.domain.com/app",
		},
		{
			name:        "Bracketed IPv6 with custom port",
			input:       "[::1]:8048",
			port:        8048,
			wsPath:      "/ws-rmte",
			wantRelay:   "ws://[::1]:8048/ws-rmte",
			wantDisplay: "[::1]:8048",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRelay, gotDisplay := resolvePublicRelayURL(tt.input, tt.port, tt.wsPath)
			if gotRelay != tt.wantRelay {
				t.Errorf("relayURL = %q, want %q", gotRelay, tt.wantRelay)
			}
			if gotDisplay != tt.wantDisplay {
				t.Errorf("display = %q, want %q", gotDisplay, tt.wantDisplay)
			}
		})
	}

	// Verify exact Pterodactyl + Cloudflare Origin end-to-end link generation
	relayURL, _ := resolvePublicRelayURL("https://my.rmte.biz.id", 2638, "/ws-rmte")
	shareLink := buildShareLink(relayURL, "/", "rmtepusat", "anjay_mas_")
	wantLink := "https://my.rmte.biz.id/?server=wss%3A%2F%2Fmy.rmte.biz.id%2Fws-rmte&session=rmtepusat#pass=anjay_mas_"
	if shareLink != wantLink {
		t.Errorf("shareLink = %q, want %q", shareLink, wantLink)
	}
}
