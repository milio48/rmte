package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xtaci/smux/v2"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type Session struct {
	ID          string
	Host        *SafeConn
	Viewers     map[string]map[string]*SafeConn // viewerID -> connID -> Conn
	ChatHistory []map[string]interface{}
	AuthToken   string // S4: password-derived token for access control
	Mutex       sync.RWMutex

	// Preview Proxy fields
	PreviewEnabled bool
	ProxySecret    string
	SmuxSession    *smux.Session
	SmuxMu         sync.RWMutex

	// Metrics & Admin monitoring
	HostIP     string
	CreatedAt  time.Time
	LastActive time.Time
	BytesRx    uint64
	BytesTx    uint64
	FileBytes  uint64
}

const maxViewersPerSession = 50

var (
	sessions  = make(map[string]*Session)
	sessionMu sync.RWMutex

	// serverCfg is the active relay configuration (set by runServer)
	serverCfg *ServeConfig
)

func runServer(cfg *ServeConfig) {
	serverCfg = cfg
	mux := http.NewServeMux()
	mux.HandleFunc(cfg.WSPath, handleWS)
	mux.HandleFunc(cfg.WSPath+"-proxy", handleWSProxy)
	mux.HandleFunc("/p/", handlePreviewHTTP)
	if !cfg.NoWeb {
		setupWebHandler(mux, cfg.WebPath, cfg.WSPath)
	}
	if cfg.AdminPass != "" {
		setupAdminHandler(mux, cfg.AdminPath, cfg.AdminPass)
	}

	bindHost := "127.0.0.1"
	if cfg.Public {
		bindHost = "0.0.0.0"
	}
	addr := fmt.Sprintf("%s:%d", bindHost, cfg.Port)
	fmt.Printf("Relay Server started on %s\n", addr)
	if cfg.AdminPass != "" {
		fmt.Printf("Relay Admin Dashboard enabled at %s\n", cfg.AdminPath)
	}
	log.Fatal(http.ListenAndServe(addr, mux))
}

func handleWS(w http.ResponseWriter, r *http.Request) {
	clientIP := getClientIP(r)
	if isIPBanned(clientIP) {
		http.Error(w, "Forbidden: IP address is banned on this relay", http.StatusForbidden)
		return
	}

	rawConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn := &SafeConn{Conn: rawConn}
	defer conn.Close()

	var role string
	var sessionID string
	var viewerID string
	var connID = fmt.Sprintf("c-%d", time.Now().UnixNano())

	// Wait for auth message
	_, msg, err := conn.ReadMessage()
	if err != nil {
		return
	}

	var auth struct {
		Type            string `json:"type"`
		Role            string `json:"role"`
		SessionID       string `json:"session_id"`
		ViewerID        string `json:"viewer_id"`
		AuthToken       string `json:"auth_token"`
		ProtocolVersion string `json:"protocol_version"`
		InternalToken   string `json:"internal_token"`
		Client          string `json:"client"` // "web" | "cli" (self-declared)
		Preview         bool   `json:"preview"`
	}

	if err := json.Unmarshal(msg, &auth); err != nil || auth.Type != "auth" {
		return
	}

	role = auth.Role
	sessionID = auth.SessionID
	viewerID = auth.ViewerID

	if role == "host" {
		// Standalone mode: only the embedded host (holding the internal token) may register
		if serverCfg != nil && serverCfg.Mode == modeStandalone && auth.InternalToken != serverCfg.InternalToken {
			conn.WriteJSON(map[string]string{"type": "error", "message": "relay disabled: server is running in standalone mode"})
			fmt.Printf("External host rejected (standalone mode) from %s\n", r.RemoteAddr)
			return
		}

		sessionMu.Lock()
		if auth.SessionID != "" {
			reqID := strings.ToLower(strings.TrimSpace(auth.SessionID))
			if err := validateSessionID(reqID); err != nil {
				sessionMu.Unlock()
				conn.WriteJSON(map[string]string{"type": "error", "message": "invalid session_id: " + err.Error()})
				fmt.Printf("Host rejected from %s: invalid session_id %q: %v\n", r.RemoteAddr, auth.SessionID, err)
				return
			}
			if _, exists := sessions[reqID]; exists {
				sessionMu.Unlock()
				conn.WriteJSON(map[string]string{"type": "error", "message": fmt.Sprintf("session ID %q is already in use", reqID)})
				fmt.Printf("Host rejected from %s: session ID %q is already in use\n", r.RemoteAddr, reqID)
				return
			}
			sessionID = reqID
		} else {
			for {
				randBytes := make([]byte, 4)
				rand.Read(randBytes)
				cand := hex.EncodeToString(randBytes)
				if _, exists := sessions[cand]; !exists {
					sessionID = cand
					break
				}
			}
		}

		var proxySecret string
		if auth.Preview {
			proxySecret = generatePassword(32)
		}

		s := &Session{
			ID:             sessionID,
			Host:           conn,
			Viewers:        make(map[string]map[string]*SafeConn),
			ChatHistory:    make([]map[string]interface{}, 0),
			AuthToken:      auth.AuthToken,
			PreviewEnabled: auth.Preview,
			ProxySecret:    proxySecret,
			HostIP:         clientIP,
			CreatedAt:      time.Now(),
			LastActive:     time.Now(),
		}
		sessions[sessionID] = s
		sessionMu.Unlock()

		// Send back the session ID plus relay paths so the host can build correct links
		resp := map[string]interface{}{
			"type":       "auth_success",
			"session_id": sessionID,
		}
		if serverCfg != nil {
			resp["web_path"] = serverCfg.WebPath
			resp["ws_path"] = serverCfg.WSPath
			resp["no_web"] = serverCfg.NoWeb
			resp["no_cli"] = serverCfg.NoCLI
		}
		if auth.Preview {
			resp["preview_enabled"] = true
			resp["proxy_secret"] = proxySecret
			if serverCfg != nil {
				resp["ws_proxy_path"] = serverCfg.WSPath + "-proxy"
			}
			resp["capabilities"] = []string{"preview_v1"}
		}
		conn.WriteJSON(resp)

		fmt.Printf("Host connected. Session: %s (Protocol: %s)\n", sessionID, auth.ProtocolVersion)

		defer func() {
			sessionMu.Lock()
			if sess, ok := sessions[sessionID]; ok {
				recordClosedSession(sess, "Closed by host")
				sess.SmuxMu.Lock()
				if sess.SmuxSession != nil {
					sess.SmuxSession.Close()
				}
				sess.SmuxMu.Unlock()
			}
			delete(sessions, sessionID)
			sessionMu.Unlock()
			fmt.Printf("Host disconnected. Session %s closed.\n", sessionID)
		}()
	} else {
		// Soft restriction: clients self-declare their type, so this can be spoofed
		if serverCfg != nil && serverCfg.NoCLI && auth.Client == "cli" {
			conn.WriteJSON(map[string]string{"type": "error", "message": "CLI clients are disabled on this relay"})
			return
		}

		sessionMu.RLock()
		s, ok := sessions[sessionID]
		sessionMu.RUnlock()

		if !ok {
			conn.WriteJSON(map[string]string{"type": "error", "message": "session not found"})
			return
		}

		// S4: Validate auth token
		if s.AuthToken != "" && auth.AuthToken != s.AuthToken {
			conn.WriteJSON(map[string]string{"type": "error", "message": "invalid password"})
			fmt.Printf("Viewer %s rejected: invalid auth token for session %s\n", viewerID, sessionID)
			return
		}

		// P7: Check viewer limit
		s.Mutex.RLock()
		viewerCount := len(s.Viewers)
		s.Mutex.RUnlock()
		if viewerCount >= maxViewersPerSession {
			conn.WriteJSON(map[string]string{"type": "error", "message": "session full"})
			return
		}

		s.Mutex.Lock()
		if s.Viewers[viewerID] == nil {
			s.Viewers[viewerID] = make(map[string]*SafeConn)
		}
		s.Viewers[viewerID][connID] = conn
		s.Mutex.Unlock()

		conn.WriteJSON(map[string]interface{}{
			"type":      "auth_success",
			"viewer_id": viewerID,
			"conn_id":   connID,
		})

		s.Mutex.RLock()
		historyMsg := map[string]interface{}{
			"type":    "control",
			"action":  "chat_history",
			"history": s.ChatHistory,
		}
		s.Mutex.RUnlock()
		conn.WriteJSON(historyMsg)
		fmt.Printf("Viewer %s connected to session %s (Conn: %s, Protocol: %s)\n", viewerID, sessionID, connID, auth.ProtocolVersion)

		defer func() {
			s.Mutex.Lock()
			delete(s.Viewers[viewerID], connID)
			hasOtherConn := false
			if len(s.Viewers[viewerID]) == 0 {
				delete(s.Viewers, viewerID)
			} else {
				hasOtherConn = true
			}
			s.Mutex.Unlock()
			fmt.Printf("Viewer %s disconnected from session %s\n", viewerID, sessionID)

			if !hasOtherConn {
				s.Host.WriteJSON(map[string]interface{}{
					"type":      "control",
					"action":    "viewer_disconnected",
					"viewer_id": viewerID,
				})
			}
		}()
	}

	// Heartbeat goroutine
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(10*time.Second)); err != nil {
					return
				}
			}
		}
	}()

	conn.SetReadDeadline(time.Now().Add(35 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(35 * time.Second))
		return nil
	})

	// Message loop
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			break
		}

		sessionMu.RLock()
		s, ok := sessions[sessionID]
		sessionMu.RUnlock()

		if !ok {
			break
		}

		msgLen := uint64(len(data))
		atomic.AddUint64(&s.BytesRx, msgLen)
		s.LastActive = time.Now()

		if mt == websocket.BinaryMessage {
			if len(data) > 0 && data[0] == 255 {
				atomic.AddUint64(&s.FileBytes, msgLen)
			}
			// Proxy binary message
			if role == "host" {
				// Broadcast to all viewers
				s.Mutex.RLock()
				vCount := 0
				for _, conns := range s.Viewers {
					for _, vConn := range conns {
						vConn.WriteMessage(websocket.BinaryMessage, data)
						vCount++
					}
				}
				s.Mutex.RUnlock()
				if vCount > 0 {
					atomic.AddUint64(&s.BytesTx, msgLen*uint64(vCount))
				}
			} else {
				// Send to host
				s.Host.WriteMessage(websocket.BinaryMessage, data)
				atomic.AddUint64(&s.BytesTx, msgLen)
			}
		} else if mt == websocket.TextMessage {
			// Handle control messages
			var ctrl map[string]interface{}
			if err := json.Unmarshal(data, &ctrl); err == nil {
				action, _ := ctrl["action"].(string)

				if role == "viewer" {
					if action == "chat" {
						s.Mutex.Lock()
						s.ChatHistory = append(s.ChatHistory, ctrl)
						if len(s.ChatHistory) > 50 {
							s.ChatHistory = s.ChatHistory[1:]
						}
						s.Mutex.Unlock()

						s.Mutex.RLock()
						for _, conns := range s.Viewers {
							for _, vConn := range conns {
								vConn.WriteMessage(websocket.TextMessage, data)
							}
						}
						s.Mutex.RUnlock()
					} else if action == "ping" {
						conn.WriteJSON(map[string]interface{}{
							"type":   "control",
							"action": "pong",
							"t":      ctrl["t"],
						})
					} else if action == "req_sync" ||
						action == "req_dir" ||
						action == "req_read_file" ||
						action == "prepare_save" ||
						action == "prepare_upload" ||
						action == "create_file" ||
						action == "create_dir" ||
						action == "rename_file" ||
						action == "delete_file" {
						// Inject caller's connID so host can route responses back
						ctrl["target_conn"] = connID
						newData, _ := json.Marshal(ctrl)
						s.Host.WriteMessage(websocket.TextMessage, newData)
					} else {
						s.Host.WriteMessage(websocket.TextMessage, data)
					}
				} else {
					targetConn, hasTarget := ctrl["target_conn"].(string)

					if hasTarget && targetConn != "" {
						s.Mutex.RLock()
						// Route specific JSON message to target_conn
						for _, conns := range s.Viewers {
							if vConn, exists := conns[targetConn]; exists {
								vConn.WriteMessage(websocket.TextMessage, data)
								break
							}
						}
						s.Mutex.RUnlock()
					} else {
						if action == "chat" {
							s.Mutex.Lock()
							s.ChatHistory = append(s.ChatHistory, ctrl)
							if len(s.ChatHistory) > 50 {
								s.ChatHistory = s.ChatHistory[1:]
							}
							s.Mutex.Unlock()
						}

						s.Mutex.RLock()
						// Broadcast to all viewers
						for _, conns := range s.Viewers {
							for _, vConn := range conns {
								vConn.WriteMessage(websocket.TextMessage, data)
							}
						}
						s.Mutex.RUnlock()
					}
				}
			}
		}
	}
}

// validateSessionID verifies that a session ID contains only lowercase
// alphanumeric characters (a-z, 0-9) and does not exceed 10 characters.
func validateSessionID(id string) error {
	if len(id) == 0 {
		return fmt.Errorf("session ID cannot be empty")
	}
	if len(id) > 10 {
		return fmt.Errorf("session ID %q exceeds maximum length of 10 characters", id)
	}
	for _, ch := range id {
		if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9')) {
			return fmt.Errorf("session ID %q must only contain lowercase alphanumeric characters (a-z, 0-9)", id)
		}
	}
	return nil
}

