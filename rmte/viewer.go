package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/term"
)

func runViewer(serverURL, sessionID, password, displayName string) {
	myDispName := displayName
	if myDispName == "" {
		fmt.Print("Enter your display name: ")
		var name string
		fmt.Scanln(&name)
		myDispName = strings.TrimSpace(name)
		if myDispName == "" {
			myDispName = generateViewerID()
		}
	}

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
		"type":             "auth",
		"role":             "viewer",
		"session_id":       sessionID,
		"viewer_id":        viewerID,
		"viewer_name":      myDispName,
		"auth_token":       generateAuthToken(password),
		"protocol_version": protocolVersion,
	}
	conn.WriteJSON(auth)

	// Wait for auth success
	var authSuccess struct {
		Type     string `json:"type"`
		ViewerID string `json:"viewer_id"`
		Message  string `json:"message"`
	}
	if err := conn.ReadJSON(&authSuccess); err != nil || authSuccess.Type != "auth_success" {
		log.Fatal("Auth failed:", authSuccess.Message)
	}

	fmt.Printf("Connected as %s\n", authSuccess.ViewerID)

	var chatHistory []map[string]interface{}
	var chatHistoryMu sync.Mutex
	var inChatMode bool = false
	var inChatModeMu sync.RWMutex

	tabsSynced := make(chan bool, 1)

	// Sync tabs
	conn.WriteJSON(map[string]interface{}{
		"type":   "control",
		"action": "get_tabs",
	})

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
					fmt.Fprintln(os.Stderr, "\n[E2EE Error: Decryption failed. Please check if --pass matches the host's password exactly!]")
					continue
				}

				isJoinedMu.RLock()
				j := isJoined
				isJoinedMu.RUnlock()

				if tabID == currentTab && j {
					str := string(plaintext)
					str = strings.ReplaceAll(str, "\r\n", "\n")
					str = strings.ReplaceAll(str, "\n", "\r\n")
					os.Stdout.Write([]byte(str))
				}
			} else if mt == websocket.TextMessage {
				var ctrl struct {
					Type    string                   `json:"type"`
					Action  string                   `json:"action"`
					TabID   byte                     `json:"tab_id"`
					Tabs    []int                    `json:"tabs"`
					Data    string                   `json:"data"`
					Sender  string                   `json:"sender"`
					Message string                   `json:"message"`
					Time    string                   `json:"time"`
					History []map[string]interface{} `json:"history"`
				}
				if err := json.Unmarshal(data, &ctrl); err == nil {
					if ctrl.Action == "chat_history" {
						chatHistoryMu.Lock()
						chatHistory = ctrl.History
						chatHistoryMu.Unlock()
					} else if ctrl.Action == "chat" {
						chatHistoryMu.Lock()
						chatHistory = append(chatHistory, map[string]interface{}{
							"sender":  ctrl.Sender,
							"message": ctrl.Message,
							"time":    ctrl.Time,
						})
						chatHistoryMu.Unlock()

						isJoinedMu.RLock()
						joined := isJoined
						isJoinedMu.RUnlock()

						inChatModeMu.RLock()
						chatting := inChatMode
						inChatModeMu.RUnlock()

						if chatting {
							fmt.Printf("[%s] %s: %s\n", ctrl.Time, ctrl.Sender, ctrl.Message)
						} else if !joined {
							fmt.Printf("\n[Chat] %s: %s\n> ", ctrl.Sender, ctrl.Message)
						}
					} else if ctrl.Action == "tab_created" {
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
							isJoinedMu.RLock()
							joined := isJoined
							isJoinedMu.RUnlock()
							if !joined {
								fmt.Printf("\n[System: Tab %d Created! Press Enter to refresh.]\n> ", ctrl.TabID)
							}
						}
						tabsMu.Unlock()
					} else if ctrl.Action == "tab_deleted" {
						tabsMu.Lock()
						newTabs := []byte{}
						for _, t := range tabs {
							if t != ctrl.TabID {
								newTabs = append(newTabs, t)
							}
						}
						tabs = newTabs
						
						if currentTab == ctrl.TabID {
							if len(tabs) > 0 {
								currentTab = tabs[0]
							} else {
								currentTab = 0
							}
						}
						
						isJoinedMu.RLock()
						joined := isJoined
						isJoinedMu.RUnlock()
						if !joined {
							fmt.Printf("\n[System: Tab %d Deleted! Press Enter to refresh.]\n> ", ctrl.TabID)
						}
						tabsMu.Unlock()
					} else if ctrl.Action == "tabs_list" {
						tabsMu.Lock()
						tabs = []byte{}
						for _, t := range ctrl.Tabs {
							tabs = append(tabs, byte(t))
						}
						tabsMu.Unlock()
						select {
						case tabsSynced <- true:
						default:
						}
					} else if ctrl.Action == "sync_data" {
						payload, err := base64.StdEncoding.DecodeString(ctrl.Data)
						if err == nil {
							tabID, plaintext, err := decryptBinary(payload)
							if err != nil {
								fmt.Fprintln(os.Stderr, "\n[E2EE Error: Sync data decryption failed. Please check if --pass matches the host's password exactly!]")
							} else {
								isJoinedMu.RLock()
								j := isJoined
								isJoinedMu.RUnlock()

								if tabID == currentTab && j {
									str := string(plaintext)
									str = strings.ReplaceAll(str, "\r\n", "\n")
									str = strings.ReplaceAll(str, "\n", "\r\n")
									os.Stdout.Write([]byte(str))
								}
							}
						}
					}
				}
			}
		}
	}()

	// Wait up to 1 second for initial tabs list synchronization
	select {
	case <-tabsSynced:
	case <-time.After(1 * time.Second):
	}

	// Main loop for TUI menu
	for {
		fmt.Println("\n--- RMTE MENU ---")
		fmt.Printf("Current Tab: %d\n", currentTab)
		tabsMu.Lock()
		fmt.Println("Available Tabs:", tabs)
		tabsMu.Unlock()
		fmt.Println("Commands: [j] Join Tab, [n] New Tab, [s] Switch Tab, [d] Delete Tab, [c] Chat, [q] Quit")
		fmt.Print("> ")

		var cmd string
		fmt.Scanln(&cmd)

		switch cmd {
		case "j":
			fmt.Printf("Joining Tab %d... (Press Ctrl+] to exit back to menu)\n", currentTab)
			// Send presence set_focus first
			conn.WriteJSON(map[string]interface{}{
				"type":        "control",
				"action":      "set_focus",
				"viewer_id":   viewerID,
				"viewer_name": myDispName,
				"tab_id":      currentTab,
			})
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
			// Let background thread receive tab_created and populate available tabs list
			time.Sleep(200 * time.Millisecond)
		case "s":
			fmt.Print("Enter Tab ID: ")
			var id int
			fmt.Scanln(&id)
			currentTab = byte(id)
			// Update presence set_focus for new tab
			conn.WriteJSON(map[string]interface{}{
				"type":        "control",
				"action":      "set_focus",
				"viewer_id":   viewerID,
				"viewer_name": myDispName,
				"tab_id":      currentTab,
			})
		case "d":
			fmt.Print("Enter Tab ID to Delete: ")
			var id int
			fmt.Scanln(&id)
			conn.WriteJSON(map[string]interface{}{
				"type":   "control",
				"action": "delete_tab",
				"tab_id": id,
			})
		case "c":
			inChatModeMu.Lock()
			inChatMode = true
			inChatModeMu.Unlock()

			fmt.Println("\n=======================================================")
			fmt.Println("   RMTE CHAT ROOM - Press Enter empty or type /exit to leave")
			fmt.Println("=======================================================")
			
			// Show past history
			chatHistoryMu.Lock()
			for _, m := range chatHistory {
				t, _ := m["time"].(string)
				s, _ := m["sender"].(string)
				msgText, _ := m["message"].(string)
				fmt.Printf("[%s] %s: %s\n", t, s, msgText)
			}
			chatHistoryMu.Unlock()
			fmt.Println("-------------------------------------------------------")

			scanner := bufio.NewScanner(os.Stdin)
			for {
				fmt.Print("Chat > ")
				if !scanner.Scan() {
					break
				}
				msgInput := scanner.Text()
				msgInput = strings.TrimSpace(msgInput)
				if msgInput == "" || msgInput == "/exit" {
					break
				}

				// Send message via websocket
				conn.WriteJSON(map[string]interface{}{
					"type":    "control",
					"action":  "chat",
					"sender":  myDispName,
					"message": msgInput,
					"time":    time.Now().Format("15:04"),
				})
			}

			inChatModeMu.Lock()
			inChatMode = false
			inChatModeMu.Unlock()
			fmt.Println("\nExited chat room.")
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
