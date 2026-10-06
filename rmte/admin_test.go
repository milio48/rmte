package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetClientIP(t *testing.T) {
	tests := []struct {
		name     string
		headers  map[string]string
		remote   string
		expected string
	}{
		{
			name: "Cloudflare CF-Connecting-IP priority",
			headers: map[string]string{
				"CF-Connecting-IP": "103.45.67.89",
				"X-Forwarded-For":  "180.252.1.2",
				"X-Real-IP":        "192.168.1.10",
			},
			remote:   "172.68.1.1:45123",
			expected: "103.45.67.89",
		},
		{
			name: "X-Forwarded-For first IP fallback",
			headers: map[string]string{
				"X-Forwarded-For": "180.252.1.2, 172.68.1.1",
				"X-Real-IP":       "192.168.1.10",
			},
			remote:   "10.0.0.1:54321",
			expected: "180.252.1.2",
		},
		{
			name: "X-Real-IP fallback",
			headers: map[string]string{
				"X-Real-IP": "192.168.1.10",
			},
			remote:   "127.0.0.1:8080",
			expected: "192.168.1.10",
		},
		{
			name:     "RemoteAddr direct socket fallback",
			headers:  map[string]string{},
			remote:   "203.0.113.195:34567",
			expected: "203.0.113.195",
		},
		{
			name: "Malicious non-IP header ignored",
			headers: map[string]string{
				"CF-Connecting-IP": "');alert(1)//",
				"X-Forwarded-For":  "<script>evil()</script>",
				"X-Real-IP":        "bad-ip-string",
			},
			remote:   "203.0.113.195:34567",
			expected: "203.0.113.195",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = tc.remote
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			got := getClientIP(req)
			if got != tc.expected {
				t.Errorf("getClientIP() = %q; want %q", got, tc.expected)
			}
		})
	}
}

func TestBannedIPRegistry(t *testing.T) {
	testIP := "203.0.113.50"

	if isIPBanned(testIP) {
		t.Fatalf("IP %s should not be banned initially", testIP)
	}

	banIP(testIP, "Abusive bandwidth usage")
	if !isIPBanned(testIP) {
		t.Fatalf("IP %s should be banned after banIP()", testIP)
	}

	list := getBannedIPList()
	found := false
	for _, item := range list {
		if item.IP == testIP {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Banned IP list does not contain %s", testIP)
	}

	unbanIP(testIP)
	if isIPBanned(testIP) {
		t.Fatalf("IP %s should not be banned after unbanIP()", testIP)
	}
}

func TestAdminAPIEndToEnd(t *testing.T) {
	mux := http.NewServeMux()
	adminPass := "super-relay-secret"
	setupAdminHandler(mux, "/admin-rmte", adminPass)

	// 1. Test unauthorized request
	reqStats := httptest.NewRequest("GET", "/admin-rmte/api/stats", nil)
	rrStats := httptest.NewRecorder()
	mux.ServeHTTP(rrStats, reqStats)
	if rrStats.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 Unauthorized, got %d", rrStats.Code)
	}

	// 2. Test failed login
	badLoginPayload, _ := json.Marshal(map[string]string{"password": "wrong"})
	reqBadLogin := httptest.NewRequest("POST", "/admin-rmte/api/login", bytes.NewReader(badLoginPayload))
	rrBadLogin := httptest.NewRecorder()
	mux.ServeHTTP(rrBadLogin, reqBadLogin)
	if rrBadLogin.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 on bad password, got %d", rrBadLogin.Code)
	}

	// 3. Test successful login & cookie reception
	goodLoginPayload, _ := json.Marshal(map[string]string{"password": adminPass})
	reqGoodLogin := httptest.NewRequest("POST", "/admin-rmte/api/login", bytes.NewReader(goodLoginPayload))
	rrGoodLogin := httptest.NewRecorder()
	mux.ServeHTTP(rrGoodLogin, reqGoodLogin)
	if rrGoodLogin.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK on login, got %d", rrGoodLogin.Code)
	}

	cookie := rrGoodLogin.Result().Cookies()
	if len(cookie) == 0 || cookie[0].Name != "rmte_admin_token" {
		t.Fatalf("Expected rmte_admin_token cookie, got none")
	}

	// 4. Test authorized stats request with cookie
	reqStatsAuth := httptest.NewRequest("GET", "/admin-rmte/api/stats", nil)
	reqStatsAuth.AddCookie(cookie[0])
	rrStatsAuth := httptest.NewRecorder()
	mux.ServeHTTP(rrStatsAuth, reqStatsAuth)
	if rrStatsAuth.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK on stats with cookie, got %d", rrStatsAuth.Code)
	}

	var statsResp AdminStatsResponse
	if err := json.NewDecoder(rrStatsAuth.Body).Decode(&statsResp); err != nil {
		t.Fatalf("Failed to parse stats JSON: %v", err)
	}
}
