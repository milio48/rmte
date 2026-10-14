package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestStripANSI(t *testing.T) {
	input := []byte("\x1b[32mHello\x1b[0m \x1b[1;34mWorld\x1b[0m\r\n\x1b]0;Title\x07Line 2")
	expected := "Hello World\nLine 2"
	got := stripANSI(input)
	if got != expected {
		t.Fatalf("stripANSI failed: got %q, want %q", got, expected)
	}
}

func TestHandleAIExecSecurityGuardDisabled(t *testing.T) {
	setHostOptions(HostOptions{
		AllowExec: false,
	})

	// Create test websocket server and client pair
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer raw.Close()

		conn := &SafeConn{Conn: raw}
		ctrl := map[string]interface{}{
			"action":      "ai_exec",
			"target_conn": "c-viewer-123",
			"command":     "echo hello",
		}
		handleAIExec(ctrl, conn)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	clientRaw, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer clientRaw.Close()

	var resp map[string]interface{}
	err = clientRaw.ReadJSON(&resp)
	if err != nil {
		t.Fatalf("read response failed: %v", err)
	}

	if resp["action"] != "ai_exec_result" {
		t.Errorf("expected action ai_exec_result, got %v", resp["action"])
	}
	if resp["target_conn"] != "c-viewer-123" {
		t.Errorf("expected target_conn c-viewer-123, got %v", resp["target_conn"])
	}
	errMsg, _ := resp["error"].(string)
	if !strings.Contains(errMsg, "--allow-exec") {
		t.Errorf("expected error mentioning --allow-exec, got %q", errMsg)
	}
	exitCode, _ := resp["exit_code"].(float64)
	if exitCode != 1 {
		t.Errorf("expected exit_code 1, got %v", exitCode)
	}
}

func TestHandleAIExecWindowsRejectionNotice(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("skipping Windows rejection test on non-windows platform")
	}

	setHostOptions(HostOptions{
		AllowExec: true,
	})

	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer raw.Close()

		conn := &SafeConn{Conn: raw}
		ctrl := map[string]interface{}{
			"action":      "ai_exec",
			"target_conn": "c-viewer-456",
			"command":     "dir",
		}
		handleAIExec(ctrl, conn)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	clientRaw, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer clientRaw.Close()

	var resp map[string]interface{}
	err = clientRaw.ReadJSON(&resp)
	if err != nil {
		t.Fatalf("read response failed: %v", err)
	}

	errMsg, _ := resp["error"].(string)
	if !strings.Contains(errMsg, "Linux/POSIX host") {
		t.Errorf("expected error mentioning Linux/POSIX host requirement, got %q", errMsg)
	}
}

func TestHandleAIGetInfo(t *testing.T) {
	setHostOptions(HostOptions{
		AllowExec: true,
		Preview:   true,
	})

	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer raw.Close()

		conn := &SafeConn{Conn: raw}
		handleAIGetInfo("c-target-789", conn)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	clientRaw, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer clientRaw.Close()

	var resp map[string]interface{}
	err = clientRaw.ReadJSON(&resp)
	if err != nil {
		t.Fatalf("read response failed: %v", err)
	}

	if resp["action"] != "ai_get_info_result" {
		t.Errorf("expected action ai_get_info_result, got %v", resp["action"])
	}
	if resp["target_conn"] != "c-target-789" {
		t.Errorf("expected target_conn c-target-789, got %v", resp["target_conn"])
	}

	info, _ := resp["info"].(map[string]interface{})
	if info == nil {
		t.Fatalf("expected info object, got nil")
	}

	if info["os"] != runtime.GOOS {
		t.Errorf("expected os %s, got %v", runtime.GOOS, info["os"])
	}
	if info["allow_exec"] != true {
		t.Errorf("expected allow_exec true, got %v", info["allow_exec"])
	}
	if info["preview"] != true {
		t.Errorf("expected preview true, got %v", info["preview"])
	}
}

func TestApplyPresenceTransitionWithBot(t *testing.T) {
	pMap := make(map[string]ViewersPresence)

	// 1. Regular user connects
	tabs, bots, _, _, _ := applyPresenceTransitionWithBot(pMap, "unfocus", "v-alice", "Alice", lobbyTabID, false)
	if len(bots) != 0 {
		t.Fatalf("expected 0 bots, got: %v", bots)
	}
	if len(tabs["255"]) != 1 || tabs["255"][0] != "Alice" {
		t.Fatalf("expected Alice in lobby, got: %v", tabs["255"])
	}

	// 2. AI Bot connects
	tabs, bots, _, _, _ = applyPresenceTransitionWithBot(pMap, "unfocus", "v-bot", "Claude-MCP", lobbyTabID, true)
	if len(bots) != 1 || bots[0] != "Claude-MCP" {
		t.Fatalf("expected Claude-MCP in bots list, got: %v", bots)
	}
	if len(tabs["255"]) != 2 {
		t.Fatalf("expected 2 users in lobby, got: %v", tabs["255"])
	}

	// 3. Alice focuses Tab 0 (regular)
	tabs, bots, _, _, _ = applyPresenceTransitionWithBot(pMap, "set_focus", "v-alice", "Alice", 0, false)
	if len(bots) != 1 || bots[0] != "Claude-MCP" {
		t.Fatalf("expected Claude-MCP still in bots list, got: %v", bots)
	}
	if len(tabs["0"]) != 1 || tabs["0"][0] != "Alice" {
		t.Fatalf("expected Alice on Tab 0, got: %v", tabs["0"])
	}

	// 4. Bot unfocus with is_bot omitted (should retain bot status from prev)
	tabs, bots, _, _, _ = applyPresenceTransitionWithBot(pMap, "unfocus", "v-bot", "Claude-MCP", lobbyTabID, false)
	if len(bots) != 1 || bots[0] != "Claude-MCP" {
		t.Fatalf("expected Claude-MCP still in bots list after unfocus, got: %v", bots)
	}

	// 5. Bot disconnects
	tabs, bots, evt, user, shouldLog := applyPresenceTransitionWithBot(pMap, "viewer_disconnected", "v-bot", "", 0, false)
	if !shouldLog || evt != "DISCONNECT" || user != "Claude-MCP" {
		t.Fatalf("expected disconnect event for bot, got evt=%q, user=%q", evt, user)
	}
	if len(bots) != 0 {
		t.Fatalf("expected 0 bots after disconnect, got: %v", bots)
	}
}

// Suppress unused warning if json package is imported
var _ = json.Marshal
