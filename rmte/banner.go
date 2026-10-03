package main

import (
	"fmt"
	"strings"
)

// BannerInfo holds everything printed in the startup banner of `serve` and `share`.
type BannerInfo struct {
	Mode          string // standalone | hybrid | relay | share
	Bind          string // only for serve
	Hostname      string // only for serve
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
	Dir           string
}

func bannerFromServe(cfg *ServeConfig, sessionID string) BannerInfo {
	bind := "127.0.0.1"
	if cfg.Public {
		bind = "0.0.0.0"
	}
	return BannerInfo{
		Mode:          cfg.Mode,
		Bind:          fmt.Sprintf("%s:%d", bind, cfg.Port),
		Hostname:      cfg.Hostname,
		Port:          cfg.Port,
		RelayURL:      fmt.Sprintf("ws://%s:%d%s", cfg.Hostname, cfg.Port, cfg.WSPath),
		WebPath:       cfg.WebPath,
		WSPath:        cfg.WSPath,
		Pass:          cfg.Pass,
		PassGenerated: cfg.PassGenerated,
		SessionID:     sessionID,
		Buffer:        cfg.Buffer,
		NoWeb:         cfg.NoWeb,
		NoCLI:         cfg.NoCLI,
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
		line("Host Name", b.Hostname)
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

	passDisplay := b.Pass
	if !b.PassGenerated {
		passDisplay = "<your-password>"
	}

	if b.Mode == modeRelay {
		sb.WriteString("\nConnect a host to this relay:\n")
		fmt.Fprintf(&sb, "  rmte share --server-relay=\"%s\" --pass=\"secret\"\n", b.RelayURL)
	} else {
		if !b.NoWeb {
			fmt.Fprintf(&sb, "\nShareable link (Web Version):\n  %s\n", buildShareLink(b.RelayURL, b.WebPath, b.SessionID))
		}
		if !b.NoCLI {
			fmt.Fprintf(&sb, "\nJoin CLI / TUI Version:\n  rmte join --server-relay=\"%s\" --id=\"%s\" --pass=\"%s\"\n",
				b.RelayURL, b.SessionID, passDisplay)
		}
		if b.Mode == modeHybrid {
			fmt.Fprintf(&sb, "\nConnect more hosts to this relay:\n  rmte share --server-relay=\"%s\" --pass=\"secret\"\n", b.RelayURL)
		}
	}
	if b.Hostname == "unknown" {
		sb.WriteString("\nTip: set --hostname=<public-ip-or-domain> to get ready-to-share links.\n")
	}
	sb.WriteString("\n")
	fmt.Print(sb.String())
}
