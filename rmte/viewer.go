package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/term"
)

var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1bO[A-Za-z]|\x1b.`)

// win32InputRegex matches Windows Terminal / ConPTY win32-input-mode sequences:
// \x1b[Vk;Sc;Uc;Kd;Cs;Rc_
var win32InputRegex = regexp.MustCompile(`\x1b\[(\d+);(\d+);(\d+);(\d+);(\d+);(\d+)_`)

func stripAnsiEscapes(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

var (
	sanitizerCarryMu sync.Mutex
	sanitizerCarry   string
)

func sanitizeTerminalOutput(s string) string {
	// Suppress private escape sequence \x1b[?9001h (win32-input-mode enable).
	// ConPTY on Windows hosts emits this sequence by default. If forwarded to a
	// Windows client terminal (e.g. Windows Terminal), the local terminal switches
	// to win32-input-mode, serializing key presses into \x1b[Vk;Sc;Uc;Kd;Cs;Rc_
	// which breaks raw ASCII byte reading (including Ctrl+] / 0x1D) and corrupts TUI menus.
	sanitizerCarryMu.Lock()
	defer sanitizerCarryMu.Unlock()

	combined := sanitizerCarry + s
	sanitizerCarry = ""

	// Strip any complete sequence
	combined = strings.ReplaceAll(combined, "\x1b[?9001h", "")

	// Chunk-fragmentation defense: preserve trailing partial prefix for next frame
	target := "\x1b[?9001h"
	for i := len(target) - 1; i >= 1; i-- {
		if strings.HasSuffix(combined, target[:i]) {
			sanitizerCarry = target[:i]
			combined = combined[:len(combined)-i]
			break
		}
	}
	return combined
}

func isExitKeySequence(buf []byte) bool {
	// 1. Raw ASCII control keys:
	// ASCII 17 (0x11) = Ctrl+Q (guaranteed letter-based C0 code across all Windows/Unix terminals)
	// ASCII 29 (0x1D) = Ctrl+] (traditional Telnet/RMTE escape shortcut)
	for _, b := range buf {
		if b == 17 || b == 29 {
			return true
		}
	}

	// 2. Extended escape sequences (CSI-u / modifyOtherKeys):
	// \x1b[113;5u = Ctrl+Q (113 = 'q', 5 = Ctrl)
	// \x1b[93;5u  = Ctrl+] (93 = ']', 5 = Ctrl)
	str := string(buf)
	if strings.Contains(str, "\x1b[113;5u") || strings.Contains(str, "\x1b[93;5u") {
		return true
	}

	// 3. Windows win32-input-mode escape sequences:
	// Format: \x1b[Vk;Sc;Uc;Kd;Cs;Rc_
	// Vk: Virtual Key Code (81 for 'Q', 221 for VK_OEM_6 ']')
	// Uc: Unicode character (17 for Ctrl+Q, 29 for Ctrl+])
	// Kd: 1 for KeyDown, 0 for KeyUp
	// Cs: Control keys bitmask (0x0004 Right Ctrl, 0x0008 Left Ctrl)
	if strings.Contains(str, "\x1b[") && strings.Contains(str, "_") {
		matches := win32InputRegex.FindAllStringSubmatch(str, -1)
		for _, m := range matches {
			if len(m) == 7 {
				vk, _ := strconv.Atoi(m[1])
				uc, _ := strconv.Atoi(m[3])
				kd, _ := strconv.Atoi(m[4])
				cs, _ := strconv.Atoi(m[5])

				// Only consider key-down events (kd == 1)
				if kd == 1 {
					isCtrl := (cs & 0x0C) != 0
					if uc == 17 || uc == 29 {
						return true
					}
					if isCtrl && (vk == 81 || vk == 221) {
						return true
					}
				}
			}
		}
	}

	return false
}

func cleanupTerminalModes() {
	// Disable win32-input-mode (?9001l), focus reporting (?1004l), bracketed paste (?2004l),
	// mouse tracking (?1000-1006l), and show cursor (?25h)
	os.Stdout.WriteString("\x1b[?9001l\x1b[?1004l\x1b[?2004l\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?25h")
}

func runViewer(serverURL, sessionID, password, displayName string) {
	cleanupTerminalModes()
	stdinReader := bufio.NewReader(os.Stdin)

	myDispName := displayName
	if myDispName == "" {
		fmt.Print("Enter your display name: ")
		name, _ := stdinReader.ReadString('\n')
		myDispName = stripAnsiEscapes(strings.TrimSpace(name))
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
		"client":           "cli",
	}
	conn.WriteJSON(auth)

	// Wait for auth success
	var authSuccess struct {
		Type     string `json:"type"`
		ViewerID string `json:"viewer_id"`
		Message  string `json:"message"`
	}
	if err := conn.ReadJSON(&authSuccess); err != nil {
		log.Fatal("Auth failed: ", err)
	}
	if authSuccess.Type != "auth_success" {
		fatalf("Relay rejected viewer: %s", authSuccess.Message)
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
				if len(data) > 0 && data[0] == dataChannelTabID {
					continue // Tab 255 is the file channel; the CLI client does not use it
				}
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
					str = sanitizeTerminalOutput(str)
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
							fmt.Printf("\r\x1b[K[%s] %s: %s\nChat > ", ctrl.Time, ctrl.Sender, ctrl.Message)
						} else if !joined {
							fmt.Printf("\r\x1b[K\n[Chat] %s: %s\n> ", ctrl.Sender, ctrl.Message)
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
							if len(payload) > 0 && payload[0] == dataChannelTabID {
								continue // Tab 255 is the file channel; the CLI client does not use it
							}
							tabID, plaintext, err := decryptBinary(payload)
							if err != nil {
								fmt.Fprintln(os.Stderr, "\n[E2EE Error: Sync data decryption failed. Please check if --pass matches the host's password exactly!]")
							} else {
								isJoinedMu.RLock()
								j := isJoined
								isJoinedMu.RUnlock()

								if tabID == currentTab && j {
									str := string(plaintext)
									str = sanitizeTerminalOutput(str)
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
	cleanupTerminalModes()
	for {
		fmt.Println("\n--- RMTE MENU ---")
		fmt.Printf("Current Tab: %d\n", currentTab)
		tabsMu.Lock()
		fmt.Println("Available Tabs:", tabs)
		tabsMu.Unlock()
		fmt.Println("Commands: [j] Join Tab, [n] New Tab, [s] Switch Tab, [d] Delete Tab, [c] Chat, [q] Quit")
		fmt.Print("> ")

		line, err := stdinReader.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToLower(stripAnsiEscapes(strings.TrimSpace(line)))

		switch cmd {
		case "j":
			fmt.Printf("Joining Tab %d... (Press Ctrl+Q or Ctrl+] to exit back to menu)\n", currentTab)
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

			// Recreate reader after raw mode to guarantee no stale buffered bytes
			stdinReader = bufio.NewReader(os.Stdin)

			// Send unfocus presence to relay so other participants know we exited the tab
			conn.WriteJSON(map[string]interface{}{
				"type":        "control",
				"action":      "unfocus",
				"viewer_id":   viewerID,
				"viewer_name": myDispName,
			})
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
			line, _ := stdinReader.ReadString('\n')
			idStr := stripAnsiEscapes(strings.TrimSpace(line))
			id, err := strconv.Atoi(idStr)
			if err != nil || id < 0 || id > 254 {
				fmt.Println("Invalid Tab ID (must be 0-254).")
				continue
			}
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
			line, _ := stdinReader.ReadString('\n')
			idStr := stripAnsiEscapes(strings.TrimSpace(line))
			id, err := strconv.Atoi(idStr)
			if err != nil || id < 0 || id > 254 {
				fmt.Println("Invalid Tab ID (must be 0-254).")
				continue
			}
			conn.WriteJSON(map[string]interface{}{
				"type":   "control",
				"action": "delete_tab",
				"tab_id": id,
			})
		case "c":
			cleanupTerminalModes()
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

			for {
				fmt.Print("Chat > ")
				line, err := stdinReader.ReadString('\n')
				if err != nil {
					break
				}
				msgInput := stripAnsiEscapes(strings.TrimSpace(line))
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
			cleanupTerminalModes()
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
	defer func() {
		term.Restore(fd, oldState)
		cleanupTerminalModes()
	}()

	// Ensure local terminal is not in win32-input-mode
	cleanupTerminalModes()

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

		if isExitKeySequence(buf[:n]) {
			return
		}

		payload, err := encryptBinary(tabID, buf[:n])
		if err == nil {
			ws.WriteMessage(websocket.BinaryMessage, payload)
		}
	}
}
