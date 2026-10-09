package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// BannedIPInfo holds details of a blocked client IP.
type BannedIPInfo struct {
	IP       string    `json:"ip"`
	Reason   string    `json:"reason"`
	BannedAt time.Time `json:"banned_at"`
}

// SessionHistoryInfo holds historical stats of closed sessions.
type SessionHistoryInfo struct {
	SessionID string    `json:"session_id"`
	HostIP    string    `json:"host_ip"`
	CreatedAt time.Time `json:"created_at"`
	ClosedAt  time.Time `json:"closed_at"`
	Duration  string    `json:"duration"`
	BytesRx   uint64    `json:"bytes_rx"`
	BytesTx   uint64    `json:"bytes_tx"`
	FileBytes uint64    `json:"file_bytes"`
	Reason    string    `json:"reason"`
}

// ActiveSessionDTO is JSON serialization for currently active sessions.
type ActiveSessionDTO struct {
	ID             string    `json:"id"`
	HostIP         string    `json:"host_ip"`
	CreatedAt      time.Time `json:"created_at"`
	Uptime         string    `json:"uptime"`
	ViewersCount   int       `json:"viewers_count"`
	BytesRx        uint64    `json:"bytes_rx"`
	BytesTx        uint64    `json:"bytes_tx"`
	FileBytes      uint64    `json:"file_bytes"`
	PreviewEnabled bool      `json:"preview_enabled"`
}

// AdminStatsResponse is the dashboard payload.
type AdminStatsResponse struct {
	ServerVersion string               `json:"server_version"`
	RelayUptime   string               `json:"relay_uptime"`
	TotalSessions int                  `json:"total_sessions"`
	TotalViewers  int                  `json:"total_viewers"`
	ActiveSessions []ActiveSessionDTO  `json:"active_sessions"`
	History       []SessionHistoryInfo `json:"history"`
	BannedIPs     []BannedIPInfo       `json:"banned_ips"`
}

var (
	bannedIPs       = make(map[string]BannedIPInfo)
	bannedMu        sync.RWMutex
	sessionHistory  = make([]SessionHistoryInfo, 0, 100)
	historyMu       sync.RWMutex
	adminTokens     = make(map[string]time.Time)
	adminTokenMu    sync.RWMutex
	serverStartTime = time.Now()
)

// getClientIP extracts real client IP with priority for Cloudflare CF-Connecting-IP, validating with net.ParseIP.
func getClientIP(r *http.Request) string {
	candidates := []string{
		r.Header.Get("CF-Connecting-IP"),
		"", // will populate with first X-Forwarded-For part below
		r.Header.Get("X-Real-IP"),
	}
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			candidates[1] = parts[0]
		}
	}

	for _, cand := range candidates {
		cand = strings.TrimSpace(cand)
		if cand != "" && net.ParseIP(cand) != nil {
			return cand
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && net.ParseIP(host) != nil {
		return host
	}
	if net.ParseIP(r.RemoteAddr) != nil {
		return r.RemoteAddr
	}
	return "unknown"
}

func isIPBanned(ip string) bool {
	bannedMu.RLock()
	defer bannedMu.RUnlock()
	_, found := bannedIPs[ip]
	return found
}

func banIP(ip, reason string) {
	if ip == "" {
		return
	}
	bannedMu.Lock()
	defer bannedMu.Unlock()
	bannedIPs[ip] = BannedIPInfo{
		IP:       ip,
		Reason:   reason,
		BannedAt: time.Now(),
	}
}

func unbanIP(ip string) {
	bannedMu.Lock()
	defer bannedMu.Unlock()
	delete(bannedIPs, ip)
}

func getBannedIPList() []BannedIPInfo {
	bannedMu.RLock()
	defer bannedMu.RUnlock()
	list := make([]BannedIPInfo, 0, len(bannedIPs))
	for _, info := range bannedIPs {
		list = append(list, info)
	}
	return list
}

func recordClosedSessionLocked(sess *Session, reason string) {
	if sess == nil {
		return
	}
	historyMu.Lock()
	defer historyMu.Unlock()

	dur := time.Since(sess.CreatedAt).Round(time.Second)
	info := SessionHistoryInfo{
		SessionID: sess.ID,
		HostIP:    sess.HostIP,
		CreatedAt: sess.CreatedAt,
		ClosedAt:  time.Now(),
		Duration:  dur.String(),
		BytesRx:   atomic.LoadUint64(&sess.BytesRx),
		BytesTx:   atomic.LoadUint64(&sess.BytesTx),
		FileBytes: atomic.LoadUint64(&sess.FileBytes),
		Reason:    reason,
	}

	// Keep max 100 historical entries (FIFO)
	if len(sessionHistory) >= 100 {
		sessionHistory = sessionHistory[1:]
	}
	sessionHistory = append(sessionHistory, info)
}

func recordClosedSession(sess *Session, reason string) {
	if sess == nil {
		return
	}
	sess.Mutex.RLock()
	defer sess.Mutex.RUnlock()
	recordClosedSessionLocked(sess, reason)
}

func terminateSession(sessionID string) bool {
	sessionMu.Lock()
	sess, exists := sessions[sessionID]
	if !exists {
		sessionMu.Unlock()
		return false
	}
	delete(sessions, sessionID)
	sessionMu.Unlock()

	sess.Mutex.Lock()
	if sess.ReconnectTimer != nil {
		sess.ReconnectTimer.Stop()
		sess.ReconnectTimer = nil
	}
	if sess.Host != nil {
		_ = sess.Host.Close()
		sess.Host = nil
	}
	for _, viewerConns := range sess.Viewers {
		for _, vConn := range viewerConns {
			_ = vConn.Close()
		}
	}
	recordClosedSessionLocked(sess, "Terminated by admin")
	sess.Mutex.Unlock()

	sess.SmuxMu.Lock()
	if sess.SmuxSession != nil {
		_ = sess.SmuxSession.Close()
	}
	sess.SmuxMu.Unlock()

	return true
}

func generateAdminToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("token-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func isValidAdminToken(token string) bool {
	if token == "" {
		return false
	}
	adminTokenMu.RLock()
	exp, exists := adminTokens[token]
	adminTokenMu.RUnlock()
	if !exists {
		return false
	}
	if time.Now().After(exp) {
		adminTokenMu.Lock()
		delete(adminTokens, token)
		adminTokenMu.Unlock()
		return false
	}
	return true
}

func checkAdminAuth(r *http.Request) bool {
	cookie, err := r.Cookie("rmte_admin_token")
	if err != nil {
		return false
	}
	return isValidAdminToken(cookie.Value)
}

// setupAdminHandler mounts the admin dashboard and JSON endpoints.
func setupAdminHandler(mux *http.ServeMux, adminPath, adminPass string) {
	if adminPass == "" {
		return
	}

	apiPrefix := adminPath + "/api"

	// Login endpoint
	mux.HandleFunc(apiPrefix+"/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if subtle.ConstantTimeCompare([]byte(req.Password), []byte(adminPass)) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "Invalid admin password"})
			return
		}

		token := generateAdminToken()
		adminTokenMu.Lock()
		adminTokens[token] = time.Now().Add(24 * time.Hour)
		adminTokenMu.Unlock()

		http.SetCookie(w, &http.Cookie{
			Name:     "rmte_admin_token",
			Value:    token,
			Path:     adminPath,
			Expires:  time.Now().Add(24 * time.Hour),
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
	})

	// Logout endpoint
	mux.HandleFunc(apiPrefix+"/logout", func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("rmte_admin_token")
		if err == nil {
			adminTokenMu.Lock()
			delete(adminTokens, cookie.Value)
			adminTokenMu.Unlock()
		}
		http.SetCookie(w, &http.Cookie{
			Name:     "rmte_admin_token",
			Value:    "",
			Path:     adminPath,
			MaxAge:   -1,
			HttpOnly: true,
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
	})

	// Stats endpoint
	mux.HandleFunc(apiPrefix+"/stats", func(w http.ResponseWriter, r *http.Request) {
		if !checkAdminAuth(r) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		sessionMu.RLock()
		activeDTOs := make([]ActiveSessionDTO, 0, len(sessions))
		totalViewers := 0

		for _, s := range sessions {
			s.Mutex.RLock()
			viewersCount := 0
			for _, conns := range s.Viewers {
				viewersCount += len(conns)
			}
			totalViewers += viewersCount

			uptimeStr := time.Since(s.CreatedAt).Round(time.Second).String()
			activeDTOs = append(activeDTOs, ActiveSessionDTO{
				ID:             s.ID,
				HostIP:         s.HostIP,
				CreatedAt:      s.CreatedAt,
				Uptime:         uptimeStr,
				ViewersCount:   viewersCount,
				BytesRx:        atomic.LoadUint64(&s.BytesRx),
				BytesTx:        atomic.LoadUint64(&s.BytesTx),
				FileBytes:      atomic.LoadUint64(&s.FileBytes),
				PreviewEnabled: s.PreviewEnabled,
			})
			s.Mutex.RUnlock()
		}
		sessionMu.RUnlock()

		historyMu.RLock()
		histCopy := make([]SessionHistoryInfo, len(sessionHistory))
		copy(histCopy, sessionHistory)
		historyMu.RUnlock()

		resp := AdminStatsResponse{
			ServerVersion:  appVersion,
			RelayUptime:    time.Since(serverStartTime).Round(time.Second).String(),
			TotalSessions:  len(activeDTOs),
			TotalViewers:   totalViewers,
			ActiveSessions: activeDTOs,
			History:        histCopy,
			BannedIPs:      getBannedIPList(),
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// Terminate session endpoint
	mux.HandleFunc(apiPrefix+"/terminate", func(w http.ResponseWriter, r *http.Request) {
		if !checkAdminAuth(r) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		var req struct {
			SessionID string `json:"session_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		ok := terminateSession(req.SessionID)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": ok})
	})

	// Ban IP endpoint
	mux.HandleFunc(apiPrefix+"/ban", func(w http.ResponseWriter, r *http.Request) {
		if !checkAdminAuth(r) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		var req struct {
			SessionID string `json:"session_id"`
			IP        string `json:"ip"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		if req.IP != "" {
			banIP(req.IP, "Banned by administrator")
		}
		if req.SessionID != "" {
			terminateSession(req.SessionID)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
	})

	// Unban IP endpoint
	mux.HandleFunc(apiPrefix+"/unban", func(w http.ResponseWriter, r *http.Request) {
		if !checkAdminAuth(r) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		var req struct {
			IP string `json:"ip"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		unbanIP(req.IP)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
	})

	// Serve Admin Frontend UI
	adminUIHandler := func(w http.ResponseWriter, r *http.Request) {
		data, err := uiAssets.ReadFile("ui/admin.html")
		if err != nil {
			http.Error(w, "Admin UI template not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	}

	trimmedPath := strings.TrimSuffix(adminPath, "/")
	if !strings.HasSuffix(adminPath, "/") {
		mux.HandleFunc(adminPath, func(w http.ResponseWriter, r *http.Request) {
			target := trimmedPath + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusFound)
		})
	}

	mux.HandleFunc(trimmedPath+"/rmte.svg", func(w http.ResponseWriter, r *http.Request) {
		data, err := uiAssets.ReadFile("ui/rmte.svg")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(data)
	})

	mux.HandleFunc(trimmedPath+"/", adminUIHandler)
}
