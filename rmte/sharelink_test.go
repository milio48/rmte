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
