package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"sync"

	"github.com/gorilla/websocket"
	"golang.org/x/term"
)

func runViewer(serverURL, sessionID, password string) {
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

	viewerID := generateViewerID()
	auth := map[string]string{
		"type":       "auth",
		"role":       "viewer",
		"session_id": sessionID,
		"viewer_id":  viewerID,
	}
	conn.WriteJSON(auth)

	var authResp struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := conn.ReadJSON(&authResp); err != nil || authResp.Type == "error" {
		log.Fatal("Auth failed:", authResp.Message)
	}

	fmt.Printf("Connected as %s\n", viewerID)

	var currentTab byte = 0
	var tabs []byte = []byte{0}
	var tabsMu sync.Mutex
	var isJoined bool = false
	var isJoinedMu sync.RWMutex

	// Goroutine to handle incoming messages
	go func() {
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				fmt.Println("\nDisconnected from server.")
				os.Exit(0)
			}

			if mt == websocket.BinaryMessage {
				tabID, plaintext, err := decryptBinary(data)
				if err != nil {
					continue
				}

				isJoinedMu.RLock()
				j := isJoined
				isJoinedMu.RUnlock()

				if tabID == currentTab && j {
					os.Stdout.Write(plaintext)
				}
			} else if mt == websocket.TextMessage {
				var ctrl struct {
					Type   string `json:"type"`
					Action string `json:"action"`
					TabID  byte   `json:"tab_id"`
				}
				if err := json.Unmarshal(data, &ctrl); err == nil {
					if ctrl.Action == "tab_created" {
						tabsMu.Lock()
						exists := false
						for _, t := range tabs {
							if t == ctrl.TabID {
								exists = true
								break
							}
						}
						if !exists {
							tabs = append(tabs, ctrl.TabID)
						}
						tabsMu.Unlock()
					}
				}
			}
		}
	}()

	// Main loop for TUI menu
	for {
		fmt.Println("\n--- RMTE MENU ---")
		fmt.Printf("Current Tab: %d\n", currentTab)
		fmt.Println("Available Tabs:", tabs)
		fmt.Println("Commands: [j] Join Tab, [n] New Tab, [s] Switch Tab, [q] Quit")
		fmt.Print("> ")

		var cmd string
		fmt.Scanln(&cmd)

		switch cmd {
		case "j":
			fmt.Printf("Joining Tab %d... (Press Ctrl+] to exit back to menu)\n", currentTab)
			// Request sync for current tab
			conn.WriteJSON(map[string]interface{}{
				"type":   "control",
				"action": "req_sync",
				"tab_id": currentTab,
			})
			isJoinedMu.Lock()
			isJoined = true
			isJoinedMu.Unlock()
			
			enterRawTerminal(conn, currentTab)
			
			isJoinedMu.Lock()
			isJoined = false
			isJoinedMu.Unlock()
		case "n":
			fmt.Println("Requesting new tab...")
			conn.WriteJSON(map[string]interface{}{
				"type":   "control",
				"action": "request_new_tab",
			})
		case "s":
			fmt.Print("Enter Tab ID: ")
			var id int
			fmt.Scanln(&id)
			currentTab = byte(id)
		case "q":
			return
		}
	}
}

func enterRawTerminal(ws *SafeConn, tabID byte) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		fmt.Println("Not a terminal")
		return
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		fmt.Println("Failed to enter raw mode:", err)
		return
	}
	defer term.Restore(fd, oldState)

	// Send terminal size
	w, h, err := term.GetSize(fd)
	if err == nil {
		ws.WriteJSON(map[string]interface{}{
			"type":   "control",
			"action": "resize",
			"tab_id": tabID,
			"cols":   w,
			"rows":   h,
		})
	}

	buf := make([]byte, 1024)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			break
		}

		for i := 0; i < n; i++ {
			// ESCAPE KEY: Ctrl+] (ASCII 29)
			if buf[i] == 29 {
				return
			}
		}

		payload, err := encryptBinary(tabID, buf[:n])
		if err == nil {
			ws.WriteMessage(websocket.BinaryMessage, payload)
		}
	}
}
