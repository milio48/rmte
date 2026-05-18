package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
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
}

const maxViewersPerSession = 50

var (
	sessions  = make(map[string]*Session)
	sessionMu sync.RWMutex
)

func runServer(port int) {
	http.HandleFunc("/ws", handleWS)
	// Web UI handler will be added in web.go
	setupWebHandler()

	addr := fmt.Sprintf(":%d", port)
	fmt.Printf("Relay Server started on %s\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func handleWS(w http.ResponseWriter, r *http.Request) {
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
	}

	if err := json.Unmarshal(msg, &auth); err != nil || auth.Type != "auth" {
		return
	}

	role = auth.Role
	sessionID = auth.SessionID
	viewerID = auth.ViewerID

	if role == "host" {
		randBytes := make([]byte, 4)
		rand.Read(randBytes)
		sessionID = hex.EncodeToString(randBytes)

		s := &Session{
			ID:          sessionID,
			Host:        conn,
			Viewers:     make(map[string]map[string]*SafeConn),
			ChatHistory: make([]map[string]interface{}, 0),
			AuthToken:   auth.AuthToken,
		}
		sessionMu.Lock()
		sessions[sessionID] = s
		sessionMu.Unlock()

		// Send back the session ID
		conn.WriteJSON(map[string]interface{}{
			"type":       "auth_success",
			"session_id": sessionID,
		})
		
		fmt.Printf("Host connected. Session: %s (Protocol: %s)\n", sessionID, auth.ProtocolVersion)
		
		defer func() {
			sessionMu.Lock()
			delete(sessions, sessionID)
			sessionMu.Unlock()
			fmt.Printf("Host disconnected. Session %s closed.\n", sessionID)
		}()
	} else {
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

		if mt == websocket.BinaryMessage {
			// Proxy binary message
			if role == "host" {
				// Broadcast to all viewers
				s.Mutex.RLock()
				for _, conns := range s.Viewers {
					for _, vConn := range conns {
						vConn.WriteMessage(websocket.BinaryMessage, data)
					}
				}
				s.Mutex.RUnlock()
			} else {
				// Send to host
				s.Host.WriteMessage(websocket.BinaryMessage, data)
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
					} else if action == "req_sync" {
						ctrl["target_conn"] = connID
						newData, _ := json.Marshal(ctrl)
						s.Host.WriteMessage(websocket.TextMessage, newData)
					} else {
						s.Host.WriteMessage(websocket.TextMessage, data)
					}
				} else {
					targetConn, hasTarget := ctrl["target_conn"].(string)
					
					if hasTarget {
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
