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

	"github.com/creack/pty"
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
}

type ViewersPresence struct {
	ViewerName string
	TabID      byte
}

// File manager state for Tab 255 data channel
type PendingSave struct {
	Path       string
	TargetConn string
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

	// Pending file save state: when a viewer sends prepare_save,
	// we register what path the next Tab 255 binary frame should write to.
	pendingSave   *PendingSave
	pendingSaveMu sync.Mutex

	// Host working directory (sandbox root for file operations)
	hostWorkDir string
)

func runHost(serverURL, password string, bufferMB int) {
	maxBufferSize = bufferMB * 1024 * 1024

	// Set working directory as sandbox root
	wd, err := os.Getwd()
	if err != nil {
		log.Fatal("Cannot get working directory:", err)
	}
	hostWorkDir = wd

	u, err := url.Parse(serverURL)
	if err != nil {
		log.Fatal(err)
	}

	rawConn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Fatal("Dial error:", err)
	}
	conn := &SafeConn{Conn: rawConn}
	defer conn.Close()

	if err := setupCrypto(password); err != nil {
		log.Fatal(err)
	}

	// Auth as host
	auth := map[string]string{
		"type":             "auth",
		"role":             "host",
		"auth_token":       generateAuthToken(password),
		"protocol_version": protocolVersion,
	}
	conn.WriteJSON(auth)

	// Wait for session ID
	var authResp struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
	}
	if err := conn.ReadJSON(&authResp); err != nil || authResp.Type != "auth_success" {
		log.Fatal("Auth failed:", err)
	}

	fmt.Printf("Session ID: %s\n", authResp.SessionID)
	fmt.Printf("Share this ID with viewers to join.\n")
	fmt.Printf("Buffer limit: %d MB\n", bufferMB)

	// Create initial tab (ID 0)
	createTab(0, conn)

	// Message loop
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			break
		}

		if mt == websocket.BinaryMessage {
			tabID, plaintext, err := decryptBinary(data)
			if err != nil {
				continue
			}

			// Tab 255 = Data Channel for file uploads/saves
			if tabID == dataChannelTabID {
				handleDataChannelWrite(plaintext, conn)
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
					if ok {
						if f, isFile := tab.ReadCloser.(*os.File); isFile {
							pty.Setsize(f, &pty.Winsize{Cols: uint16(colsFloat), Rows: uint16(rowsFloat)})
						}
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
						if tab.Cmd != nil && tab.Cmd.Process != nil {
							tab.Cmd.Process.Kill()
						}
						if tab.ReadCloser != nil {
							tab.ReadCloser.Close()
						}
						if tab.WriteCloser != nil {
							tab.WriteCloser.Close()
						}
						delete(tabs, tabID)
					}
					tabsMu.Unlock()

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

					conn.WriteJSON(map[string]interface{}{
						"type":   "control",
						"action": "presence",
						"tabs":   tabsPresence,
					})
				case "viewer_disconnected":
					viewerID, _ := ctrl["viewer_id"].(string)
					presenceMutex.Lock()
					delete(presenceMap, viewerID)
					
					tabsPresence := make(map[string][]string)
					for _, p := range presenceMap {
						tabKey := fmt.Sprintf("%d", p.TabID)
						tabsPresence[tabKey] = append(tabsPresence[tabKey], p.ViewerName)
					}
					presenceMutex.Unlock()

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
					handleReqReadFile(reqPath, targetConn, conn)

				case "prepare_save":
					reqPath, _ := ctrl["path"].(string)
					targetConn, _ := ctrl["target_conn"].(string)
					handlePrepareSave(reqPath, targetConn, conn)

				case "prepare_upload":
					reqPath, _ := ctrl["path"].(string)
					targetConn, _ := ctrl["target_conn"].(string)
					handlePrepareSave(reqPath, targetConn, conn) // Same logic as save

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
		"path":        reqPath,
		"files":       files,
	})
}

func handleReqReadFile(reqPath, targetConn string, conn *SafeConn) {
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
		"path":        reqPath,
	})

	// Send file content as encrypted binary on Tab 255
	payload, err := encryptBinary(dataChannelTabID, fileData)
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

func handlePrepareSave(reqPath, targetConn string, conn *SafeConn) {
	_, err := sanitizePath(reqPath)
	if err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "fm_error",
			"target_conn": targetConn,
			"message":     err.Error(),
		})
		return
	}

	pendingSaveMu.Lock()
	pendingSave = &PendingSave{
		Path:       reqPath,
		TargetConn: targetConn,
	}
	pendingSaveMu.Unlock()

	conn.WriteJSON(map[string]interface{}{
		"type":        "control",
		"action":      "ready_for_data",
		"target_conn": targetConn,
		"path":        reqPath,
	})
}

func handleDataChannelWrite(plaintext []byte, conn *SafeConn) {
	pendingSaveMu.Lock()
	ps := pendingSave
	pendingSave = nil
	pendingSaveMu.Unlock()

	if ps == nil {
		log.Println("[FileManager] Received Tab 255 data but no pending save registered")
		return
	}

	if len(plaintext) > maxBufferSize {
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "fm_error",
			"target_conn": ps.TargetConn,
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
			"message":     fmt.Sprintf("cannot create directory: %v", err),
		})
		return
	}

	if err := os.WriteFile(absPath, plaintext, 0644); err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "fm_error",
			"target_conn": ps.TargetConn,
			"message":     fmt.Sprintf("write error: %v", err),
		})
		return
	}

	conn.WriteJSON(map[string]interface{}{
		"type":        "control",
		"action":      "file_saved",
		"target_conn": ps.TargetConn,
		"path":        ps.Path,
		"status":      "success",
		"size":        len(plaintext),
	})
	log.Printf("[FileManager] Saved %s (%d bytes)", ps.Path, len(plaintext))
}

// ===== TERMINAL TAB MANAGEMENT =====

func createTab(id byte, ws *SafeConn) {
	shell := "bash"
	var args []string
	if runtime.GOOS == "windows" {
		shell = "cmd"
		if os.Getenv("COMSPEC") != "" {
			shell = os.Getenv("COMSPEC")
		}
		args = []string{"/q"}
	} else if os.Getenv("SHELL") != "" {
		shell = os.Getenv("SHELL")
	}

	c := exec.Command(shell, args...)
	f, err := pty.Start(c)
	if err != nil {
		if runtime.GOOS == "windows" {
			log.Printf("PTY not supported on Windows, falling back to Pipes for Tab %d", id)
			runWithPipes(id, c, ws)
			return
		}
		log.Printf("Failed to start PTY for tab %d: %v", id, err)
		return
	}

	tab := &TabSession{
		Cmd:         c,
		ReadCloser:  f,
		WriteCloser: f,
		IsPipe:      false,
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

	log.Printf("[FileManager] Created file %s", reqPath)
	conn.WriteJSON(map[string]interface{}{
		"type": "control", "action": "file_created",
		"target_conn": targetConn, "path": reqPath,
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

	if err := os.MkdirAll(absPath, 0755); err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type": "control", "action": "fm_error",
			"target_conn": targetConn,
			"message":     fmt.Sprintf("create dir error: %v", err),
		})
		return
	}

	log.Printf("[FileManager] Created directory %s", reqPath)
	conn.WriteJSON(map[string]interface{}{
		"type": "control", "action": "dir_created",
		"target_conn": targetConn, "path": reqPath,
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

	log.Printf("[FileManager] Renamed %s → %s", oldPath, newPath)
	conn.WriteJSON(map[string]interface{}{
		"type": "control", "action": "file_renamed",
		"target_conn": targetConn,
		"old_path": oldPath, "new_path": newPath,
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

	log.Printf("[FileManager] Deleted %s", reqPath)
	conn.WriteJSON(map[string]interface{}{
		"type": "control", "action": "file_deleted",
		"target_conn": targetConn, "path": reqPath,
	})
}
