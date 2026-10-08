package main

import (
	"fmt"
	"net/url"
	"strings"
)

// resolvePublicRelayURL computes the advertised public WebSocket relay URL from --public-url.
// It supports:
//   - Full URLs with schemes: https://my.rmte.biz.id, http://serverku.net:8041, wss://my.rmte.biz.id/ws-rmte
//   - Host with custom port: serverku.net:8041 -> ws://serverku.net:8041/ws-rmte
//   - Host without port: my.rmte.biz.id -> wss://my.rmte.biz.id/ws-rmte (defaults to TLS wss)
//   - Empty (default): ws://localhost:<port>/<ws-path>
func resolvePublicRelayURL(raw string, port int, wsPath string) (relayURL string, publicDisplay string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Sprintf("ws://localhost:%d%s", port, wsPath), ""
	}

	normalized := raw
	// If no scheme is present, deduce whether to default to wss or ws
	if !strings.Contains(normalized, "://") {
		hasExplicitPort := false
		isPort443 := false
		if strings.HasPrefix(normalized, "[") {
			// Bracketed IPv6: e.g. [::1]:8048
			if idx := strings.LastIndex(normalized, "]:"); idx != -1 {
				hasExplicitPort = true
				if normalized[idx+2:] == "443" {
					isPort443 = true
				}
			}
		} else if strings.Count(normalized, ":") == 1 {
			// Single colon indicates host:port (e.g. serverku.net:8041)
			parts := strings.Split(normalized, ":")
			hasExplicitPort = true
			if parts[1] == "443" {
				isPort443 = true
			}
		}

		if isPort443 {
			normalized = "wss://" + normalized
		} else if hasExplicitPort {
			normalized = "ws://" + normalized
		} else {
			// Domain or host without explicit port (e.g. my.rmte.biz.id) defaults to wss/https
			normalized = "wss://" + normalized
		}
	}

	u, err := url.Parse(normalized)
	if err != nil || u.Host == "" {
		// Fallback
		return fmt.Sprintf("ws://%s:%d%s", raw, port, wsPath), raw
	}

	wsScheme := "ws"
	if u.Scheme == "https" || u.Scheme == "wss" {
		wsScheme = "wss"
	}

	path := u.Path
	if path == "" || path == "/" {
		path = wsPath
	} else if !strings.HasSuffix(path, wsPath) && u.Scheme != "ws" && u.Scheme != "wss" {
		path = strings.TrimSuffix(path, "/") + wsPath
	}

	relayURL = fmt.Sprintf("%s://%s%s", wsScheme, u.Host, path)
	return relayURL, raw
}

// BannerInfo holds everything printed in the startup banner of `serve` and `share`.
type BannerInfo struct {
	Mode          string // standalone | hybrid | relay | share
	Bind          string // only for serve
	PublicURL     string // only for serve
	Port          int    // only for serve
	RelayURL      string // public WebSocket URL of the relay
	WebPath       string
	WSPath        string
	Pass          string
	PassGenerated bool
	SessionID     string // empty in relay mode
	Buffer        int
	NoWeb         bool
	NoCLI         bool
	Preview       bool
	Dir           string
}

func bannerFromServe(cfg *ServeConfig, sessionID string) BannerInfo {
	bind := "127.0.0.1"
	if cfg.Public {
		bind = "0.0.0.0"
	}
	relayURL, publicDisplay := resolvePublicRelayURL(cfg.PublicURL, cfg.Port, cfg.WSPath)
	return BannerInfo{
		Mode:          cfg.Mode,
		Bind:          fmt.Sprintf("%s:%d", bind, cfg.Port),
		PublicURL:     publicDisplay,
		Port:          cfg.Port,
		RelayURL:      relayURL,
		WebPath:       cfg.WebPath,
		WSPath:        cfg.WSPath,
		Pass:          cfg.Pass,
		PassGenerated: cfg.PassGenerated,
		SessionID:     sessionID,
		Buffer:        cfg.Buffer,
		NoWeb:         cfg.NoWeb,
		NoCLI:         cfg.NoCLI,
		Preview:       cfg.Preview,
		Dir:           cfg.Dir,
	}
}

func yesNo(b bool) string {
	if b {
		return "✅ Yes"
	}
	return "❌ No"
}

func printBanner(b BannerInfo) {
	var sb strings.Builder
	line := func(k, v string) { fmt.Fprintf(&sb, "%-17s %s\n", k+":", v) }

	fmt.Fprintf(&sb, "\nRMTE v%s — Mode: %s\n", appVersion, b.Mode)
	sb.WriteString(strings.Repeat("─", 48) + "\n")

	if b.Mode == "share" {
		line("Relay", b.RelayURL)
	} else {
		line("Bind", b.Bind)
		if b.PublicURL != "" {
			line("Public URL", b.PublicURL)
		}
		line("Rmte Port", fmt.Sprint(b.Port))
		line("Open to Relay", yesNo(b.Mode == modeHybrid || b.Mode == modeRelay))
	}

	if b.Dir != "" {
		line("Directory", b.Dir)
	}

	if b.Mode != modeRelay {
		if b.PassGenerated {
			line("Custom Password", "❌ No  (generated: "+b.Pass+")")
		} else {
			line("Custom Password", "✅ Yes")
		}
		line("Session ID", b.SessionID)
		line("Buffer limit", fmt.Sprintf("%d MB", b.Buffer))
	} else {
		line("Session ID", "❌ Relay Only")
	}

	if b.Mode != "share" {
		webPath := b.WebPath
		if b.NoWeb {
			webPath = "❌ disabled (--no-web)"
		}
		line("Web Path", webPath)
		line("WS Path", b.WSPath)
		if b.NoCLI {
			line("CLI Clients", "❌ disabled (--no-cli)")
		}
	}

	if b.Preview {
		line("Web Preview", "✅ Enabled (Proxied reverse-proxy)")
	}

	passDisplay := b.Pass
	if passDisplay == "" {
		passDisplay = "<your-password>"
	}

	if b.Mode == modeRelay {
		sb.WriteString("\nConnect a host to this relay:\n")
		fmt.Fprintf(&sb, "  rmte share --server-relay=\"%s\" --pass=\"secret\"\n", b.RelayURL)
	} else {
		if !b.NoWeb {
			fmt.Fprintf(&sb, "\nShareable link (Web Version):\n  %s\n", buildShareLink(b.RelayURL, b.WebPath, b.SessionID, b.Pass))
		}
		if !b.NoCLI {
			fmt.Fprintf(&sb, "\nJoin CLI / TUI Version:\n  rmte join --server-relay=\"%s\" --id=\"%s\" --pass=\"%s\"\n",
				b.RelayURL, b.SessionID, passDisplay)
		}
		if b.Mode == modeHybrid {
			fmt.Fprintf(&sb, "\nConnect more hosts to this relay:\n  rmte share --server-relay=\"%s\" --pass=\"secret\"\n", b.RelayURL)
		}
	}
	if b.PublicURL == "" && strings.HasPrefix(b.Bind, "0.0.0.0:") {
		sb.WriteString("\nTip: set --public-url=<domain-or-url> (e.g. https://my.rmte.biz.id or http://host:port) to generate ready-to-share links.\n")
	}
	sb.WriteString("\n")
	fmt.Print(sb.String())
}
