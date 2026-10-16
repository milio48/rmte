package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

var (
	// appVersion is set at build time via: -ldflags "-X main.appVersion=..."
	// Defaults to "dev" for local builds without build tags.
	appVersion = "dev"
)

func init() {
	setupDNSFallback()
	setupTLSConfig(false)

	if appVersion == "dev" {
		if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			appVersion = strings.TrimPrefix(bi.Main.Version, "v")
		}
	} else {
		appVersion = strings.TrimPrefix(appVersion, "v")
	}
}

// shouldBypassPhantomDNS determines if address is Go's hardcoded defaultNS phantom
// ("127.0.0.1" or "::1") caused by missing or empty nameserver configuration in /etc/resolv.conf.
func shouldBypassPhantomDNS(address string, hasSystemResolvConf bool) bool {
	host, _, splitErr := net.SplitHostPort(address)
	isPhantomHost := splitErr == nil && (host == "127.0.0.1" || host == "::1")
	return isPhantomHost && !hasSystemResolvConf
}

// hasConfiguredNameservers checks if the given resolv.conf path exists and contains
// at least one valid "nameserver <IP>" directive (avoiding phantoms in minimal/empty containers).
func hasConfiguredNameservers(resolvPath string) bool {
	data, err := os.ReadFile(resolvPath)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "nameserver ") || strings.HasPrefix(line, "nameserver\t") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && net.ParseIP(fields[1]) != nil {
				return true
			}
		}
	}
	return false
}

// setupDNSFallback ensures reliable DNS resolution on environments like Android, Termux,
// or minimal containers where /etc/resolv.conf is missing/empty and Go defaults to unreachable [::1]:53 or 127.0.0.1:53.
func setupDNSFallback() {
	defaultDialer := &net.Dialer{Timeout: 3 * time.Second}
	hasSystemResolv := hasConfiguredNameservers("/etc/resolv.conf")

	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			if !shouldBypassPhantomDNS(address, hasSystemResolv) {
				conn, err := defaultDialer.DialContext(ctx, network, address)
				if err == nil {
					return conn, nil
				}
			}

			// Fallback to trusted public DNS: Cloudflare (1.1.1.1:53) then Google (8.8.8.8:53)
			conn, err := defaultDialer.DialContext(ctx, "udp", "1.1.1.1:53")
			if err == nil {
				return conn, nil
			}
			return defaultDialer.DialContext(ctx, "udp", "8.8.8.8:53")
		},
	}
}

const (
	protocolVersion = "0.6"

	defaultPort        = 8048
	defaultWSPath      = "/ws-rmte"
	defaultPublicRelay = "wss://my.rmte.biz.id/ws-rmte"
)

// Serve modes
const (
	modeStandalone = "standalone" // relay + local host, external hosts rejected
	modeHybrid     = "hybrid"     // relay + local host, external hosts accepted
	modeRelay      = "relay"      // relay only, no local PTY
	modeShare      = "share"      // share client host connected to external relay
)

type CredentialSource int

const (
	CredSourceFlag CredentialSource = iota
	CredSourceFile
	CredSourceEnv
	CredSourceDefault
)

func resolveCredential(flagVal string, flagSet bool, passFile string, envKey string, defaultVal string) (string, CredentialSource, error) {
	if flagSet && flagVal != "" {
		return flagVal, CredSourceFlag, nil
	}
	if passFile != "" {
		st, err := os.Stat(passFile)
		if err != nil {
			return "", CredSourceFile, fmt.Errorf("failed to stat credential file %q: %w", passFile, err)
		}
		if runtime.GOOS != "windows" {
			perm := st.Mode().Perm()
			if perm&0077 != 0 {
				fmt.Fprintf(os.Stderr, "⚠️  [Security] Warning: permissions on credential file %q are %#o (recommended: 0600)\n", passFile, perm)
			}
		}
		data, err := os.ReadFile(passFile)
		if err != nil {
			return "", CredSourceFile, fmt.Errorf("failed to read credential file %q: %w", passFile, err)
		}
		val := strings.TrimSpace(string(data))
		if val == "" {
			return "", CredSourceFile, fmt.Errorf("credential file %q is empty", passFile)
		}
		return val, CredSourceFile, nil
	}
	if envKey != "" {
		if envVal := os.Getenv(envKey); envVal != "" {
			return envVal, CredSourceEnv, nil
		}
	}
	return defaultVal, CredSourceDefault, nil
}

// ServeConfig holds all options for `rmte serve`.
type ServeConfig struct {
	Mode          string
	Port          int
	Pass          string
	PassGenerated bool
	PassSource    CredentialSource
	Buffer        int
	WebPath       string
	WSPath        string
	PublicURL     string
	Public        bool
	NoWeb         bool
	NoCLI         bool
	Preview       bool   // enable Embedded Web Browser Preview reverse proxy
	AllowExec     bool   // allow remote command execution via MCP or CLI exec
	Quiet         bool   // run in background (daemon) and print startup banner + PID
	Dir           string // initial working directory for host
	ID            string // custom session ID for host (optional)
	InternalToken string // used by the embedded host in standalone mode
	AdminPath     string // HTTP path for Admin Dashboard (e.g. /admin or /admin-rmte)
	AdminPass     string // Password for Admin Dashboard (disabled if empty)
	Insecure      bool   // Skip TLS certificate verification
	Sandbox       bool   // Restrict file manager and MCP file tools to Dir root
	MaxBuffer     int    // Max buffer limit in MB per session (0 = unlimited / opt-in)
	MaxSessions   int    // Max concurrent active sessions (0 = unlimited)
	MaxViewers    int    // Max viewer connections per session
	MaxChat       int    // Max chat history messages retained in memory on host (default 50; 0 to disable)
}

var origArgs []string

func main() {
	cleanOldBinary()
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
		runShare(os.Args[1:])
	case "join":
		cmdJoin()
	case "exec":
		cmdExec()
	case "mcp":
		cmdMCP()
	case "stop":
		cmdStop()
	case "update", "upgrade":
		cmdUpdate()
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
	var passFile string
	flag.StringVar(&passFile, "pass-file", "", "Path to file containing session password")
	flag.IntVar(&cfg.Buffer, "buffer", 1, "Max buffer size in MB (ignored in relay mode)")
	flag.IntVar(&cfg.MaxBuffer, "max-buffer", 0, "Max buffer limit in MB per session (0 = unlimited; relay multi-tenant recommended)")
	flag.IntVar(&cfg.MaxSessions, "max-sessions", 100, "Max concurrent active sessions (0 = unlimited)")
	flag.IntVar(&cfg.MaxViewers, "max-viewers", 50, "Max viewer connections per session (0 = unlimited)")
	flag.IntVar(&cfg.MaxChat, "max-chat", DefaultMaxChat, "Max chat history retained in memory on host (default 50; 0 to disable)")
	flag.StringVar(&cfg.WebPath, "web-path", "/", "HTTP path for the Web UI")
	flag.StringVar(&cfg.WSPath, "ws-path", defaultWSPath, "WebSocket path")
	flag.StringVar(&cfg.PublicURL, "public-url", "", "Full public base URL or domain (e.g. https://my.rmte.biz.id or http://host:8041)")
	flag.StringVar(&cfg.PublicURL, "url", "", "Alias for --public-url")
	flag.StringVar(&cfg.Dir, "dir", "", "Initial working directory for File Explorer and terminal")
	flag.StringVar(&cfg.ID, "id", "", "Custom Session ID (lowercase a-z, 0-9, max 10 chars; random if empty)")
	flag.BoolVar(&cfg.Public, "public", false, "Bind to 0.0.0.0 (default 127.0.0.1)")
	flag.BoolVar(&cfg.NoWeb, "no-web", false, "Do not serve the Web UI")
	flag.BoolVar(&cfg.NoCLI, "no-cli", false, "Reject CLI clients (soft restriction)")
	flag.BoolVar(&cfg.Preview, "web-preview", false, "Enable Embedded Web Browser Preview reverse proxy")
	flag.BoolVar(&cfg.Preview, "preview", false, "Alias for --web-preview")
	flag.BoolVar(&cfg.AllowExec, "allow-exec", false, "Allow remote command execution via MCP or CLI exec")
	flag.BoolVar(&cfg.AllowExec, "enable-ai", false, "Alias for --allow-exec")
	flag.BoolVar(&cfg.Sandbox, "sandbox", false, "Restrict file manager and MCP file tools to project directory")
	flag.BoolVar(&cfg.Sandbox, "dir-only", false, "Alias for --sandbox")
	flag.BoolVar(&cfg.Quiet, "quiet", false, "Run in background (detached) and print connection info with PID")
	flag.BoolVar(&cfg.Quiet, "q", false, "Alias for --quiet")
	flag.StringVar(&cfg.AdminPath, "admin-path", "/admin", "HTTP path for Relay Admin Dashboard")
	flag.StringVar(&cfg.AdminPath, "path-admin", "/admin", "Alias for --admin-path")
	flag.StringVar(&cfg.AdminPass, "admin-pass", "", "Password for Relay Admin Dashboard (disabled if empty)")
	flag.StringVar(&cfg.AdminPass, "password-admin", "", "Alias for --admin-pass")
	flag.BoolVar(&cfg.Insecure, "insecure", false, "Skip TLS certificate verification")
	flag.BoolVar(&cfg.Insecure, "skip-tls-verify", false, "Alias for --insecure")
	flag.Parse()

	if cfg.Insecure {
		setupTLSConfig(true)
	}

	if cfg.Quiet && !isDaemonChild() {
		runAsDaemon()
	}

	passSet, bufferSet, idSet, adminPassSet := false, false, false, false
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "pass":
			passSet = true
		case "buffer":
			bufferSet = true
		case "id":
			idSet = true
		case "admin-pass", "password-admin":
			adminPassSet = true
		}
	})

	resolvedPass, passSource, err := resolveCredential(cfg.Pass, passSet, passFile, "RMTE_PASS", "")
	if err != nil {
		fatalf("Error resolving session password: %v", err)
	}
	cfg.Pass = resolvedPass
	cfg.PassSource = passSource

	resolvedID, _, err := resolveCredential(cfg.ID, idSet, "", "RMTE_ID", "")
	if err != nil {
		fatalf("Error resolving session ID: %v", err)
	}
	cfg.ID = resolvedID

	resolvedAdminPass, _, err := resolveCredential(cfg.AdminPass, adminPassSet, "", "RMTE_ADMIN_PASS", "")
	if err != nil {
		fatalf("Error resolving admin password: %v", err)
	}
	cfg.AdminPass = resolvedAdminPass

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
	if cfg.MaxChat < 0 {
		fatalf("Error: --max-chat must be >= 0")
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

	if cfg.AdminPass != "" {
		cfg.AdminPath = strings.TrimSpace(cfg.AdminPath)
		if !strings.HasPrefix(cfg.AdminPath, "/") {
			cfg.AdminPath = "/" + cfg.AdminPath
		}
		cfg.AdminPath = strings.TrimSuffix(cfg.AdminPath, "/")
		if cfg.AdminPath == "" {
			cfg.AdminPath = "/admin"
		}
		if !cfg.NoWeb && (cfg.AdminPath == strings.TrimSuffix(cfg.WebPath, "/") || cfg.AdminPath+"/" == cfg.WebPath) {
			fatalf("Error: --admin-path and --web-path must differ")
		}
		if cfg.AdminPath == cfg.WSPath || cfg.AdminPath == cfg.WSPath+"-proxy" {
			fatalf("Error: --admin-path and --ws-path must differ")
		}
		if cfg.AdminPath == "/p" || cfg.AdminPath == "/p/" {
			fatalf("Error: --admin-path cannot be %q (reserved for Web Browser Preview)", cfg.AdminPath)
		}
		if cfg.AdminPath == "/favicon.ico" || cfg.AdminPath == "/favicon.ico/" {
			fatalf("Error: --admin-path cannot conflict with /favicon.ico")
		}
		cfgJSONPath := strings.TrimSuffix(cfg.WebPath, "/") + "/config.json"
		if cfg.AdminPath == "/config.json" || (!cfg.NoWeb && cfg.AdminPath == cfgJSONPath) {
			fatalf("Error: --admin-path cannot conflict with web configuration endpoint")
		}
	}

	if cfg.Mode == modeRelay {
		if passSet || bufferSet || passFile != "" {
			fmt.Println("Warning: --pass and --buffer are ignored in relay mode (they belong to hosts).")
		}
		cfg.Pass = ""
	} else if cfg.Pass == "" {
		cfg.Pass = generatePassword(12)
		cfg.PassGenerated = true
		cfg.PassSource = CredSourceDefault
	} else if cfg.PassSource == CredSourceEnv {
		fmt.Printf("⚠️  [Auth] Using pre-set password from RMTE_PASS environment variable (NOT random).\n")
	}

	if cfg.Mode == modeStandalone {
		cfg.InternalToken = generatePassword(32)
	}

	if cfg.Mode == modeRelay {
		instanceID := "relay"
		if cfg.ID != "" {
			instanceID = cfg.ID
		}
		pidFile := fmt.Sprintf("rmte-%s.pid", instanceID)
		_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0644)
		meta := SessionMeta{
			SessionID: instanceID,
			Mode:      modeRelay,
			PID:       os.Getpid(),
			Port:      cfg.Port,
			Public:    cfg.Public,
			PublicURL: cfg.PublicURL,
			WebPath:   cfg.WebPath,
			WSPath:    cfg.WSPath,
			AdminPath: cfg.AdminPath,
			AdminPass: cfg.AdminPass,
			IsDaemon:  isDaemonChild(),
		}
		_ = saveSessionMeta(meta)
		defer func() {
			_ = os.Remove(pidFile)
			removeSessionMeta(instanceID)
		}()

		printBanner(bannerFromServe(&cfg, ""))
		markDaemonReady("")
		runServer(&cfg)
		return
	}
	runServeAndShare(&cfg)
}

func parseShareOptions(args []string) (HostOptions, error) {
	if len(args) > 0 && args[0] == "share" {
		args = args[1:]
	}
	fs := flag.NewFlagSet("share", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server-relay", defaultPublicRelay, "Relay server WebSocket URL")
	serverAlias := fs.String("server", "", "Alias for --server-relay")
	pass := fs.String("pass", "", "Password for E2EE (random if empty)")
	password := fs.String("password", "", "Alias for --pass")
	passFile := fs.String("pass-file", "", "Path to file containing session password")
	id := fs.String("id", "", "Custom Session ID (lowercase a-z, 0-9, max 10 chars; random if empty)")
	bufferMB := fs.Int("buffer", 1, "Max buffer size in MB (terminal ring buffer and file manager)")
	maxChat := fs.Int("max-chat", DefaultMaxChat, "Max chat history messages retained in memory on host (default 50; 0 to disable)")
	dir := fs.String("dir", "", "Initial working directory for File Explorer and terminal")
	webPreview := fs.Bool("web-preview", true, "Enable Embedded Web Browser Preview reverse proxy")
	preview := fs.Bool("preview", true, "Alias for --web-preview")
	allowExec := fs.Bool("allow-exec", false, "Allow remote command execution via MCP or CLI exec")
	enableAI := fs.Bool("enable-ai", false, "Alias for --allow-exec")
	sandbox := fs.Bool("sandbox", false, "Restrict file manager and MCP file tools to project directory")
	dirOnly := fs.Bool("dir-only", false, "Alias for --sandbox")
	quiet := fs.Bool("quiet", false, "Run in background (detached) and print connection info with PID")
	q := fs.Bool("q", false, "Alias for --quiet")
	insecure := fs.Bool("insecure", false, "Skip TLS certificate verification")
	skipTLS := fs.Bool("skip-tls-verify", false, "Alias for --insecure")

	if len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return HostOptions{}, err
		}
	}

	relayURL := *server
	if *serverAlias != "" {
		relayURL = *serverAlias
	}

	chosenPass := *pass
	if *password != "" {
		chosenPass = *password
	}

	passSet, idSet, serverSet := false, false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "pass", "password":
			passSet = true
		case "id":
			idSet = true
		case "server-relay", "server":
			serverSet = true
		}
	})

	resolvedPass, passSource, err := resolveCredential(chosenPass, passSet, *passFile, "RMTE_PASS", "")
	if err != nil {
		return HostOptions{}, fmt.Errorf("error resolving session password: %w", err)
	}

	resolvedID, _, err := resolveCredential(*id, idSet, "", "RMTE_ID", "")
	if err != nil {
		return HostOptions{}, fmt.Errorf("error resolving session ID: %w", err)
	}

	resolvedServer, _, _ := resolveCredential(relayURL, serverSet, "", "RMTE_SERVER_RELAY", defaultPublicRelay)
	if !serverSet && resolvedServer == defaultPublicRelay {
		if envS := os.Getenv("RMTE_SERVER"); envS != "" {
			resolvedServer = envS
		}
	}
	relayURL = resolvedServer

	if *bufferMB < 1 {
		return HostOptions{}, fmt.Errorf("--buffer must be >= 1")
	}
	if *maxChat < 0 {
		return HostOptions{}, fmt.Errorf("--max-chat must be >= 0")
	}

	var cleanID string
	if resolvedID != "" {
		cleanID = strings.ToLower(strings.TrimSpace(resolvedID))
		if err := validateSessionID(cleanID); err != nil {
			return HostOptions{}, fmt.Errorf("invalid --id: %w", err)
		}
	}

	var cleanDir string
	if *dir != "" {
		absDir, err := filepath.Abs(*dir)
		if err != nil {
			return HostOptions{}, fmt.Errorf("invalid --dir=%q: %w", *dir, err)
		}
		st, err := os.Stat(absDir)
		if err != nil || !st.IsDir() {
			return HostOptions{}, fmt.Errorf("--dir=%q does not exist or is not a directory", *dir)
		}
		cleanDir = filepath.ToSlash(absDir)
	}

	opts := HostOptions{
		DialURL:    relayURL,
		PublicURL:  relayURL,
		Pass:       resolvedPass,
		PassSource: passSource,
		Buffer:     *bufferMB,
		MaxChat:    *maxChat,
		Mode:       modeShare,
		Dir:        cleanDir,
		ID:         cleanID,
		Preview:    *webPreview && *preview,
		AllowExec:  *allowExec || *enableAI,
		Sandbox:    *sandbox || *dirOnly,
		Insecure:   *insecure || *skipTLS,
		Quiet:      *quiet || *q,
	}
	if opts.Pass == "" {
		opts.Pass = generatePassword(12)
		opts.PassGenerated = true
		opts.PassSource = CredSourceDefault
	}
	return opts, nil
}

func runShare(args []string) {
	opts, err := parseShareOptions(args)
	if err != nil {
		fatalf("Error: %v", err)
	}
	if opts.Insecure {
		setupTLSConfig(true)
	}
	if opts.Quiet && !isDaemonChild() {
		runAsDaemon()
	}
	if opts.PassSource == CredSourceEnv {
		fmt.Printf("⚠️  [Auth] Using pre-set password from RMTE_PASS environment variable (NOT random).\n")
	}
	runHost(opts)
}

func cmdJoin() {
	fs := flag.NewFlagSet("join", flag.ExitOnError)
	server := fs.String("server-relay", defaultPublicRelay, "Relay server WebSocket URL")
	serverAlias := fs.String("server", "", "Alias for --server-relay")
	sessionID := fs.String("id", "", "Session ID to join")
	pass := fs.String("pass", "", "Password for E2EE")
	password := fs.String("password", "", "Alias for --pass")
	passFile := fs.String("pass-file", "", "Path to file containing session password")
	name := fs.String("name", "", "Display name of the viewer")
	insecure := fs.Bool("insecure", false, "Skip TLS certificate verification")
	skipTLS := fs.Bool("skip-tls-verify", false, "Alias for --insecure")

	_ = fs.Parse(os.Args[1:])

	if *insecure || *skipTLS {
		setupTLSConfig(true)
	}

	relayURL := *server
	if *serverAlias != "" {
		relayURL = *serverAlias
	}
	chosenPass := *pass
	if *password != "" {
		chosenPass = *password
	}

	passSet, idSet, serverSet := false, false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "pass", "password":
			passSet = true
		case "id":
			idSet = true
		case "server-relay", "server":
			serverSet = true
		}
	})

	resolvedPass, _, err := resolveCredential(chosenPass, passSet, *passFile, "RMTE_PASS", "")
	if err != nil {
		fatalf("Error resolving session password: %v", err)
	}
	resolvedID, _, err := resolveCredential(*sessionID, idSet, "", "RMTE_ID", "")
	if err != nil {
		fatalf("Error resolving session ID: %v", err)
	}
	resolvedServer, _, _ := resolveCredential(relayURL, serverSet, "", "RMTE_SERVER_RELAY", defaultPublicRelay)
	if !serverSet && resolvedServer == defaultPublicRelay {
		if envS := os.Getenv("RMTE_SERVER"); envS != "" {
			resolvedServer = envS
		}
	}

	if resolvedID == "" || resolvedPass == "" {
		fatalf("Error: session ID and password are required for rmte join (set via --id/--pass, --pass-file, or RMTE_ID/RMTE_PASS)")
	}
	runViewer(resolvedServer, resolvedID, resolvedPass, *name)
}

func cmdExec() {
	fs := flag.NewFlagSet("exec", flag.ExitOnError)
	server := fs.String("server-relay", defaultPublicRelay, "Relay server WebSocket URL")
	serverAlias := fs.String("server", "", "Alias for --server-relay")
	sessionID := fs.String("id", "", "Target session ID")
	pass := fs.String("pass", "", "Session password for E2EE")
	password := fs.String("password", "", "Alias for --pass")
	passFile := fs.String("pass-file", "", "Path to file containing session password")
	name := fs.String("name", "rmte-exec", "Client name")
	timeout := fs.Int("timeout", 60, "Execution timeout in seconds")
	background := fs.Bool("background", false, "Run command in a dedicated background tab")
	bg := fs.Bool("bg", false, "Alias for --background")
	tabName := fs.String("tab-name", "", "Custom tab title when running in background")
	tabNameAlias := fs.String("name-tab", "", "Alias for --tab-name")
	insecure := fs.Bool("insecure", false, "Skip TLS certificate verification")
	skipTLS := fs.Bool("skip-tls-verify", false, "Alias for --insecure")

	_ = fs.Parse(os.Args[1:])

	relayURL := *server
	if *serverAlias != "" {
		relayURL = *serverAlias
	}
	chosenPass := *pass
	if *password != "" {
		chosenPass = *password
	}

	passSet, idSet, serverSet := false, false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "pass", "password":
			passSet = true
		case "id":
			idSet = true
		case "server-relay", "server":
			serverSet = true
		}
	})

	resolvedPass, _, err := resolveCredential(chosenPass, passSet, *passFile, "RMTE_PASS", "")
	if err != nil {
		fatalf("Error resolving session password: %v", err)
	}
	resolvedID, _, err := resolveCredential(*sessionID, idSet, "", "RMTE_ID", "")
	if err != nil {
		fatalf("Error resolving session ID: %v", err)
	}
	resolvedServer, _, _ := resolveCredential(relayURL, serverSet, "", "RMTE_SERVER_RELAY", defaultPublicRelay)
	if !serverSet && resolvedServer == defaultPublicRelay {
		if envS := os.Getenv("RMTE_SERVER"); envS != "" {
			resolvedServer = envS
		}
	}

	if resolvedID == "" || resolvedPass == "" {
		fatalf("Error: session ID and password are required for rmte exec (set via --id/--pass, --pass-file, or RMTE_ID/RMTE_PASS)")
	}

	chosenTabName := *tabName
	if *tabNameAlias != "" {
		chosenTabName = *tabNameAlias
	}

	cmdArgs := fs.Args()
	if len(cmdArgs) == 0 {
		fatalf("Error: command argument is required (e.g. rmte exec --id=... --pass=... -- ls -la)")
	}
	cmdStr := strings.Join(cmdArgs, " ")

	exitCode := runExec(resolvedServer, resolvedID, resolvedPass, *name, cmdStr, *timeout, *background || *bg, chosenTabName, *insecure || *skipTLS)
	os.Exit(exitCode)
}

func cmdMCP() {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	server := fs.String("server-relay", defaultPublicRelay, "Relay server WebSocket URL")
	serverAlias := fs.String("server", "", "Alias for --server-relay")
	sessionID := fs.String("id", "", "Target session ID")
	pass := fs.String("pass", "", "Session password for E2EE")
	password := fs.String("password", "", "Alias for --pass")
	passFile := fs.String("pass-file", "", "Path to file containing session password")
	name := fs.String("name", "Claude-MCP", "Client name")
	insecure := fs.Bool("insecure", false, "Skip TLS certificate verification")
	skipTLS := fs.Bool("skip-tls-verify", false, "Alias for --insecure")

	_ = fs.Parse(os.Args[1:])

	relayURL := *server
	if *serverAlias != "" {
		relayURL = *serverAlias
	}
	chosenPass := *pass
	if *password != "" {
		chosenPass = *password
	}

	passSet, idSet, serverSet := false, false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "pass", "password":
			passSet = true
		case "id":
			idSet = true
		case "server-relay", "server":
			serverSet = true
		}
	})

	resolvedPass, _, err := resolveCredential(chosenPass, passSet, *passFile, "RMTE_PASS", "")
	if err != nil {
		fatalf("Error resolving session password: %v", err)
	}
	resolvedID, _, err := resolveCredential(*sessionID, idSet, "", "RMTE_ID", "")
	if err != nil {
		fatalf("Error resolving session ID: %v", err)
	}
	resolvedServer, _, _ := resolveCredential(relayURL, serverSet, "", "RMTE_SERVER_RELAY", defaultPublicRelay)
	if !serverSet && resolvedServer == defaultPublicRelay {
		if envS := os.Getenv("RMTE_SERVER"); envS != "" {
			resolvedServer = envS
		}
	}

	if resolvedID == "" || resolvedPass == "" {
		fatalf("Error: session ID and password are required for rmte mcp (set via --id/--pass, --pass-file, or RMTE_ID/RMTE_PASS)")
	}

	runMCP(resolvedServer, resolvedID, resolvedPass, *name, *insecure || *skipTLS)
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

	if cfg.MaxBuffer > 0 && cfg.Buffer > cfg.MaxBuffer {
		fmt.Printf("\n\033[33m⚠️  [Buffer] Standalone relay clamped buffer to %d MB (requested: %d MB via --buffer). Terminal scrollback and file transfers are capped accordingly.\033[0m\n", cfg.MaxBuffer, cfg.Buffer)
		cfg.Buffer = cfg.MaxBuffer
	}

	relayURL, _ := resolvePublicRelayURL(cfg.PublicURL, cfg.Port, cfg.WSPath)
	runHost(HostOptions{
		DialURL:       fmt.Sprintf("ws://127.0.0.1:%d%s", cfg.Port, cfg.WSPath),
		PublicURL:     relayURL,
		Pass:          cfg.Pass,
		PassGenerated: cfg.PassGenerated,
		Buffer:        cfg.Buffer,
		MaxChat:       cfg.MaxChat,
		InternalToken: cfg.InternalToken,
		Mode:          cfg.Mode,
		Dir:           cfg.Dir,
		ID:            cfg.ID,
		Preview:       cfg.Preview,
		AllowExec:     cfg.AllowExec,
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
  rmte --allow-exec                     Enable AI agent execution (MCP & CLI exec)

Usage:
  rmte       [--pass="secret"] [--id="mysession"] [--dir="path"] [--buffer=1] [--allow-exec] [-q] [--max-chat=50]
  rmte share [--server-relay="%s"] [--pass="secret"] [--id="mysession"] [--buffer=1] [--dir="path"] [--allow-exec] [-q] [--max-chat=50]
  rmte serve [--mode=standalone|hybrid|relay] [--port=%d] [--pass="secret"] [--id="mysession"]
             [--buffer=1] [--dir="path"] [--web-path="/"] [--ws-path="%s"] [--public-url="https://example.com"]
             [--public] [--no-web | --no-cli] [--web-preview] [--allow-exec] [-q | --quiet]
             [--admin-path="/admin"] [--admin-pass="secret"]
             [--max-buffer=0] [--max-sessions=100] [--max-viewers=50] [--max-chat=50]
  rmte join  [--server-relay="%s"] --id="..." --pass="secret" [--name="name"]
  rmte exec  [--server-relay="%s"] --id="..." --pass="secret" [--timeout=60] [--bg] -- <command>
  rmte mcp   [--server-relay="%s"] --id="..." --pass="secret" [--name="Claude-MCP"]
  rmte stop  <session_id | pid>
  rmte update [--check] [--force] [--restart]
  rmte help | version

Serve modes:
  standalone  Relay + local host. External hosts (rmte share) are rejected. (default)
  hybrid      Relay + local host. External hosts are accepted.
  relay       Relay only. No local PTY, no session ID.

Notes:
  * If --pass is empty, a random password is generated and printed.
  * If --id is specified, must be lowercase alphanumeric (a-z, 0-9) up to 10 chars.
  * Web Browser Preview is enabled by default for share (use --web-preview for serve).
  * Remote execution (exec/mcp) requires host started with --allow-exec.
  * Default bind for serve is 127.0.0.1; use --public to bind 0.0.0.0.
`, appVersion, defaultPublicRelay, defaultPublicRelay, defaultPort, defaultWSPath, defaultPublicRelay, defaultPublicRelay, defaultPublicRelay)
}
