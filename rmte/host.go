package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"runtime"
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
	ReadCloser  io.ReadCloser
	WriteCloser io.WriteCloser
	IsPipe      bool
	Buffer      []byte
	Mutex       sync.Mutex

	// LineBuffer for Windows Pipe mode to emulate PTY backspace
	LineBuffer []byte
}

var (
	tabs   = make(map[byte]*TabSession)
	tabsMu sync.RWMutex
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
			var ctrl struct {
				Type   string `json:"type"`
				Action string `json:"action"`
				TabID  byte   `json:"tab_id"`
				Cols   uint16 `json:"cols"`
				Rows   uint16 `json:"rows"`
			}
			if err := json.Unmarshal(data, &ctrl); err == nil {
				switch ctrl.Action {
				case "request_new_tab":
					newID := byte(len(tabs))
					createTab(newID, conn)
					conn.WriteJSON(map[string]interface{}{
						"type":   "control",
						"action": "tab_created",
						"tab_id": newID,
					})
				case "resize":
					tabsMu.RLock()
					tab, ok := tabs[ctrl.TabID]
					tabsMu.RUnlock()
					if ok {
						if f, isFile := tab.ReadCloser.(*os.File); isFile {
							pty.Setsize(f, &pty.Winsize{Cols: ctrl.Cols, Rows: ctrl.Rows})
						}
					}
				case "req_sync":
					// Sync history to viewer
					tabsMu.RLock()
					tab, ok := tabs[ctrl.TabID]
					tabsMu.RUnlock()
					if ok {
						tab.Mutex.Lock()
						history := make([]byte, len(tab.Buffer))
						copy(history, tab.Buffer)
						tab.Mutex.Unlock()
						
						// In a real multi-user scenario, we might want to send only to the requesting viewer.
						// But for simplicity, we just broadcast or the server handles it.
						// The server doesn't know who requested it in this simple model.
						// We'll just send it back.
						payload, _ := encryptBinary(ctrl.TabID, history)
						conn.WriteMessage(websocket.BinaryMessage, payload)
					}
				}
			}
		}
	}
}

func createTab(id byte, ws *SafeConn) {
	shell := "bash"
	if runtime.GOOS == "windows" {
		shell = "cmd"
		if os.Getenv("COMSPEC") != "" {
			shell = os.Getenv("COMSPEC")
		}
	} else if os.Getenv("SHELL") != "" {
		shell = os.Getenv("SHELL")
	}

	c := exec.Command(shell)
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
