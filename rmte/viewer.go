package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/term"
)

func backoffDelay(attempt int) time.Duration {
	d := time.Duration(attempt) * time.Second
	if d > 5*time.Second {
		return 5 * time.Second
	}
	return d
}

func dialAndAuthViewer(serverURL, sessionID, password, viewerID, displayName string, ctl *ctlChannel) (*SafeConn, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return nil, err
	}

	rawConn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return nil, err
	}
	conn := &SafeConn{Conn: rawConn}

	// Keys come from the password and session id alone (see crypto.go).
	keys, err := deriveSessionKeys(password, sessionID)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := installSessionKeys(keys); err != nil {
		_ = conn.Close()
		return nil, err
	}
	ctl.setAEAD(aesGCM)

	auth := map[string]string{
		"type":             "auth",
		"role":             "viewer",
		"session_id":       sessionID,
		"viewer_id":        viewerID,
		"viewer_name":      displayName,
		"auth_token":       keys.authToken,
		"protocol_version": protocolVersion,
		"client":           "cli",
	}
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := conn.WriteJSON(auth); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetWriteDeadline(time.Time{})

	var authSuccess struct {
		Type     string `json:"type"`
		ViewerID string `json:"viewer_id"`
		Message  string `json:"message"`
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err := conn.ReadJSON(&authSuccess); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Time{})
	if authSuccess.Type != "auth_success" {
		_ = conn.Close()
		return nil, fmt.Errorf("relay rejected viewer: %s", authSuccess.Message)
	}

	// Seal all further control messages and ask the host for its epoch.
	conn.ctl = ctl
	if err := conn.WriteJSON(map[string]interface{}{"type": "control", "action": ctlHelloAction}); err != nil {
		_ = conn.Close()
		return nil, err
	}

	return conn, nil
}

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

var (
	activeTerminalRawState   *term.State
	activeTerminalRawStateMu sync.Mutex
)

func restoreActiveTerminalRawMode() {
	activeTerminalRawStateMu.Lock()
	if activeTerminalRawState != nil {
		_ = term.Restore(int(os.Stdin.Fd()), activeTerminalRawState)
		activeTerminalRawState = nil
	}
	activeTerminalRawStateMu.Unlock()
	cleanupTerminalModes()
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

	viewerID := generateViewerID()
	ctl := newCtlChannel(nil, sessionID, false)
	conn, err := dialAndAuthViewer(serverURL, sessionID, password, viewerID, myDispName, ctl)
	if err != nil {
		fatalf("Connection failed: %v", err)
	}
	vc := NewViewerConn(conn)
	defer vc.Close()

	fmt.Printf("Connected as %s\n", viewerID)

	var chatHistory []map[string]interface{}
	var chatHistoryMu sync.Mutex
	var inChatMode bool = false
	var inChatModeMu sync.RWMutex

	tabsSynced := make(chan bool, 1)

	// Announce initial presence in lobby so host and all viewers know we connected immediately
	_ = vc.WriteJSON(map[string]interface{}{
		"type":        "control",
		"action":      "unfocus",
		"viewer_id":   viewerID,
		"viewer_name": myDispName,
	})

	// Sync tabs
	_ = vc.WriteJSON(map[string]interface{}{
		"type":   "control",
		"action": "get_tabs",
	})
	_ = vc.WriteJSON(map[string]interface{}{
		"type":   "control",
		"action": "get_chat_history",
	})

	var currentTab atomic.Uint32
	currentTab.Store(0)
	var tabs []byte = []byte{0}
	var tabsMu sync.Mutex
	var isJoined atomic.Bool
	var isQuitting atomic.Bool

	// Goroutine to handle incoming messages
	go func() {
		for {
			activeRaw := vc.Get()
			if activeRaw == nil {
				if isQuitting.Load() {
					return
				}
				time.Sleep(100 * time.Millisecond)
				continue
			}

			mt, data, err := activeRaw.ReadMessage()
			if err != nil {
				if isQuitting.Load() {
					return
				}

				joined := isJoined.Load()
				curTab := byte(currentTab.Load())
				fmt.Print("\r\x1b[K\n[Connection lost. Reconnecting to server...]\n")

				reconnectStart := time.Now()
				attempt := 0
				var newConn *SafeConn
				for {
					if isQuitting.Load() {
						return
					}
					if time.Since(reconnectStart) >= 60*time.Second {
						fmt.Println("\n[Failed to reconnect within 60s. Exiting.]")
						restoreActiveTerminalRawMode()
						os.Exit(1)
					}

					attempt++
					delay := backoffDelay(attempt)
					time.Sleep(delay)

					elapsed := int(time.Since(reconnectStart).Seconds())
					fmt.Printf("\r\x1b[K[Reconnecting attempt %d (elapsed %ds/60s)...]", attempt, elapsed)
					nc, rerr := dialAndAuthViewer(serverURL, sessionID, password, viewerID, myDispName, ctl)
					if rerr == nil {
						newConn = nc
						break
					}
				}

				vc.Swap(newConn)
				fmt.Println("\r\x1b[K[Reconnected successfully!]")

				if joined {
					_ = vc.WriteJSON(map[string]interface{}{
						"type":        "control",
						"action":      "set_focus",
						"viewer_id":   viewerID,
						"viewer_name": myDispName,
						"tab_id":      curTab,
					})
					_ = vc.WriteJSON(map[string]interface{}{
						"type":   "control",
						"action": "req_sync",
						"tab_id": curTab,
					})
					fd := int(os.Stdin.Fd())
					w, h, err := term.GetSize(fd)
					if err == nil && w > 0 && h > 0 {
						_ = vc.WriteJSON(map[string]interface{}{
							"type":   "control",
							"action": "resize",
							"tab_id": curTab,
							"cols":   w,
							"rows":   h,
						})
					}
				} else {
					_ = vc.WriteJSON(map[string]interface{}{
						"type":        "control",
						"action":      "unfocus",
						"viewer_id":   viewerID,
						"viewer_name": myDispName,
					})
					_ = vc.WriteJSON(map[string]interface{}{
						"type":   "control",
						"action": "get_tabs",
					})
					_ = vc.WriteJSON(map[string]interface{}{
						"type":   "control",
						"action": "get_chat_history",
					})
					inChatModeMu.RLock()
					chatting := inChatMode
					inChatModeMu.RUnlock()
					if chatting {
						fmt.Print("Chat > ")
					} else {
						fmt.Print("> ")
					}
				}
				continue
			}

			if mt == websocket.BinaryMessage {
				if len(data) > 0 && data[0] == dataChannelTabID {
					continue // Tab 255 is the file channel; the CLI client does not use it
				}
				tabID, plaintext, err := decryptBinary(dirToViewer, data)
				if err != nil {
					fmt.Fprintln(os.Stderr, "\n[E2EE Error: Decryption failed. Please check if --pass matches the host's password exactly!]")
					continue
				}

				if tabID == byte(currentTab.Load()) && isJoined.Load() {
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
				msg, ok := clientAcceptControl(activeRaw, data)
				if !ok {
					continue
				}
				plain, _ := json.Marshal(msg)
				if err := json.Unmarshal(plain, &ctrl); err == nil {
					if ctrl.Action == "chat_history" {
						chatHistoryMu.Lock()
						if len(chatHistory) == 0 {
							chatHistory = ctrl.History
						} else {
							seen := make(map[string]bool)
							var merged []map[string]interface{}
							for _, m := range ctrl.History {
								key := fmt.Sprintf("%v|%v|%v", m["time"], m["sender"], m["message"])
								if !seen[key] {
									seen[key] = true
									merged = append(merged, m)
								}
							}
							for _, m := range chatHistory {
								key := fmt.Sprintf("%v|%v|%v", m["time"], m["sender"], m["message"])
								if !seen[key] {
									seen[key] = true
									merged = append(merged, m)
								}
							}
							chatHistory = merged
						}
						chatHistoryMu.Unlock()
					} else if ctrl.Action == "chat" {
						chatHistoryMu.Lock()
						chatHistory = append(chatHistory, map[string]interface{}{
							"sender":  ctrl.Sender,
							"message": ctrl.Message,
							"time":    ctrl.Time,
						})
						chatHistoryMu.Unlock()

						joined := isJoined.Load()

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
							if !isJoined.Load() {
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

						if byte(currentTab.Load()) == ctrl.TabID {
							if len(tabs) > 0 {
								currentTab.Store(uint32(tabs[0]))
							} else {
								currentTab.Store(0)
							}
						}

						if !isJoined.Load() {
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
							tabID, plaintext, err := decryptBinary(dirToViewer, payload)
							if err != nil {
								fmt.Fprintln(os.Stderr, "\n[E2EE Error: Sync data decryption failed. Please check if --pass matches the host's password exactly!]")
							} else {
								if tabID == byte(currentTab.Load()) && isJoined.Load() {
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
		curTab := byte(currentTab.Load())
		fmt.Println("\n--- RMTE MENU ---")
		fmt.Printf("Current Tab: %d\n", curTab)
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
		curTab = byte(currentTab.Load())

		switch cmd {
		case "j":
			fmt.Printf("Joining Tab %d... (Press Ctrl+Q or Ctrl+] to exit back to menu)\n", curTab)
			// Send presence set_focus first
			_ = vc.WriteJSON(map[string]interface{}{
				"type":        "control",
				"action":      "set_focus",
				"viewer_id":   viewerID,
				"viewer_name": myDispName,
				"tab_id":      curTab,
			})
			// Request sync for current tab
			_ = vc.WriteJSON(map[string]interface{}{
				"type":   "control",
				"action": "req_sync",
				"tab_id": curTab,
			})
			isJoined.Store(true)

			enterRawTerminal(vc, curTab)

			isJoined.Store(false)

			// Recreate reader after raw mode to guarantee no stale buffered bytes
			stdinReader = bufio.NewReader(os.Stdin)

			// Send unfocus presence to relay so other participants know we exited the tab
			_ = vc.WriteJSON(map[string]interface{}{
				"type":        "control",
				"action":      "unfocus",
				"viewer_id":   viewerID,
				"viewer_name": myDispName,
			})
		case "n":
			fmt.Println("Requesting new tab...")
			_ = vc.WriteJSON(map[string]interface{}{
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
			currentTab.Store(uint32(id))
			// Update presence set_focus for new tab
			_ = vc.WriteJSON(map[string]interface{}{
				"type":        "control",
				"action":      "set_focus",
				"viewer_id":   viewerID,
				"viewer_name": myDispName,
				"tab_id":      byte(id),
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
			_ = vc.WriteJSON(map[string]interface{}{
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

			// Request fresh chat history from host
			_ = vc.WriteJSON(map[string]interface{}{
				"type":   "control",
				"action": "get_chat_history",
			})
			time.Sleep(50 * time.Millisecond)

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
				_ = vc.WriteJSON(map[string]interface{}{
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
			isQuitting.Store(true)
			_ = vc.Close()
			return
		}
	}
}

func enterRawTerminal(vc *ViewerConn, tabID byte) {
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
	activeTerminalRawStateMu.Lock()
	activeTerminalRawState = oldState
	activeTerminalRawStateMu.Unlock()

	defer func() {
		restoreActiveTerminalRawMode()
	}()

	// Ensure local terminal is not in win32-input-mode
	cleanupTerminalModes()

	watcher := startResizeWatcher(vc, tabID, fd)
	defer watcher.Stop()

	// Send terminal size
	w, h, err := term.GetSize(fd)
	if err == nil && w > 0 && h > 0 {
		_ = vc.WriteJSON(map[string]interface{}{
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

		payload, err := encryptBinary(dirToHost, tabID, buf[:n])
		if err == nil {
			_ = vc.WriteMessage(websocket.BinaryMessage, payload)
		}
	}
}
