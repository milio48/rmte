package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"sync"
	"time"
	"io"

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

var (
	tabs   = make(map[byte]*TabSession)
	tabsMu sync.RWMutex

	presenceMap   = make(map[string]ViewersPresence)
	presenceMutex sync.Mutex
)

const maxBufferSize = 100 * 1024

func runHost(serverURL, password string) {
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
		"type": "auth",
		"role": "host",
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
							tab.LineBuffer = append(tab.LineBuffer, '\n')
							tab.WriteCloser.Write(tab.LineBuffer)
							tab.LineBuffer = nil
						} else if b == '\x7f' || b == '\x08' {
							// Backspace: remove last character and erase visually
							if len(tab.LineBuffer) > 0 {
								tab.LineBuffer = tab.LineBuffer[:len(tab.LineBuffer)-1]
								payload, _ := encryptBinary(tabID, []byte("\b \b"))
								conn.WriteMessage(websocket.BinaryMessage, payload)
							}
						} else {
							// Normal character
							tab.LineBuffer = append(tab.LineBuffer, b)
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
					var activeTabs []int
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
					newID := byte(len(tabs))
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
				}
			}
		}
	}
}

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
