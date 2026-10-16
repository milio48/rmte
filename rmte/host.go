package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type SafeConn struct {
	*websocket.Conn
	mu sync.Mutex
	// ctl, when set, seals every WriteJSON in an end-to-end control envelope.
	// It is attached only after the relay handshake, which stays plaintext.
	ctl *ctlChannel
}

func (c *SafeConn) WriteMessage(messageType int, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn.WriteMessage(messageType, data)
}

func (c *SafeConn) WriteJSON(v interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctl == nil {
		return c.Conn.WriteJSON(v)
	}
	// Seal under the write lock so sequence numbers hit the wire in order.
	payload, err := c.ctl.seal(v)
	if err != nil || payload == nil {
		return err
	}
	return c.Conn.WriteMessage(websocket.TextMessage, payload)
}

func (c *SafeConn) WriteControl(messageType int, data []byte, deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn.WriteControl(messageType, data, deadline)
}

type TabSession struct {
	Title         string
	IsAI          bool
	AIProcessDone chan struct{}
	aiDoneOnce    sync.Once
	AIListenerMu  sync.Mutex
	AIListener    chan []byte
	AIDoneChan    chan struct{}
	Cmd           *exec.Cmd
	Pid           int
	ReadCloser    io.ReadCloser
	WriteCloser   io.WriteCloser
	IsPipe        bool
	Buffer        []byte
	Mutex         sync.Mutex

	// LineBuffer for Windows Pipe mode to emulate PTY backspace
	LineBuffer []byte
	Resizer    func(cols, rows int) error
	CloseFn    func() error
	closeOnce  sync.Once
	waitOnce   sync.Once
}

func (t *TabSession) waitCmd() {
	if t == nil || t.Cmd == nil {
		return
	}
	t.waitOnce.Do(func() {
		_ = t.Cmd.Wait()
	})
}

func (t *TabSession) notifyAIDone() {
	if t == nil {
		return
	}
	t.aiDoneOnce.Do(func() {
		if t.AIProcessDone != nil {
			close(t.AIProcessDone)
		}
	})
}

func (t *TabSession) Close() {
	if t == nil {
		return
	}
	t.closeOnce.Do(func() {
		if t.CloseFn != nil {
			t.CloseFn()
		} else {
			if t.Cmd != nil && t.Cmd.Process != nil {
				t.Cmd.Process.Kill()
			}
			if t.ReadCloser != nil {
				t.ReadCloser.Close()
			}
			if t.WriteCloser != nil && any(t.WriteCloser) != any(t.ReadCloser) {
				t.WriteCloser.Close()
			}
		}
		t.waitCmd()
		if t.IsAI {
			t.notifyAIDone()
		}
	})
}

type ViewersPresence struct {
	ViewerName string
	TabID      byte
	IsBot      bool
}

// File manager state for Tab 255 data channel
type PendingSave struct {
	Path       string
	TargetConn string
	TransferID uint32
}

const dataChannelTabID byte = 255
const lobbyTabID byte = 255 // Sentinel Tab ID representing users in Menu / Lobby (unfocused)

var (
	tabs   = make(map[byte]*TabSession)
	tabsMu sync.RWMutex

	nextTabID byte = 1 // S3: incrementing counter (0 is initial tab)

	presenceMap   = make(map[string]ViewersPresence)
	presenceMutex sync.Mutex

	// Dynamic buffer limit (set by --buffer flag; thread-safe via atomic)
	maxBufferSize atomic.Int64
)

func getMaxBufferSize() int {
	return int(maxBufferSize.Load())
}

func setMaxBufferSize(bytes int) {
	maxBufferSize.Store(int64(bytes))
}

var (

	// Host sandbox restriction (--sandbox flag)
	hostSandbox bool

	// Pending file saves keyed by transfer id: when a viewer sends prepare_save,
	// we register what path the next Tab 255 binary frame with that id should write to.
	pendingSaves  = make(map[uint32]*PendingSave)
	pendingSaveMu sync.Mutex

	// Host working directory (sandbox root for file operations)
	hostWorkDir string

	// Event log file and memory history
	eventLogFile   *os.File
	eventLogMu     sync.Mutex
	eventHistory   []map[string]interface{}
	eventHistoryMu sync.RWMutex

	// Chat history (retained in memory on host, capped by HostOptions.MaxChat)
	hostChatHistory   []map[string]interface{}
	hostChatHistoryMu sync.RWMutex

	// Active host WebSocket connection (supports seamless reconnect)
	currentHostConnMu sync.RWMutex
	currentHostConn   *SafeConn
)

func setHostConn(c *SafeConn) {
	currentHostConnMu.Lock()
	currentHostConn = c
	currentHostConnMu.Unlock()
}

func getHostConn() *SafeConn {
	currentHostConnMu.RLock()
	defer currentHostConnMu.RUnlock()
	return currentHostConn
}

const DefaultMaxChat = 50

func appendHostChatHistory(ctrl map[string]interface{}) {
	maxChat := getHostOptions().MaxChat
	if maxChat <= 0 {
		return
	}
	hostChatHistoryMu.Lock()
	defer hostChatHistoryMu.Unlock()
	hostChatHistory = append(hostChatHistory, ctrl)
	if len(hostChatHistory) > maxChat {
		hostChatHistory = hostChatHistory[len(hostChatHistory)-maxChat:]
	}
}

func getHostChatHistory() []map[string]interface{} {
	hostChatHistoryMu.RLock()
	defer hostChatHistoryMu.RUnlock()
	chats := make([]map[string]interface{}, len(hostChatHistory))
	copy(chats, hostChatHistory)
	return chats
}

// HostOptions configures a host session (used by `serve` and `share`).
type HostOptions struct {
	DialURL       string // URL actually dialed (may be loopback)
	PublicURL     string // URL shown in printed links
	Pass          string
	PassGenerated bool
	PassSource    CredentialSource
	Buffer        int
	MaxChat       int // Max chat history retained on host (default 50; 0 disables retention)
	InternalToken string
	Mode          string       // standalone | hybrid | share
	Dir           string       // initial working directory
	ID            string       // custom session ID (optional)
	Preview       bool         // enable Embedded Web Browser Preview reverse proxy
	AllowExec     bool         // allow remote command execution via MCP or CLI exec
	Sandbox       bool         // restrict file manager and MCP file tools to Dir root
	Insecure      bool         // skip TLS certificate verification
	Quiet         bool         // run in background (daemon)
	Serve         *ServeConfig // non-nil when embedded in `serve`
}

var (
	currentHostOptionsMu sync.RWMutex
	currentHostOptions   HostOptions
)

func getHostOptions() HostOptions {
	currentHostOptionsMu.RLock()
	defer currentHostOptionsMu.RUnlock()
	return currentHostOptions
}

func setHostOptions(opts HostOptions) {
	currentHostOptionsMu.Lock()
	currentHostOptions = opts
	currentHostOptionsMu.Unlock()
}

func initEventLog(sessionID string) {
	exePath, err := os.Executable()
	var dir string
	if err == nil {
		dir = filepath.Dir(exePath)
	} else {
		dir = "."
	}
	logPath := filepath.Join(dir, fmt.Sprintf("rmte-%s.log", sessionID))
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("[EventLog] Failed to open %s: %v", logPath, err)
		return
	}
	eventLogFile = f
	log.Printf("[EventLog] Logging events to %s", logPath)
}

func logEvent(conn *SafeConn, eventType, user, message string) {
	now := time.Now()
	timeStr := now.Format("2006-01-02 15:04:05")
	timeShort := now.Format("15:04:05")
	entryLine := fmt.Sprintf("[%s] [%s] [%s] %s\n", timeStr, eventType, user, message)

	eventLogMu.Lock()
	if eventLogFile != nil {
		eventLogFile.WriteString(entryLine)
	}
	eventLogMu.Unlock()

	evt := map[string]interface{}{
		"time":    timeShort,
		"date":    timeStr,
		"type":    eventType,
		"user":    user,
		"message": message,
	}

	eventHistoryMu.Lock()
	eventHistory = append(eventHistory, evt)
	if len(eventHistory) > 100 {
		eventHistory = eventHistory[len(eventHistory)-100:]
	}
	eventHistoryMu.Unlock()

	if conn == nil {
		conn = getHostConn()
	}
	if conn != nil {
		conn.WriteJSON(map[string]interface{}{
			"type":   "control",
			"action": "event_log",
			"event":  evt,
		})
	}
}

func runHost(opts HostOptions) {
	if opts.MaxChat < 0 {
		opts.MaxChat = 0
	}
	setHostOptions(opts)
	setMaxBufferSize(opts.Buffer * 1024 * 1024)

	// Set working directory as sandbox root
	if opts.Dir != "" {
		hostWorkDir = opts.Dir
	} else {
		wd, err := os.Getwd()
		if err != nil {
			log.Fatal("Cannot get working directory:", err)
		}
		hostWorkDir = wd
	}
	hostWorkDir = filepath.ToSlash(hostWorkDir)
	hostSandbox = opts.Sandbox
	if hostSandbox {
		if canonicalRoot, err := filepath.EvalSymlinks(hostWorkDir); err == nil {
			hostWorkDir = filepath.ToSlash(canonicalRoot)
		}
	}

	u, err := url.Parse(opts.DialURL)
	if err != nil {
		log.Fatal(err)
	}

	rawConn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Fatal("Dial error:", err)
	}
	conn := &SafeConn{Conn: rawConn}
	defer conn.Close()

	if err := setupCrypto(opts.Pass); err != nil {
		log.Fatal(err)
	}

	// Auth as host
	auth := map[string]interface{}{
		"type":             "auth",
		"role":             "host",
		"auth_token":       generateAuthToken(opts.Pass),
		"protocol_version": protocolVersion,
		"preview":          opts.Preview,
		"buffer":           opts.Buffer,
	}
	if opts.InternalToken != "" {
		auth["internal_token"] = opts.InternalToken
	}
	if opts.ID != "" {
		auth["session_id"] = opts.ID
	}
	conn.WriteJSON(auth)

	// Wait for session ID
	var authResp struct {
		Type           string `json:"type"`
		SessionID      string `json:"session_id"`
		Message        string `json:"message"`
		WebPath        string `json:"web_path"`
		NoWeb          bool   `json:"no_web"`
		NoCLI          bool   `json:"no_cli"`
		PreviewEnabled bool   `json:"preview_enabled"`
		ProxySecret    string `json:"proxy_secret"`
		WSProxyPath    string `json:"ws_proxy_path"`
		ClampedBuffer  int    `json:"clamped_buffer"`
	}
	if err := conn.ReadJSON(&authResp); err != nil {
		log.Fatal("Auth failed: ", err)
	}
	if authResp.Type != "auth_success" {
		msg := authResp.Message
		if msg == "" {
			msg = "unexpected response from relay"
		}
		fatalf("Relay rejected host: %s", msg)
	}
	if authResp.WebPath == "" {
		authResp.WebPath = "/" // older relays
	}

	if authResp.ClampedBuffer > 0 && authResp.ClampedBuffer < opts.Buffer {
		fmt.Printf("\n\033[33m⚠️  [Buffer] Relay clamped buffer to %d MB (requested: %d MB). Terminal scrollback and file transfers are capped accordingly.\033[0m\n", authResp.ClampedBuffer, opts.Buffer)
		opts.Buffer = authResp.ClampedBuffer
		if opts.Serve != nil {
			opts.Serve.Buffer = authResp.ClampedBuffer
		}
		setHostOptions(opts)
		setMaxBufferSize(opts.Buffer * 1024 * 1024)
	}

	var info BannerInfo
	if opts.Serve != nil {
		info = bannerFromServe(opts.Serve, authResp.SessionID)
	} else {
		info = BannerInfo{
			Mode:          "share",
			RelayURL:      opts.PublicURL,
			WebPath:       authResp.WebPath,
			Pass:          opts.Pass,
			PassGenerated: opts.PassGenerated,
			PassSource:    opts.PassSource,
			SessionID:     authResp.SessionID,
			Buffer:        opts.Buffer,
			NoWeb:         authResp.NoWeb,
			NoCLI:         authResp.NoCLI,
			Preview:       opts.Preview,
			AllowExec:     opts.AllowExec,
			Dir:           opts.Dir,
			Sandbox:       opts.Sandbox,
		}
	}
	printBanner(info)

	// Launch secondary WebSocket proxy connection for preview if enabled
	if opts.Preview && authResp.PreviewEnabled && authResp.ProxySecret != "" {
		proxyPath := authResp.WSProxyPath
		if proxyPath == "" {
			proxyPath = "/ws-rmte-proxy"
		}
		go runHostProxy(opts, authResp.SessionID, authResp.ProxySecret, proxyPath)
	}

	// From here on every control message to viewers is sealed.
	hostCtl = newCtlChannel(aesGCM, authResp.SessionID, true)
	conn.ctl = hostCtl
	announceHostEpoch(conn)

	// Initialize Event Log file on Host
	initEventLog(authResp.SessionID)
	logEvent(conn, "HOST_START", "host", fmt.Sprintf("Host session %s active in %s", authResp.SessionID, hostWorkDir))

	// Signal parent if running in background daemon mode
	markDaemonReady(authResp.SessionID)
	meta := SessionMeta{
		SessionID:   authResp.SessionID,
		Password:    opts.Pass,
		ServerRelay: opts.DialURL,
		Dir:         hostWorkDir,
		Buffer:      opts.Buffer,
		Preview:     opts.Preview,
		PID:         os.Getpid(),
		Mode:        opts.Mode,
		IsDaemon:    isDaemonChild(),
	}
	if serverCfg != nil {
		meta.Port = serverCfg.Port
		meta.Public = serverCfg.Public
		meta.PublicURL = serverCfg.PublicURL
		meta.WebPath = serverCfg.WebPath
		meta.WSPath = serverCfg.WSPath
		meta.AdminPath = serverCfg.AdminPath
		meta.AdminPass = serverCfg.AdminPass
	}
	_ = saveSessionMeta(meta)

	pidFile := fmt.Sprintf("rmte-%s.pid", authResp.SessionID)
	_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0644)
	defer func() {
		_ = os.Remove(pidFile)
		removeSessionMeta(authResp.SessionID)
	}()

	setHostConn(conn)

	// Create initial tab (ID 0)
	createTab(0, conn)

	// Message loop with auto-reconnect
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[Host] Relay connection interrupted: %v", err)
			_ = conn.Close()
			setHostConn(nil)

			// Enter Reconnect Phase (120 seconds grace period)
			newConn, rerr := reconnectToRelay(opts, authResp.SessionID, 120*time.Second)
			if rerr != nil {
				log.Printf("[Host] Reconnect timeout (120s) exceeded: %v. Terminating host session.", rerr)
				break
			}

			conn = newConn
			setHostConn(conn)

			logEvent(conn, "HOST_RECONNECT", "host", fmt.Sprintf("Host session %s reconnected to relay", authResp.SessionID))
			continue
		}

		if mt == websocket.BinaryMessage {
			// Tab 255 = Data Channel for file uploads/saves (framed with a transfer id)
			if len(data) > 0 && data[0] == dataChannelTabID {
				transferID, plaintext, err := decryptDataChannel(dirToHost, data)
				if err != nil {
					continue
				}
				handleDataChannelWrite(transferID, plaintext, conn)
				continue
			}

			tabID, plaintext, err := decryptBinary(dirToHost, data)
			if err != nil {
				continue
			}

			tabsMu.RLock()
			tab, ok := tabs[tabID]
			tabsMu.RUnlock()

			if ok {
				if tab.IsAI {
					continue
				}
				if tab.IsPipe && runtime.GOOS == "windows" {
					// Emulate PTY line buffering for Windows pipe fallback
					for _, b := range plaintext {
						if b == '\r' || b == '\n' {
							// Echo newline
							payload, _ := encryptBinary(dirToViewer, tabID, []byte("\r\n"))
							conn.WriteMessage(websocket.BinaryMessage, payload)

							// Send to process
							tab.Mutex.Lock()
							tab.LineBuffer = append(tab.LineBuffer, '\n')
							sendData := make([]byte, len(tab.LineBuffer))
							copy(sendData, tab.LineBuffer)
							tab.LineBuffer = nil
							tab.Mutex.Unlock()

							tab.WriteCloser.Write(sendData)
						} else if b == '\x03' {
							// Ctrl+C (ETX): clear line buffer, echo ^C, and terminate child process
							tab.Mutex.Lock()
							tab.LineBuffer = nil
							tab.Mutex.Unlock()

							payload, _ := encryptBinary(dirToViewer, tabID, []byte("^C\r\n"))
							conn.WriteMessage(websocket.BinaryMessage, payload)

							tab.WriteCloser.Write([]byte{3})
							go interruptTabProcess(tab)
						} else if b == '\x7f' || b == '\x08' {
							// Backspace: remove last character and erase visually
							tab.Mutex.Lock()
							hasChars := len(tab.LineBuffer) > 0
							if hasChars {
								tab.LineBuffer = tab.LineBuffer[:len(tab.LineBuffer)-1]
							}
							tab.Mutex.Unlock()

							if hasChars {
								payload, _ := encryptBinary(dirToViewer, tabID, []byte("\b \b"))
								conn.WriteMessage(websocket.BinaryMessage, payload)
							}
						} else {
							// Normal character
							tab.Mutex.Lock()
							tab.LineBuffer = append(tab.LineBuffer, b)
							tab.Mutex.Unlock()

							payload, _ := encryptBinary(dirToViewer, tabID, []byte{b})
							conn.WriteMessage(websocket.BinaryMessage, payload)
						}
					}
				} else {
					tab.WriteCloser.Write(plaintext)
					if runtime.GOOS == "windows" {
						for _, b := range plaintext {
							if b == '\x03' {
								go interruptTabProcess(tab)
								break
							}
						}
					}
				}
			}
		} else if mt == websocket.TextMessage {
			if ctrl, ok := hostAcceptControl(conn, data); ok {
				action, _ := ctrl["action"].(string)
				switch action {
				case ctlHelloAction:
					// A viewer asks for the current epoch; any sealed reply carries it.
					targetConn, _ := ctrl["target_conn"].(string)
					conn.WriteJSON(map[string]interface{}{
						"type":        "control",
						"action":      ctlHelloAction,
						"target_conn": targetConn,
					})
				case "get_tabs":
					tabsMu.RLock()
					activeTabs := make([]int, 0)
					tabTitles := make(map[string]string)
					for id, tab := range tabs {
						activeTabs = append(activeTabs, int(id))
						if tab.Title != "" {
							tabTitles[strconv.Itoa(int(id))] = tab.Title
						} else {
							tabTitles[strconv.Itoa(int(id))] = fmt.Sprintf("Terminal %d", id)
						}
					}
					tabsMu.RUnlock()

					sort.Ints(activeTabs)

					conn.WriteJSON(map[string]interface{}{
						"type":   "control",
						"action": "tabs_list",
						"tabs":   activeTabs,
						"titles": tabTitles,
					})

					presenceMutex.Lock()
					tabsPresence := buildTabsPresence(presenceMap)
					botViewers := buildBotViewers(presenceMap)
					presenceMutex.Unlock()

					conn.WriteJSON(map[string]interface{}{
						"type":        "control",
						"action":      "presence",
						"tabs":        tabsPresence,
						"bot_viewers": botViewers,
					})
				case "request_new_tab":
					newID := nextTabID
					nextTabID++
					title, _ := ctrl["title"].(string)
					if title == "" {
						title = fmt.Sprintf("Terminal %d", newID)
					}
					createTabWithTitle(newID, conn, title, false)
					logEvent(conn, "NEW_TAB", "system", fmt.Sprintf("Terminal Tab %d (%s) created", newID, title))
					conn.WriteJSON(map[string]interface{}{
						"type":   "control",
						"action": "tab_created",
						"tab_id": newID,
						"title":  title,
					})
				case "resize":
					tabIDFloat, _ := ctrl["tab_id"].(float64)
					tabID := byte(tabIDFloat)
					colsFloat, _ := ctrl["cols"].(float64)
					rowsFloat, _ := ctrl["rows"].(float64)
					tabsMu.RLock()
					tab, ok := tabs[tabID]
					tabsMu.RUnlock()
					if ok && tab.Resizer != nil {
						tab.Resizer(int(colsFloat), int(rowsFloat))
					}
				case "req_sync":
					tabIDFloat, _ := ctrl["tab_id"].(float64)
					tabID := byte(tabIDFloat)
					targetConn, _ := ctrl["target_conn"].(string)

					tabsMu.RLock()
					tab, ok := tabs[tabID]
					tabsMu.RUnlock()
					if ok {
						tab.Mutex.Lock()
						history := make([]byte, len(tab.Buffer))
						copy(history, tab.Buffer)
						tab.Mutex.Unlock()

						payload, _ := encryptBinary(dirToViewer, tabID, history)
						encoded := base64.StdEncoding.EncodeToString(payload)
						conn.WriteJSON(map[string]interface{}{
							"type":        "control",
							"action":      "sync_data",
							"target_conn": targetConn,
							"data":        encoded,
						})
					}
				case "delete_tab":
					tabIDFloat, _ := ctrl["tab_id"].(float64)
					tabID := byte(tabIDFloat)

					tabsMu.Lock()
					tab, ok := tabs[tabID]
					if ok {
						tab.Close()
						delete(tabs, tabID)
					}
					tabsMu.Unlock()

					logEvent(conn, "DELETE_TAB", "system", fmt.Sprintf("Terminal Tab %d closed", tabID))

					// Broadcast tab_deleted to all viewers
					conn.WriteJSON(map[string]interface{}{
						"type":   "control",
						"action": "tab_deleted",
						"tab_id": tabID,
					})
				case "set_focus":
					viewerID, _ := ctrl["viewer_id"].(string)
					viewerName, _ := ctrl["viewer_name"].(string)
					tabIDFloat, _ := ctrl["tab_id"].(float64)
					tabID := byte(tabIDFloat)
					isBot, _ := ctrl["is_bot"].(bool)

					presenceMutex.Lock()
					tabsPresence, botViewers, eventName, eventUser, shouldLog := applyPresenceTransitionWithBot(presenceMap, "set_focus", viewerID, viewerName, tabID, isBot)
					presenceMutex.Unlock()

					if shouldLog {
						logEvent(conn, eventName, eventUser, fmt.Sprintf("User %s joined", eventUser))
					}

					conn.WriteJSON(map[string]interface{}{
						"type":        "control",
						"action":      "presence",
						"tabs":        tabsPresence,
						"bot_viewers": botViewers,
					})
				case "get_events":
					targetConn, _ := ctrl["target_conn"].(string)
					eventHistoryMu.RLock()
					evts := make([]map[string]interface{}, len(eventHistory))
					copy(evts, eventHistory)
					eventHistoryMu.RUnlock()
					conn.WriteJSON(map[string]interface{}{
						"type":        "control",
						"action":      "events_history",
						"target_conn": targetConn,
						"events":      evts,
					})
				case "get_chat_history":
					targetConn, _ := ctrl["target_conn"].(string)
					chats := getHostChatHistory()
					conn.WriteJSON(map[string]interface{}{
						"type":        "control",
						"action":      "chat_history",
						"target_conn": targetConn,
						"history":     chats,
					})
				case "unfocus":
					viewerID, _ := ctrl["viewer_id"].(string)
					viewerName, _ := ctrl["viewer_name"].(string)
					isBot, _ := ctrl["is_bot"].(bool)

					presenceMutex.Lock()
					tabsPresence, botViewers, eventName, eventUser, shouldLog := applyPresenceTransitionWithBot(presenceMap, "unfocus", viewerID, viewerName, lobbyTabID, isBot)
					presenceMutex.Unlock()

					if shouldLog {
						logEvent(conn, eventName, eventUser, fmt.Sprintf("User %s joined", eventUser))
					}

					conn.WriteJSON(map[string]interface{}{
						"type":        "control",
						"action":      "presence",
						"tabs":        tabsPresence,
						"bot_viewers": botViewers,
					})
				case "viewer_disconnected":
					viewerID, _ := ctrl["viewer_id"].(string)

					presenceMutex.Lock()
					tabsPresence, botViewers, eventName, eventUser, shouldLog := applyPresenceTransitionWithBot(presenceMap, "viewer_disconnected", viewerID, "", 0, false)
					presenceMutex.Unlock()

					if shouldLog {
						logEvent(conn, eventName, eventUser, fmt.Sprintf("User %s disconnected", eventUser))
					}

					conn.WriteJSON(map[string]interface{}{
						"type":        "control",
						"action":      "presence",
						"tabs":        tabsPresence,
						"bot_viewers": botViewers,
					})
				case "chat":
					appendHostChatHistory(ctrl)
					// The relay can no longer read chat, so the host fans it out.
					bcast := make(map[string]interface{}, len(ctrl))
					for k, v := range ctrl {
						if k != "target_conn" {
							bcast[k] = v
						}
					}
					conn.WriteJSON(bcast)

				// ===== FILE MANAGER ACTIONS =====
				case "req_dir":
					reqPath, _ := ctrl["path"].(string)
					targetConn, _ := ctrl["target_conn"].(string)
					handleReqDir(reqPath, targetConn, conn)

				case "req_read_file":
					reqPath, _ := ctrl["path"].(string)
					targetConn, _ := ctrl["target_conn"].(string)
					handleReqReadFile(reqPath, targetConn, ctrlTransferID(ctrl), conn)

				case "prepare_save":
					reqPath, _ := ctrl["path"].(string)
					targetConn, _ := ctrl["target_conn"].(string)
					handlePrepareSave(reqPath, targetConn, ctrlTransferID(ctrl), conn)

				case "prepare_upload":
					reqPath, _ := ctrl["path"].(string)
					targetConn, _ := ctrl["target_conn"].(string)
					handlePrepareSave(reqPath, targetConn, ctrlTransferID(ctrl), conn) // Same logic as save

				case "create_file":
					reqPath, _ := ctrl["path"].(string)
					targetConn, _ := ctrl["target_conn"].(string)
					handleCreateFile(reqPath, targetConn, conn)

				case "create_dir":
					reqPath, _ := ctrl["path"].(string)
					targetConn, _ := ctrl["target_conn"].(string)
					handleCreateDir(reqPath, targetConn, conn)

				case "rename_file":
					oldPath, _ := ctrl["old_path"].(string)
					newPath, _ := ctrl["new_path"].(string)
					targetConn, _ := ctrl["target_conn"].(string)
					handleRenameFile(oldPath, newPath, targetConn, conn)

				case "delete_file":
					reqPath, _ := ctrl["path"].(string)
					targetConn, _ := ctrl["target_conn"].(string)
					handleDeleteFile(reqPath, targetConn, conn)

				case "ai_exec":
					handleAIExec(ctrl, conn)

				case "ai_get_info":
					targetConn, _ := ctrl["target_conn"].(string)
					handleAIGetInfo(targetConn, conn)
				}
			}
		}
	}

	// Clean up active host connection, proxy secret, and tabs when host session terminates
	if c := getHostConn(); c != nil {
		_ = c.Close()
		setHostConn(nil)
	}
	resetHostProxySecret()

	tabsMu.Lock()
	for _, t := range tabs {
		t.Close()
	}
	tabsMu.Unlock()
}

func reconnectToRelay(opts HostOptions, sessionID string, timeout time.Duration) (*SafeConn, error) {
	fmt.Printf("\n⚠️  [Host] Connection to relay lost. Keeping terminal alive, reconnecting (timeout %v)...\n", timeout)
	log.Printf("[Host] Connection to relay lost. Keeping terminal alive, reconnecting (timeout %v)...", timeout)

	deadline := time.Now().Add(timeout)
	attempt := 0

	u, err := url.Parse(opts.DialURL)
	if err != nil {
		return nil, fmt.Errorf("invalid dial URL: %w", err)
	}

	for time.Now().Before(deadline) {
		attempt++
		if attempt > 1 {
			time.Sleep(2 * time.Second)
		}

		rawConn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
		if err != nil {
			continue
		}

		conn := &SafeConn{Conn: rawConn}

		auth := map[string]interface{}{
			"type":             "auth",
			"role":             "host",
			"auth_token":       generateAuthToken(opts.Pass),
			"protocol_version": protocolVersion,
			"preview":          opts.Preview,
			"session_id":       sessionID,
			"buffer":           opts.Buffer,
		}
		if opts.InternalToken != "" {
			auth["internal_token"] = opts.InternalToken
		}

		if err := conn.WriteJSON(auth); err != nil {
			_ = conn.Close()
			continue
		}

		var authResp struct {
			Type           string `json:"type"`
			SessionID      string `json:"session_id"`
			Message        string `json:"message"`
			PreviewEnabled bool   `json:"preview_enabled"`
			ProxySecret    string `json:"proxy_secret"`
			WSProxyPath    string `json:"ws_proxy_path"`
			ClampedBuffer  int    `json:"clamped_buffer"`
		}

		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		err = conn.ReadJSON(&authResp)
		_ = conn.SetReadDeadline(time.Time{})

		if err != nil || authResp.Type != "auth_success" {
			_ = conn.Close()
			continue
		}

		if authResp.ClampedBuffer > 0 && authResp.ClampedBuffer < opts.Buffer {
			opts.Buffer = authResp.ClampedBuffer
			if opts.Serve != nil {
				opts.Serve.Buffer = authResp.ClampedBuffer
			}
			setHostOptions(opts)
			setMaxBufferSize(opts.Buffer * 1024 * 1024)
		}

		// Update preview proxy secret if changed
		if opts.Preview && authResp.PreviewEnabled && authResp.ProxySecret != "" {
			setHostProxySecret(authResp.ProxySecret)
		}

		fmt.Printf("✅ [Host] Reconnected to relay successfully! Session %s resumed.\n\n", sessionID)
		log.Printf("[Host] Reconnected to relay successfully! Session %s resumed.", sessionID)
		conn.ctl = hostCtl
		announceHostEpoch(conn)
		return conn, nil
	}

	return nil, fmt.Errorf("could not reconnect within %v", timeout)
}

// ===== FILE MANAGER HANDLERS =====

// ctrlTransferID extracts the client-chosen transfer id from a control message.
func ctrlTransferID(ctrl map[string]interface{}) uint32 {
	if v, ok := ctrl["transfer_id"].(float64); ok {
		return uint32(v)
	}
	return 0
}

func isSubPath(base, target string) (bool, error) {
	normBase := filepath.Clean(base)
	normTarget := filepath.Clean(target)
	if runtime.GOOS == "windows" {
		normBase = strings.ToLower(normBase)
		normTarget = strings.ToLower(normTarget)
	}

	rel, err := filepath.Rel(normBase, normTarget)
	if err != nil {
		// FAIL-DENY: Different drive on Windows or path calculation error
		return false, err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false, nil
	}
	return true, nil
}

func evalSymlinksForTarget(path string) (string, error) {
	if realPath, err := filepath.EvalSymlinks(path); err == nil {
		return realPath, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	var trailing []string
	cur := path
	for {
		dir := filepath.Dir(cur)
		base := filepath.Base(cur)
		trailing = append([]string{base}, trailing...)
		if dir == cur {
			break
		}
		if realDir, err := filepath.EvalSymlinks(dir); err == nil {
			resolved := realDir
			for _, comp := range trailing {
				resolved = filepath.Join(resolved, comp)
			}
			return resolved, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		cur = dir
	}
	return filepath.Clean(path), nil
}

// sanitizePath resolves the requested path relative to the host working directory.
func sanitizePath(reqPath string) (string, error) {
	cleaned := filepath.Clean(reqPath)

	var absPath string
	if filepath.IsAbs(cleaned) {
		absPath = cleaned
	} else {
		absPath = filepath.Join(hostWorkDir, cleaned)
	}

	absPath, err := filepath.Abs(absPath)
	if err != nil {
		return "", fmt.Errorf("invalid path")
	}

	if hostSandbox {
		resolvedTarget, err := evalSymlinksForTarget(absPath)
		if err != nil {
			return "", fmt.Errorf("invalid path: %w", err)
		}
		allowed, err := isSubPath(hostWorkDir, resolvedTarget)
		if err != nil || !allowed {
			return "", fmt.Errorf("access denied: path escapes sandbox root (%s)", hostWorkDir)
		}
		return resolvedTarget, nil
	}

	return absPath, nil
}

// isInternalRmteFile returns true for internal runtime artifacts (rmte-*.meta, rmte-*.pid, rmte-*.log, rmte-*.json, rmte-*.tmp, rmte-*.bak)
// It performs case-insensitive matching to protect against case-folding filesystem attacks (e.g. Windows).
func isInternalRmteFile(pathOrName string) bool {
	base := strings.ToLower(filepath.Base(pathOrName))
	if strings.HasPrefix(base, "rmte-") {
		if strings.HasSuffix(base, ".meta") || strings.HasSuffix(base, ".pid") || strings.HasSuffix(base, ".log") ||
			strings.HasSuffix(base, ".json") || strings.HasSuffix(base, ".tmp") || strings.HasSuffix(base, ".bak") {
			return true
		}
	}
	return false
}

func handleReqDir(reqPath, targetConn string, conn *SafeConn) {
	absPath, err := sanitizePath(reqPath)
	if err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "fm_error",
			"target_conn": targetConn,
			"message":     err.Error(),
		})
		return
	}

	entries, err := os.ReadDir(absPath)
	if err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "fm_error",
			"target_conn": targetConn,
			"message":     fmt.Sprintf("cannot read directory: %v", err),
		})
		return
	}

	type FileEntry struct {
		Name  string `json:"name"`
		IsDir bool   `json:"is_dir"`
		Size  int64  `json:"size"`
	}

	var files []FileEntry
	for _, e := range entries {
		if isInternalRmteFile(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, FileEntry{
			Name:  e.Name(),
			IsDir: e.IsDir(),
			Size:  info.Size(),
		})
	}

	// Sort: directories first, then files alphabetically
	sort.Slice(files, func(i, j int) bool {
		if files[i].IsDir != files[j].IsDir {
			return files[i].IsDir
		}
		return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
	})

	conn.WriteJSON(map[string]interface{}{
		"type":        "control",
		"action":      "dir_data",
		"target_conn": targetConn,
		"path":        filepath.ToSlash(absPath),
		"files":       files,
	})
}

func handleReqReadFile(reqPath, targetConn string, transferID uint32, conn *SafeConn) {
	absPath, err := sanitizePath(reqPath)
	if err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "fm_error",
			"target_conn": targetConn,
			"message":     err.Error(),
		})
		return
	}

	if isInternalRmteFile(absPath) {
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "fm_error",
			"target_conn": targetConn,
			"message":     "access to internal RMTE runtime files is restricted",
		})
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "fm_error",
			"target_conn": targetConn,
			"message":     fmt.Sprintf("file not found: %v", err),
		})
		return
	}

	if info.IsDir() {
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "fm_error",
			"target_conn": targetConn,
			"message":     "cannot read a directory as file",
		})
		return
	}

	bufLimit := getMaxBufferSize()
	if info.Size() > int64(bufLimit) {
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "fm_error",
			"target_conn": targetConn,
			"message":     fmt.Sprintf("file too large: %d bytes (limit: %d bytes)", info.Size(), bufLimit),
		})
		return
	}

	fileData, err := os.ReadFile(absPath)
	if err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "fm_error",
			"target_conn": targetConn,
			"message":     fmt.Sprintf("read error: %v", err),
		})
		return
	}

	// Signal: file read starting
	conn.WriteJSON(map[string]interface{}{
		"type":        "control",
		"action":      "read_file_start",
		"target_conn": targetConn,
		"transfer_id": transferID,
		"path":        filepath.ToSlash(absPath),
	})

	logEvent(conn, "OPEN_FILE", targetConn, fmt.Sprintf("Opened %s (%d bytes)", filepath.ToSlash(absPath), len(fileData)))

	// Send file content as encrypted binary on Tab 255
	payload, err := encryptDataChannel(dirToViewer, transferID, fileData)
	if err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "fm_error",
			"target_conn": targetConn,
			"message":     "encryption error",
		})
		return
	}

	conn.WriteMessage(websocket.BinaryMessage, payload)
}

func handlePrepareSave(reqPath, targetConn string, transferID uint32, conn *SafeConn) {
	absPath, err := sanitizePath(reqPath)
	if err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "fm_error",
			"target_conn": targetConn,
			"transfer_id": transferID,
			"message":     err.Error(),
		})
		return
	}

	if isInternalRmteFile(absPath) {
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "fm_error",
			"target_conn": targetConn,
			"transfer_id": transferID,
			"message":     "modifying internal RMTE runtime files is restricted",
		})
		return
	}

	pendingSaveMu.Lock()
	pendingSaves[transferID] = &PendingSave{
		Path:       absPath,
		TargetConn: targetConn,
		TransferID: transferID,
	}
	pendingSaveMu.Unlock()

	conn.WriteJSON(map[string]interface{}{
		"type":        "control",
		"action":      "ready_for_data",
		"target_conn": targetConn,
		"transfer_id": transferID,
		"path":        filepath.ToSlash(absPath),
	})
}

func handleDataChannelWrite(transferID uint32, plaintext []byte, conn *SafeConn) {
	pendingSaveMu.Lock()
	ps := pendingSaves[transferID]
	delete(pendingSaves, transferID)
	pendingSaveMu.Unlock()

	if ps == nil {
		log.Printf("[FileManager] Received Tab 255 data for unknown transfer %d", transferID)
		return
	}

	bufLimit := getMaxBufferSize()
	if len(plaintext) > bufLimit {
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "fm_error",
			"target_conn": ps.TargetConn,
			"transfer_id": ps.TransferID,
			"message":     fmt.Sprintf("file too large: %d bytes (limit: %d bytes)", len(plaintext), bufLimit),
		})
		return
	}

	absPath, err := sanitizePath(ps.Path)
	if err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "fm_error",
			"target_conn": ps.TargetConn,
			"transfer_id": ps.TransferID,
			"message":     err.Error(),
		})
		return
	}

	// Ensure parent directory exists
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "fm_error",
			"target_conn": ps.TargetConn,
			"transfer_id": ps.TransferID,
			"message":     fmt.Sprintf("cannot create directory: %v", err),
		})
		return
	}

	if err := os.WriteFile(absPath, plaintext, 0644); err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "fm_error",
			"target_conn": ps.TargetConn,
			"transfer_id": ps.TransferID,
			"message":     fmt.Sprintf("write error: %v", err),
		})
		return
	}

	conn.WriteJSON(map[string]interface{}{
		"type":        "control",
		"action":      "file_saved",
		"target_conn": ps.TargetConn,
		"transfer_id": ps.TransferID,
		"path":        ps.Path,
		"status":      "success",
		"size":        len(plaintext),
	})
	log.Printf("[FileManager] Saved %s (%d bytes)", ps.Path, len(plaintext))
	logEvent(conn, "SAVE_FILE", ps.TargetConn, fmt.Sprintf("Saved %s (%d bytes)", filepath.ToSlash(ps.Path), len(plaintext)))
}

// ===== TERMINAL TAB MANAGEMENT =====

func createTab(id byte, ws *SafeConn) {
	createTabWithTitle(id, ws, fmt.Sprintf("Terminal %d", id), false)
}

func createTabWithTitle(id byte, ws *SafeConn, title string, isAI bool) {
	tab, err := startPlatformTabWithTitle(id, ws, title, isAI)
	if err != nil || tab == nil {
		return
	}

	tab.Title = title
	tab.IsAI = isAI
	if isAI {
		tab.AIProcessDone = make(chan struct{})
	}

	tabsMu.Lock()
	tabs[id] = tab
	tabsMu.Unlock()

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := tab.ReadCloser.Read(buf)
			if err != nil {
				break
			}
			data := buf[:n]

			tab.Mutex.Lock()
			tab.Buffer = append(tab.Buffer, data...)
			if bufLimit := getMaxBufferSize(); len(tab.Buffer) > bufLimit {
				tab.Buffer = tab.Buffer[len(tab.Buffer)-bufLimit:]
			}
			tab.Mutex.Unlock()

			if tab.IsAI {
				tab.AIListenerMu.Lock()
				listener := tab.AIListener
				doneChan := tab.AIDoneChan
				tab.AIListenerMu.Unlock()

				if listener != nil {
					select {
					case listener <- append([]byte(nil), data...):
					case <-doneChan:
					case <-tab.AIProcessDone:
					}
				}
			}

			payload, err := encryptBinary(dirToViewer, id, data)
			if err == nil {
				c := getHostConn()
				if c != nil {
					_ = c.WriteMessage(websocket.BinaryMessage, payload)
				}
			}
		}
		tab.waitCmd()
		if tab.IsAI {
			tab.notifyAIDone()
		}
		tab.Close()
	}()
}

func runWithPipes(id byte, c *exec.Cmd, ws *SafeConn) {
	runWithPipesWithTitle(id, c, ws, fmt.Sprintf("Terminal %d", id), false)
}

func runWithPipesWithTitle(id byte, c *exec.Cmd, ws *SafeConn, title string, isAI bool) {
	stdin, _ := c.StdinPipe()

	// Create a pipe to merge stdout and stderr
	pr, pw := io.Pipe()
	c.Stdout = pw
	c.Stderr = pw

	if err := c.Start(); err != nil {
		log.Printf("Failed to start process with pipes: %v", err)
		return
	}

	tab := &TabSession{
		Title:       title,
		IsAI:        isAI,
		Cmd:         c,
		ReadCloser:  pr,
		WriteCloser: stdin,
		IsPipe:      true,
		CloseFn: func() error {
			pr.Close()
			pw.Close()
			stdin.Close()
			if c.Process != nil {
				c.Process.Kill()
			}
			return nil
		},
	}
	if isAI {
		tab.AIProcessDone = make(chan struct{})
	}

	tabsMu.Lock()
	tabs[id] = tab
	tabsMu.Unlock()

	// Goroutine to read from merged output and stream to WS
	go func() {
		defer pw.Close()
		buf := make([]byte, 4096)
		for {
			n, err := tab.ReadCloser.Read(buf)
			if err != nil {
				break
			}
			data := buf[:n]

			tab.Mutex.Lock()
			tab.Buffer = append(tab.Buffer, data...)
			if bufLimit := getMaxBufferSize(); len(tab.Buffer) > bufLimit {
				tab.Buffer = tab.Buffer[len(tab.Buffer)-bufLimit:]
			}
			tab.Mutex.Unlock()

			if tab.IsAI {
				tab.AIListenerMu.Lock()
				listener := tab.AIListener
				doneChan := tab.AIDoneChan
				tab.AIListenerMu.Unlock()

				if listener != nil {
					select {
					case listener <- append([]byte(nil), data...):
					case <-doneChan:
					case <-tab.AIProcessDone:
					}
				}
			}

			payload, err := encryptBinary(dirToViewer, id, data)
			if err == nil {
				c := getHostConn()
				if c != nil {
					_ = c.WriteMessage(websocket.BinaryMessage, payload)
				}
			}
		}
		tab.waitCmd()
		if tab.IsAI {
			tab.notifyAIDone()
		}
		tab.Close()
	}()
}

// ===== EXTENDED FILE MANAGER HANDLERS =====

func handleCreateFile(reqPath, targetConn string, conn *SafeConn) {
	absPath, err := sanitizePath(reqPath)
	if err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type": "control", "action": "fm_error",
			"target_conn": targetConn, "message": err.Error(),
		})
		return
	}

	if isInternalRmteFile(absPath) {
		conn.WriteJSON(map[string]interface{}{
			"type": "control", "action": "fm_error",
			"target_conn": targetConn, "message": "creating internal RMTE runtime files is restricted",
		})
		return
	}

	// Ensure parent directory exists
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type": "control", "action": "fm_error",
			"target_conn": targetConn,
			"message":     fmt.Sprintf("cannot create parent dir: %v", err),
		})
		return
	}

	// Create empty file (fail if exists)
	if _, err := os.Stat(absPath); err == nil {
		conn.WriteJSON(map[string]interface{}{
			"type": "control", "action": "fm_error",
			"target_conn": targetConn, "message": "file already exists",
		})
		return
	}

	if err := os.WriteFile(absPath, []byte{}, 0644); err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type": "control", "action": "fm_error",
			"target_conn": targetConn,
			"message":     fmt.Sprintf("create error: %v", err),
		})
		return
	}

	log.Printf("[FileManager] Created file %s", absPath)
	logEvent(conn, "NEW_FILE", targetConn, fmt.Sprintf("Created file %s", filepath.ToSlash(absPath)))
	conn.WriteJSON(map[string]interface{}{
		"type": "control", "action": "file_created",
		"target_conn": targetConn, "path": filepath.ToSlash(absPath),
	})
}

func handleCreateDir(reqPath, targetConn string, conn *SafeConn) {
	absPath, err := sanitizePath(reqPath)
	if err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type": "control", "action": "fm_error",
			"target_conn": targetConn, "message": err.Error(),
		})
		return
	}

	if isInternalRmteFile(absPath) {
		conn.WriteJSON(map[string]interface{}{
			"type": "control", "action": "fm_error",
			"target_conn": targetConn, "message": "creating internal RMTE runtime files is restricted",
		})
		return
	}

	if err := os.MkdirAll(absPath, 0755); err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type": "control", "action": "fm_error",
			"target_conn": targetConn,
			"message":     fmt.Sprintf("create dir error: %v", err),
		})
		return
	}

	log.Printf("[FileManager] Created directory %s", absPath)
	logEvent(conn, "NEW_DIR", targetConn, fmt.Sprintf("Created directory %s", filepath.ToSlash(absPath)))
	conn.WriteJSON(map[string]interface{}{
		"type": "control", "action": "dir_created",
		"target_conn": targetConn, "path": filepath.ToSlash(absPath),
	})
}

func handleRenameFile(oldPath, newPath, targetConn string, conn *SafeConn) {
	absOld, err := sanitizePath(oldPath)
	if err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type": "control", "action": "fm_error",
			"target_conn": targetConn, "message": "source: " + err.Error(),
		})
		return
	}

	absNew, err := sanitizePath(newPath)
	if err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type": "control", "action": "fm_error",
			"target_conn": targetConn, "message": "destination: " + err.Error(),
		})
		return
	}

	if isInternalRmteFile(absOld) || isInternalRmteFile(absNew) {
		conn.WriteJSON(map[string]interface{}{
			"type": "control", "action": "fm_error",
			"target_conn": targetConn, "message": "modifying internal RMTE runtime files is restricted",
		})
		return
	}

	// Ensure target parent exists
	if err := os.MkdirAll(filepath.Dir(absNew), 0755); err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type": "control", "action": "fm_error",
			"target_conn": targetConn,
			"message":     fmt.Sprintf("cannot create target dir: %v", err),
		})
		return
	}

	if err := os.Rename(absOld, absNew); err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type": "control", "action": "fm_error",
			"target_conn": targetConn,
			"message":     fmt.Sprintf("rename error: %v", err),
		})
		return
	}

	log.Printf("[FileManager] Renamed %s → %s", absOld, absNew)
	logEvent(conn, "RENAME_FILE", targetConn, fmt.Sprintf("Renamed %s → %s", filepath.ToSlash(absOld), filepath.ToSlash(absNew)))
	conn.WriteJSON(map[string]interface{}{
		"type": "control", "action": "file_renamed",
		"target_conn": targetConn,
		"old_path":    filepath.ToSlash(absOld), "new_path": filepath.ToSlash(absNew),
	})
}

func handleDeleteFile(reqPath, targetConn string, conn *SafeConn) {
	absPath, err := sanitizePath(reqPath)
	if err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type": "control", "action": "fm_error",
			"target_conn": targetConn, "message": err.Error(),
		})
		return
	}

	if isInternalRmteFile(absPath) {
		conn.WriteJSON(map[string]interface{}{
			"type": "control", "action": "fm_error",
			"target_conn": targetConn, "message": "deleting internal RMTE runtime files is restricted",
		})
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type": "control", "action": "fm_error",
			"target_conn": targetConn, "message": "not found",
		})
		return
	}

	if info.IsDir() {
		err = os.RemoveAll(absPath)
	} else {
		err = os.Remove(absPath)
	}

	if err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type": "control", "action": "fm_error",
			"target_conn": targetConn,
			"message":     fmt.Sprintf("delete error: %v", err),
		})
		return
	}

	log.Printf("[FileManager] Deleted %s", absPath)
	logEvent(conn, "DELETE_FILE", targetConn, fmt.Sprintf("Deleted %s", filepath.ToSlash(absPath)))
	conn.WriteJSON(map[string]interface{}{
		"type": "control", "action": "file_deleted",
		"target_conn": targetConn, "path": filepath.ToSlash(absPath),
	})
}

// buildShareLink converts a WebSocket relay URL to a browser-accessible sharable link.
// e.g. ws://host:8048/ws-rmte + "/web/" → http://host:8048/web/?server=ws://host:8048/ws-rmte&session=abc123#pass=secret
// The password is placed in the URL fragment (#pass=...), which browsers never send
// to the server, so it never shows up in relay/proxy access logs. Empty pass omits it.
func buildShareLink(serverURL, webPath, sessionID, pass string) string {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return fmt.Sprintf("(could not generate link: %v)", err)
	}

	// Determine HTTP scheme from WS scheme
	var scheme string
	switch parsed.Scheme {
	case "wss":
		scheme = "https"
	default:
		scheme = "http"
	}

	if webPath == "" {
		webPath = "/"
	}
	baseURL := fmt.Sprintf("%s://%s%s", scheme, parsed.Host, webPath)

	// Encode query params
	params := url.Values{}
	params.Set("server", serverURL)
	params.Set("session", sessionID)

	link := baseURL + "?" + params.Encode()
	if pass != "" {
		link += "#" + url.Values{"pass": {pass}}.Encode()
	}
	return link
}

func buildTabsPresence(pMap map[string]ViewersPresence) map[string][]string {
	tabsPresence := make(map[string][]string)
	for _, p := range pMap {
		tabKey := fmt.Sprintf("%d", p.TabID)
		tabsPresence[tabKey] = append(tabsPresence[tabKey], p.ViewerName)
	}
	return tabsPresence
}

func buildBotViewers(pMap map[string]ViewersPresence) []string {
	seen := make(map[string]bool)
	bots := make([]string, 0)
	for _, p := range pMap {
		if p.IsBot && !seen[p.ViewerName] {
			seen[p.ViewerName] = true
			bots = append(bots, p.ViewerName)
		}
	}
	sort.Strings(bots)
	return bots
}

func applyPresenceTransitionWithBot(
	pMap map[string]ViewersPresence,
	action, viewerID, viewerName string,
	tabID byte,
	isBot bool,
) (tabsPresence map[string][]string, botViewers []string, eventName string, eventUser string, shouldLog bool) {
	if viewerID == "" {
		return buildTabsPresence(pMap), buildBotViewers(pMap), "", "", false
	}
	if viewerName == "" {
		viewerName = viewerID
	}

	switch action {
	case "set_focus":
		prev, existed := pMap[viewerID]
		botFlag := isBot
		if existed && !isBot {
			botFlag = prev.IsBot
		}
		pMap[viewerID] = ViewersPresence{
			ViewerName: viewerName,
			TabID:      tabID,
			IsBot:      botFlag,
		}
		if !existed && viewerID != "" {
			eventName = "CONNECT"
			eventUser = viewerName
			shouldLog = true
		}
	case "unfocus":
		if p, ok := pMap[viewerID]; ok {
			p.TabID = lobbyTabID
			if isBot {
				p.IsBot = true
			}
			pMap[viewerID] = p
		} else if viewerID != "" {
			pMap[viewerID] = ViewersPresence{
				ViewerName: viewerName,
				TabID:      lobbyTabID,
				IsBot:      isBot,
			}
			eventName = "CONNECT"
			eventUser = viewerName
			shouldLog = true
		}
	case "viewer_disconnected":
		prev, existed := pMap[viewerID]
		delete(pMap, viewerID)
		if existed {
			eventName = "DISCONNECT"
			eventUser = prev.ViewerName
			shouldLog = true
		}
	}

	return buildTabsPresence(pMap), buildBotViewers(pMap), eventName, eventUser, shouldLog
}

func applyPresenceTransition(
	pMap map[string]ViewersPresence,
	action, viewerID, viewerName string,
	tabID byte,
) (tabsPresence map[string][]string, eventName string, eventUser string, shouldLog bool) {
	tabs, _, eventName, eventUser, shouldLog := applyPresenceTransitionWithBot(pMap, action, viewerID, viewerName, tabID, false)
	return tabs, eventName, eventUser, shouldLog
}
