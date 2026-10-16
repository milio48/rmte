package main

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Preview port policy (host side).
//
// The host only forwards preview traffic to loopback ports that were opened
// explicitly: either listed with --preview-ports (a fixed allowlist viewers
// cannot extend), or, when that flag is empty, opened on demand by a viewer
// through a sealed "preview_open" control message, which is logged. Ports
// that were never opened (a local Redis, an admin panel) are not reachable
// through the preview proxy.

var previewPolicy = struct {
	mu      sync.RWMutex
	fixed   bool
	allowed map[int]bool
}{allowed: map[int]bool{}}

// parsePreviewPorts parses "3000,5173,8000-8010".
func parsePreviewPorts(spec string) ([]int, error) {
	var ports []int
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi := part, part
		if i := strings.Index(part, "-"); i >= 0 {
			lo, hi = part[:i], part[i+1:]
		}
		a, err1 := strconv.Atoi(strings.TrimSpace(lo))
		b, err2 := strconv.Atoi(strings.TrimSpace(hi))
		if err1 != nil || err2 != nil || a < 1 || b > 65535 || a > b || b-a > 1000 {
			return nil, fmt.Errorf("invalid preview port %q", part)
		}
		for p := a; p <= b; p++ {
			ports = append(ports, p)
		}
	}
	return ports, nil
}

// setPreviewPorts installs a fixed allowlist; an empty list means on-demand.
func setPreviewPorts(ports []int) {
	previewPolicy.mu.Lock()
	defer previewPolicy.mu.Unlock()
	previewPolicy.fixed = len(ports) > 0
	previewPolicy.allowed = map[int]bool{}
	for _, p := range ports {
		previewPolicy.allowed[p] = true
	}
}

func previewPortAllowed(port int) bool {
	previewPolicy.mu.RLock()
	defer previewPolicy.mu.RUnlock()
	return previewPolicy.allowed[port]
}

// openPreviewPort handles a viewer's request to preview port.
func openPreviewPort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid port %d", port)
	}
	if isSelfRelayPort(port) {
		return fmt.Errorf("port %d is the relay itself", port)
	}
	previewPolicy.mu.Lock()
	defer previewPolicy.mu.Unlock()
	if previewPolicy.allowed[port] {
		return nil
	}
	if previewPolicy.fixed {
		return fmt.Errorf("port %d is not in the host's --preview-ports allowlist", port)
	}
	previewPolicy.allowed[port] = true
	return nil
}
