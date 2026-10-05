package main

import (
	"crypto/rand"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

var (
	// appVersion is set at build time via: -ldflags "-X main.appVersion=..."
	// Defaults to "dev" for local builds without build tags.
	appVersion = "dev"
)

func init() {
	if appVersion == "dev" {
		if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			appVersion = strings.TrimPrefix(bi.Main.Version, "v")
		}
	} else {
		appVersion = strings.TrimPrefix(appVersion, "v")
	}
}

const (
	protocolVersion = "0.5"

	defaultPort        = 8048
	defaultWSPath      = "/ws-rmte"
	defaultServer      = "ws://localhost:8048/ws-rmte"
	defaultPublicRelay = "wss://my.rmte.biz.id/ws-rmte"
)

// Serve modes
const (
	modeStandalone = "standalone" // relay + local host, external hosts rejected
	modeHybrid     = "hybrid"     // relay + local host, external hosts accepted
	modeRelay      = "relay"      // relay only, no local PTY
)

// ServeConfig holds all options for `rmte serve`.
type ServeConfig struct {
	Mode          string
	Port          int
	Pass          string
	PassGenerated bool
	Buffer        int
	WebPath       string
	WSPath        string
	Hostname      string
	Public        bool
	NoWeb         bool
	NoCLI         bool
	Preview       bool   // enable Embedded Web Browser Preview reverse proxy
	Quiet         bool   // run in background (daemon) and print startup banner + PID
	Dir           string // initial working directory for host
	ID            string // custom session ID for host (optional)
	InternalToken string // used by the embedded host in standalone mode
}

var origArgs []string

func main() {
	origArgs = append([]string(nil), os.Args...)
	if len(os.Args) < 2 {
		// Bare `./rmte` invocation: connect to public relay
		runShare(nil)
		return
	}

	mode := os.Args[1]

	// If mode starts with "-" (e.g. `./rmte -q`, `./rmte --pass=...`), treat as quick share
	// UNLESS it is help or version
	if strings.HasPrefix(mode, "-") {
		switch mode {
		case "-h", "--help":
			printUsage()
			return
		case "-v", "--version":
			fmt.Printf("rmte v%s (protocol %s)\n", appVersion, protocolVersion)
			return
		default:
			runShare(os.Args[1:])
			return
		}
	}

	os.Args = os.Args[1:] // shift args for flags

	switch mode {
	case "serve":
		cmdServe()
	case "share":
		runShare(os.Args)
	case "join":
		cmdJoin()
	case "stop":
		cmdStop()
	case "help":
		printUsage()
	case "version":
		fmt.Printf("rmte v%s (protocol %s)\n", appVersion, protocolVersion)
	default:
		fmt.Printf("Unknown command: %s\n\n", mode)
		printUsage()
		os.Exit(1)
	}
}

func cmdServe() {
	cfg := ServeConfig{}
	flag.StringVar(&cfg.Mode, "mode", modeStandalone, "Serve mode: standalone | hybrid | relay")
	flag.IntVar(&cfg.Port, "port", defaultPort, "Port to listen on")
	flag.StringVar(&cfg.Pass, "pass", "", "Password for E2EE (random if empty; ignored in relay mode)")
	flag.IntVar(&cfg.Buffer, "buffer", 1, "Max buffer size in MB (ignored in relay mode)")
	flag.StringVar(&cfg.WebPath, "web-path", "/", "HTTP path for the Web UI")
	flag.StringVar(&cfg.WSPath, "ws-path", defaultWSPath, "WebSocket path")
	flag.StringVar(&cfg.Hostname, "hostname", "", "Public hostname/IP used only for printed links")
	flag.StringVar(&cfg.Dir, "dir", "", "Initial working directory for File Explorer and terminal")
	flag.StringVar(&cfg.ID, "id", "", "Custom Session ID (lowercase a-z, 0-9, max 10 chars; random if empty)")
	flag.BoolVar(&cfg.Public, "public", false, "Bind to 0.0.0.0 (default 127.0.0.1)")
	flag.BoolVar(&cfg.NoWeb, "no-web", false, "Do not serve the Web UI")
	flag.BoolVar(&cfg.NoCLI, "no-cli", false, "Reject CLI clients (soft restriction)")
	flag.BoolVar(&cfg.Preview, "web-preview", false, "Enable Embedded Web Browser Preview reverse proxy")
	flag.BoolVar(&cfg.Preview, "preview", false, "Alias for --web-preview")
	flag.BoolVar(&cfg.Quiet, "quiet", false, "Run in background (detached) and print connection info with PID")
	flag.BoolVar(&cfg.Quiet, "q", false, "Alias for --quiet")
	flag.Parse()

	if cfg.Quiet && !isDaemonChild() {
		runAsDaemon()
	}

	passSet, bufferSet := false, false
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "pass":
			passSet = true
		case "buffer":
			bufferSet = true
		}
	})

	cfg.Mode = strings.ToLower(strings.TrimSpace(cfg.Mode))
	switch cfg.Mode {
	case modeStandalone, modeHybrid, modeRelay:
	default:
		fatalf("Error: invalid --mode=%q (use standalone | hybrid | relay)", cfg.Mode)
	}
	if cfg.NoWeb && cfg.NoCLI {
		fatalf("Error: --no-web and --no-cli cannot be used together (no client could connect)")
	}
	if cfg.Buffer < 1 {
		fatalf("Error: --buffer must be >= 1")
	}

	if cfg.Dir != "" {
		absDir, err := filepath.Abs(cfg.Dir)
		if err != nil {
			fatalf("Error: invalid --dir=%q: %v", cfg.Dir, err)
		}
		st, err := os.Stat(absDir)
		if err != nil || !st.IsDir() {
			fatalf("Error: --dir=%q does not exist or is not a directory", cfg.Dir)
		}
		cfg.Dir = filepath.ToSlash(absDir)
	}

	if cfg.ID != "" {
		cfg.ID = strings.ToLower(strings.TrimSpace(cfg.ID))
		if err := validateSessionID(cfg.ID); err != nil {
			fatalf("Error: invalid --id: %v", err)
		}
	}

	cfg.WebPath = normalizeWebPath(cfg.WebPath)
	cfg.WSPath = normalizePath(cfg.WSPath)
	if !cfg.NoWeb && cfg.WSPath+"/" == cfg.WebPath {
		fatalf("Error: --ws-path and --web-path must differ")
	}

	if cfg.Hostname == "" {
		if cfg.Public {
			cfg.Hostname = "unknown"
		} else {
			cfg.Hostname = "localhost"
		}
	}

	if cfg.Mode == modeRelay {
		if passSet || bufferSet {
			fmt.Println("Warning: --pass and --buffer are ignored in relay mode (they belong to hosts).")
		}
		cfg.Pass = ""
	} else if cfg.Pass == "" {
		cfg.Pass = generatePassword(12)
		cfg.PassGenerated = true
	}

	if cfg.Mode == modeStandalone {
		cfg.InternalToken = generatePassword(32)
	}

	if cfg.Mode == modeRelay {
		printBanner(bannerFromServe(&cfg, ""))
		markDaemonReady("")
		runServer(&cfg)
		return
	}
	runServeAndShare(&cfg)
}

func runShare(args []string) {
	fs := flag.NewFlagSet("share", flag.ExitOnError)
	server := fs.String("server-relay", defaultPublicRelay, "Relay server WebSocket URL")
	serverAlias := fs.String("server", "", "Alias for --server-relay")
	pass := fs.String("pass", "", "Password for E2EE (random if empty)")
	password := fs.String("password", "", "Alias for --pass")
	id := fs.String("id", "", "Custom Session ID (lowercase a-z, 0-9, max 10 chars; random if empty)")
	bufferMB := fs.Int("buffer", 1, "Max buffer size in MB (terminal ring buffer and file manager)")
	dir := fs.String("dir", "", "Initial working directory for File Explorer and terminal")
	webPreview := fs.Bool("web-preview", true, "Enable Embedded Web Browser Preview reverse proxy")
	preview := fs.Bool("preview", true, "Alias for --web-preview")
	quiet := fs.Bool("quiet", false, "Run in background (detached) and print connection info with PID")
	q := fs.Bool("q", false, "Alias for --quiet")

	if len(args) > 0 {
		_ = fs.Parse(args)
	}

	if (*quiet || *q) && !isDaemonChild() {
		runAsDaemon()
	}

	relayURL := *server
	if *serverAlias != "" {
		relayURL = *serverAlias
	}

	chosenPass := *pass
	if *password != "" {
		chosenPass = *password
	}

	if *bufferMB < 1 {
		fatalf("Error: --buffer must be >= 1")
	}

	var cleanID string
	if *id != "" {
		cleanID = strings.ToLower(strings.TrimSpace(*id))
		if err := validateSessionID(cleanID); err != nil {
			fatalf("Error: invalid --id: %v", err)
		}
	}

	var cleanDir string
	if *dir != "" {
		absDir, err := filepath.Abs(*dir)
		if err != nil {
			fatalf("Error: invalid --dir=%q: %v", *dir, err)
		}
		st, err := os.Stat(absDir)
		if err != nil || !st.IsDir() {
			fatalf("Error: --dir=%q does not exist or is not a directory", *dir)
		}
		cleanDir = filepath.ToSlash(absDir)
	}

	opts := HostOptions{
		DialURL:   relayURL,
		PublicURL: relayURL,
		Pass:      chosenPass,
		Buffer:    *bufferMB,
		Mode:      "share",
		Dir:       cleanDir,
		ID:        cleanID,
		Preview:   *webPreview && *preview,
	}
	if opts.Pass == "" {
		opts.Pass = generatePassword(12)
		opts.PassGenerated = true
	}
	runHost(opts)
}

func cmdJoin() {
	server := flag.String("server-relay", defaultServer, "Relay server WebSocket URL")
	sessionID := flag.String("id", "", "Session ID to join")
	pass := flag.String("pass", "", "Password for E2EE")
	name := flag.String("name", "", "Display name of the viewer")
	flag.Parse()
	if *sessionID == "" || *pass == "" {
		fatalf("Error: --id and --pass are required")
	}
	runViewer(*server, *sessionID, *pass, *name)
}

// runServeAndShare starts the relay server, waits for it to be ready,
// then connects an embedded host session to it — all in one process.
func runServeAndShare(cfg *ServeConfig) {
	go runServer(cfg)

	// Wait for server to be ready (poll TCP on loopback)
	addr := fmt.Sprintf("127.0.0.1:%d", cfg.Port)
	for i := 0; i < 50; i++ {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	runHost(HostOptions{
		DialURL:       fmt.Sprintf("ws://127.0.0.1:%d%s", cfg.Port, cfg.WSPath),
		PublicURL:     fmt.Sprintf("ws://%s:%d%s", cfg.Hostname, cfg.Port, cfg.WSPath),
		Pass:          cfg.Pass,
		PassGenerated: cfg.PassGenerated,
		Buffer:        cfg.Buffer,
		InternalToken: cfg.InternalToken,
		Mode:          cfg.Mode,
		Dir:           cfg.Dir,
		ID:            cfg.ID,
		Preview:       cfg.Preview,
		Serve:         cfg,
	})
}

// normalizePath ensures a leading slash and no trailing slash (except root).
func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	return p
}

// normalizeWebPath ensures leading and trailing slash (e.g. "/web/").
func normalizeWebPath(p string) string {
	p = normalizePath(p)
	if p != "/" {
		p += "/"
	}
	return p
}

// generatePassword returns a random alphanumeric string of length n.
func generatePassword(n int) string {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func fatalf(format string, a ...interface{}) {
	fmt.Printf(format+"\n", a...)
	os.Exit(1)
}

func printUsage() {
	fmt.Printf(`rmte v%s - Remote Terminal Relay & Cloud IDE

Instant Sharing:
  rmte                                  Share terminal & Web IDE to %s
  rmte -q                               Run detached in background (prints share link & PID)
  rmte --id="mysession" --pass="secret" Custom session ID and password
  rmte --dir="/path" --buffer=2         Custom workspace directory and memory limit

Usage:
  rmte       [--pass="secret"] [--id="mysession"] [--dir="path"] [--buffer=1] [-q]
  rmte share [--server-relay="%s"] [--pass="secret"] [--id="mysession"] [--buffer=1] [--dir="path"] [-q]
  rmte serve [--mode=standalone|hybrid|relay] [--port=%d] [--pass="secret"] [--id="mysession"]
             [--buffer=1] [--dir="path"] [--web-path="/"] [--ws-path="%s"] [--hostname="example.com"]
             [--public] [--no-web | --no-cli] [--web-preview] [-q | --quiet]
  rmte join  --server-relay="ws://relay:%d%s" --id="..." --pass="secret" [--name="name"]
  rmte stop  <session_id | pid>
  rmte help | version

Serve modes:
  standalone  Relay + local host. External hosts (rmte share) are rejected. (default)
  hybrid      Relay + local host. External hosts are accepted.
  relay       Relay only. No local PTY, no session ID.

Notes:
  * If --pass is empty, a random password is generated and printed.
  * If --id is specified, must be lowercase alphanumeric (a-z, 0-9) up to 10 chars.
  * Web Browser Preview is enabled by default.
  * Default bind for serve is 127.0.0.1; use --public to bind 0.0.0.0.
`, appVersion, defaultPublicRelay, defaultPublicRelay, defaultPort, defaultWSPath, defaultPort, defaultWSPath)
}
