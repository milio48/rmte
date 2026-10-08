package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type SafeConn struct {
	*websocket.Conn
	mu sync.Mutex
}

func (c *SafeConn) WriteMessage(messageType int, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn.WriteMessage(messageType, data)
}

func (c *SafeConn) WriteJSON(v interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn.WriteJSON(v)
}

func (c *SafeConn) WriteControl(messageType int, data []byte, deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn.WriteControl(messageType, data, deadline)
}

type TabSession struct {
	Cmd         *exec.Cmd
	ReadCloser  io.ReadCloser
	WriteCloser io.WriteCloser
	IsPipe      bool
	Buffer      []byte
	Mutex       sync.Mutex

	// LineBuffer for Windows Pipe mode to emulate PTY backspace
	LineBuffer []byte
	Resizer    func(cols, rows int) error
	CloseFn    func() error
	closeOnce  sync.Once
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
	})
}

type ViewersPresence struct {
	ViewerName string
	TabID      byte
}

// File manager state for Tab 255 data channel
type PendingSave struct {
	Path       string
	TargetConn string
	TransferID uint32
}

const dataChannelTabID byte = 255

var (
	tabs   = make(map[byte]*TabSession)
	tabsMu sync.RWMutex

	nextTabID byte = 1 // S3: incrementing counter (0 is initial tab)

	presenceMap   = make(map[string]ViewersPresence)
	presenceMutex sync.Mutex

	// Dynamic buffer limit (set by --buffer flag)
	maxBufferSize int

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
)

// HostOptions configures a host session (used by `serve` and `share`).
type HostOptions struct {
	DialURL       string // URL actually dialed (may be loopback)
	PublicURL     string // URL shown in printed links
	Pass          string
	PassGenerated bool
	Buffer        int
	InternalToken string
	Mode          string       // standalone | hybrid | share
	Dir           string       // initial working directory
	ID            string       // custom session ID (optional)
	Preview       bool         // enable Embedded Web Browser Preview reverse proxy
	Serve         *ServeConfig // non-nil when embedded in `serve`
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

	if conn != nil {
		conn.WriteJSON(map[string]interface{}{
			"type":   "control",
			"action": "event_log",
			"event":  evt,
		})
	}
}

func runHost(opts HostOptions) {
	maxBufferSize = opts.Buffer * 1024 * 1024

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
			SessionID:     authResp.SessionID,
			Buffer:        opts.Buffer,
			NoWeb:         authResp.NoWeb,
			NoCLI:         authResp.NoCLI,
			Preview:       opts.Preview,
			Dir:           opts.Dir,
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

	// Initialize Event Log file on Host
	initEventLog(authResp.SessionID)
	logEvent(conn, "HOST_START", "host", fmt.Sprintf("Host session %s active in %s", authResp.SessionID, hostWorkDir))

	// Signal parent if running in background daemon mode
	markDaemonReady(authResp.SessionID)
	if isDaemonChild() {
		meta := SessionMeta{
			SessionID:   authResp.SessionID,
			Password:    opts.Pass,
			ServerRelay: opts.DialURL,
			Dir:         hostWorkDir,
			Buffer:      opts.Buffer,
			Preview:     opts.Preview,
			PID:         os.Getpid(),
			Mode:        opts.Mode,
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
		defer removeSessionMeta(authResp.SessionID)
	}

	// Create initial tab (ID 0)
	createTab(0, conn)

	// Message loop
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[Host] conn.ReadMessage error: %v", err)
			break
		}

		if mt == websocket.BinaryMessage {
			// Tab 255 = Data Channel for file uploads/saves (framed with a transfer id)
			if len(data) > 0 && data[0] == dataChannelTabID {
				transferID, plaintext, err := decryptDataChannel(data)
				if err != nil {
					// Legacy frame without transfer id: [255][nonce][ciphertext]
					if _, legacy, lerr := decryptBinary(data); lerr == nil {
						handleDataChannelWrite(0, legacy, conn)
					}
					continue
				}
				handleDataChannelWrite(transferID, plaintext, conn)
				continue
			}

			tabID, plaintext, err := decryptBinary(data)
			if err != nil {
				continue
			}

			tabsMu.RLock()
			tab, ok := tabs[tabID]
			tabsMu.RUnlock()

			if ok {
				if tab.IsPipe && runtime.GOOS == "windows" {
					// Emulate PTY line buffering for Windows pipe fallback
					for _, b := range plaintext {
						if b == '\r' || b == '\n' {
							// Echo newline
							payload, _ := encryptBinary(tabID, []byte("\r\n"))
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

							payload, _ := encryptBinary(tabID, []byte("^C\r\n"))
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
								payload, _ := encryptBinary(tabID, []byte("\b \b"))
								conn.WriteMessage(websocket.BinaryMessage, payload)
							}
						} else {
							// Normal character
							tab.Mutex.Lock()
							tab.LineBuffer = append(tab.LineBuffer, b)
							tab.Mutex.Unlock()

							payload, _ := encryptBinary(tabID, []byte{b})
							conn.WriteMessage(websocket.BinaryMessage, payload)
						}
					}
				} else {
					tab.WriteCloser.Write(plaintext)
				}
			}
		} else if mt == websocket.TextMessage {
			var ctrl map[string]interface{}
			if err := json.Unmarshal(data, &ctrl); err == nil {
				action, _ := ctrl["action"].(string)
				switch action {
				case "get_tabs":
					tabsMu.RLock()
					activeTabs := make([]int, 0)
					for id := range tabs {
						activeTabs = append(activeTabs, int(id))
					}
					tabsMu.RUnlock()

					sort.Ints(activeTabs)

					conn.WriteJSON(map[string]interface{}{
						"type":   "control",
						"action": "tabs_list",
						"tabs":   activeTabs,
					})
				case "request_new_tab":
					newID := nextTabID
					nextTabID++
					createTab(newID, conn)
					logEvent(conn, "NEW_TAB", "system", fmt.Sprintf("Terminal Tab %d created", newID))
					conn.WriteJSON(map[string]interface{}{
						"type":   "control",
						"action": "tab_created",
						"tab_id": newID,
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

						payload, _ := encryptBinary(tabID, history)
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
					if viewerName == "" {
						viewerName = viewerID
					}
					tabIDFloat, _ := ctrl["tab_id"].(float64)
					tabID := byte(tabIDFloat)

					presenceMutex.Lock()
					_, existed := presenceMap[viewerID]
					presenceMap[viewerID] = ViewersPresence{
						ViewerName: viewerName,
						TabID:      tabID,
					}

					// Build tab -> users map
					tabsPresence := make(map[string][]string)
					for _, p := range presenceMap {
						tabKey := fmt.Sprintf("%d", p.TabID)
						tabsPresence[tabKey] = append(tabsPresence[tabKey], p.ViewerName)
					}
					presenceMutex.Unlock()

					if !existed {
						logEvent(conn, "CONNECT", viewerName, fmt.Sprintf("User %s joined", viewerName))
					}

					conn.WriteJSON(map[string]interface{}{
						"type":   "control",
						"action": "presence",
						"tabs":   tabsPresence,
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
				case "viewer_disconnected":
					viewerID, _ := ctrl["viewer_id"].(string)
					presenceMutex.Lock()
					prev, existed := presenceMap[viewerID]
					delete(presenceMap, viewerID)

					tabsPresence := make(map[string][]string)
					for _, p := range presenceMap {
						tabKey := fmt.Sprintf("%d", p.TabID)
						tabsPresence[tabKey] = append(tabsPresence[tabKey], p.ViewerName)
					}
					presenceMutex.Unlock()

					if existed {
						logEvent(conn, "DISCONNECT", prev.ViewerName, fmt.Sprintf("User %s disconnected", prev.ViewerName))
					}

					conn.WriteJSON(map[string]interface{}{
						"type":   "control",
						"action": "presence",
						"tabs":   tabsPresence,
					})
				case "chat":
					// Broadcast to all viewers
					conn.WriteMessage(websocket.TextMessage, data)

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
				}
			}
		}
	}
}

// ===== FILE MANAGER HANDLERS =====

// ctrlTransferID extracts the client-chosen transfer id from a control message.
func ctrlTransferID(ctrl map[string]interface{}) uint32 {
	if v, ok := ctrl["transfer_id"].(float64); ok {
		return uint32(v)
	}
	return 0
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

	return absPath, nil
}

// isInternalRmteFile returns true for internal runtime artifacts (rmte-*.meta, rmte-*.pid, rmte-*.log)
// It performs case-insensitive matching to protect against case-folding filesystem attacks (e.g. Windows).
func isInternalRmteFile(pathOrName string) bool {
	base := strings.ToLower(filepath.Base(pathOrName))
	if strings.HasPrefix(base, "rmte-") {
		if strings.HasSuffix(base, ".meta") || strings.HasSuffix(base, ".pid") || strings.HasSuffix(base, ".log") {
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

	if info.Size() > int64(maxBufferSize) {
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "fm_error",
			"target_conn": targetConn,
			"message":     fmt.Sprintf("file too large: %d bytes (limit: %d bytes)", info.Size(), maxBufferSize),
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
	payload, err := encryptDataChannel(transferID, fileData)
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

	if len(plaintext) > maxBufferSize {
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "fm_error",
			"target_conn": ps.TargetConn,
			"transfer_id": ps.TransferID,
			"message":     fmt.Sprintf("file too large: %d bytes (limit: %d bytes)", len(plaintext), maxBufferSize),
		})
		return
	}

	absPath, err := sanitizePath(ps.Path)
	if err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "fm_error",
			"target_conn": ps.TargetConn,
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
	tab, err := startPlatformTab(id, ws)
	if err != nil || tab == nil {
		return
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
			if len(tab.Buffer) > maxBufferSize {
				tab.Buffer = tab.Buffer[len(tab.Buffer)-maxBufferSize:]
			}
			tab.Mutex.Unlock()

			payload, err := encryptBinary(id, data)
			if err == nil {
				ws.WriteMessage(websocket.BinaryMessage, payload)
			}
		}
		tab.Close()
	}()
}

func runWithPipes(id byte, c *exec.Cmd, ws *SafeConn) {
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
			if len(tab.Buffer) > maxBufferSize {
				tab.Buffer = tab.Buffer[len(tab.Buffer)-maxBufferSize:]
			}
			tab.Mutex.Unlock()

			payload, err := encryptBinary(id, data)
			if err == nil {
				ws.WriteMessage(websocket.BinaryMessage, payload)
			}
		}
		c.Wait()
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
