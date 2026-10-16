package main

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
)

// Client IP resolution for bans, login throttling and logs.
//
// Forwarding headers (CF-Connecting-IP, X-Forwarded-For, X-Real-IP) are set by
// whoever sends the request, so they are only honoured when the TCP peer is a
// trusted proxy (--trusted-proxies, default "loopback": nginx, Caddy or
// cloudflared on the same machine). Anyone connecting directly is identified
// by their socket address and cannot forge another IP.

var (
	trustedProxyMu   sync.RWMutex
	trustedProxyNets = mustParseTrustedProxies("loopback")
)

var trustedProxyAliases = map[string][]string{
	"loopback": {"127.0.0.0/8", "::1/128"},
	"private":  {"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"},
}

// parseTrustedProxies parses a comma-separated list of CIDRs, single IPs and
// the aliases "loopback", "private" and "none".
func parseTrustedProxies(spec string) ([]*net.IPNet, error) {
	var nets []*net.IPNet
	for _, tok := range strings.Split(spec, ",") {
		tok = strings.ToLower(strings.TrimSpace(tok))
		if tok == "" || tok == "none" {
			continue
		}
		items := []string{tok}
		if alias, ok := trustedProxyAliases[tok]; ok {
			items = alias
		}
		for _, item := range items {
			if !strings.Contains(item, "/") {
				ip := net.ParseIP(item)
				if ip == nil {
					return nil, fmt.Errorf("invalid trusted proxy %q", item)
				}
				if ip.To4() != nil {
					item += "/32"
				} else {
					item += "/128"
				}
			}
			_, n, err := net.ParseCIDR(item)
			if err != nil {
				return nil, fmt.Errorf("invalid trusted proxy %q", item)
			}
			nets = append(nets, n)
		}
	}
	return nets, nil
}

func mustParseTrustedProxies(spec string) []*net.IPNet {
	nets, err := parseTrustedProxies(spec)
	if err != nil {
		panic(err)
	}
	return nets
}

func setTrustedProxies(nets []*net.IPNet) {
	trustedProxyMu.Lock()
	trustedProxyNets = nets
	trustedProxyMu.Unlock()
}

func isTrustedProxy(ip net.IP) bool {
	if ip == nil {
		return false
	}
	trustedProxyMu.RLock()
	defer trustedProxyMu.RUnlock()
	for _, n := range trustedProxyNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func remoteIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}

// getClientIP returns the client's IP address. Forwarding headers are used
// only when the request comes from a trusted proxy; X-Forwarded-For is read
// right to left, skipping trusted hops, so values a client prepends itself
// are never picked.
func getClientIP(r *http.Request) string {
	peer := remoteIP(r)
	if peer == nil {
		return "unknown"
	}
	if !isTrustedProxy(peer) {
		return peer.String()
	}

	if cf := net.ParseIP(strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))); cf != nil {
		return cf.String()
	}
	if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
		hops := strings.Split(strings.Join(xff, ","), ",")
		for i := len(hops) - 1; i >= 0; i-- {
			ip := net.ParseIP(strings.TrimSpace(hops[i]))
			if ip == nil {
				break // malformed entry: stop rather than trust anything left of it
			}
			if !isTrustedProxy(ip) {
				return ip.String()
			}
		}
	}
	if real := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); real != nil {
		return real.String()
	}
	return peer.String()
}
