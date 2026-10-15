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

func TestHandleAIExecWindowsSuccess(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("skipping Windows execution test on non-windows platform")
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
			"command":     "Write-Output \"RMTE_WIN_SUCCESS_TEST\"",
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
	for {
		err = clientRaw.ReadJSON(&resp)
		if err != nil {
			t.Fatalf("read response failed: %v", err)
		}
		if resp["action"] == "ai_exec_result" {
			break
		}
	}

	stdout, _ := resp["stdout"].(string)
	if !strings.Contains(stdout, "RMTE_WIN_SUCCESS_TEST") {
		t.Errorf("expected stdout containing RMTE_WIN_SUCCESS_TEST, got %q", stdout)
	}
	exitCode, _ := resp["exit_code"].(float64)
	if exitCode != 0 {
		t.Errorf("expected exit_code 0, got %v", exitCode)
	}
}

func TestHandleAIExecWindowsExitCode(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("skipping Windows execution test on non-windows platform")
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
			"target_conn": "c-viewer-789",
			"command":     "exit 7",
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
	for {
		err = clientRaw.ReadJSON(&resp)
		if err != nil {
			t.Fatalf("read response failed: %v", err)
		}
		if resp["action"] == "ai_exec_result" {
			break
		}
	}

	exitCode, _ := resp["exit_code"].(float64)
	if exitCode != 7 {
		t.Errorf("expected exit_code 7, got %v (resp: %+v)", exitCode, resp)
	}
}

func TestHandleAIExecWindowsUTF8(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("skipping Windows execution test on non-windows platform")
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
			"target_conn": "c-viewer-utf8",
			"command":     "Write-Output \"Halo Dunia! 🚀 🇮🇩 café déjà vu\"",
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
	for {
		err = clientRaw.ReadJSON(&resp)
		if err != nil {
			t.Fatalf("read response failed: %v", err)
		}
		if resp["action"] == "ai_exec_result" {
			break
		}
	}

	stdout, _ := resp["stdout"].(string)
	if !strings.Contains(stdout, "Halo Dunia! 🚀 🇮🇩 café déjà vu") {
		t.Errorf("expected stdout containing UTF-8 string, got %q", stdout)
	}
	exitCode, _ := resp["exit_code"].(float64)
	if exitCode != 0 {
		t.Errorf("expected exit_code 0, got %v", exitCode)
	}
}

func TestHandleAIExecWindowsTimeout(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("skipping Windows execution test on non-windows platform")
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
			"action":          "ai_exec",
			"target_conn":     "c-viewer-timeout",
			"command":         "Start-Sleep -Seconds 10",
			"timeout_seconds": float64(1),
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
	for {
		err = clientRaw.ReadJSON(&resp)
		if err != nil {
			t.Fatalf("read response failed: %v", err)
		}
		if resp["action"] == "ai_exec_result" {
			break
		}
	}

	exitCode, _ := resp["exit_code"].(float64)
	if exitCode != 124 {
		t.Errorf("expected exit_code 124 for timeout, got %v (resp: %+v)", exitCode, resp)
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

func TestBotClientDeadlockFreeEndToEnd(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer raw.Close()

		// 1. Read viewer auth
		var authMsg map[string]interface{}
		if err := raw.ReadJSON(&authMsg); err != nil {
			return
		}
		if authMsg["type"] != "auth" || authMsg["role"] != "viewer" {
			return
		}

		// 2. Reply auth success
		_ = raw.WriteJSON(map[string]interface{}{
			"type":       "auth_success",
			"session_id": "test-session-dl",
		})

		// 3. Service client requests
		for {
			var msg map[string]interface{}
			if err := raw.ReadJSON(&msg); err != nil {
				return
			}
			action, _ := msg["action"].(string)
			switch action {
			case "unfocus":
				// bot initial presence announce
			case "ai_exec":
				cmd, _ := msg["command"].(string)
				_ = raw.WriteJSON(map[string]interface{}{
					"type":        "control",
					"action":      "ai_exec_result",
					"target_conn": "test-bot",
					"stdout":      "output-of: " + cmd,
					"exit_code":   0,
				})
			case "ai_get_info":
				_ = raw.WriteJSON(map[string]interface{}{
					"type":        "control",
					"action":      "ai_get_info_result",
					"target_conn": "test-bot",
					"info": map[string]interface{}{
						"os":         "linux",
						"arch":       "amd64",
						"allow_exec": true,
					},
				})
			case "req_dir":
				_ = raw.WriteJSON(map[string]interface{}{
					"type":        "control",
					"action":      "dir_data",
					"target_conn": "test-bot",
					"path":        "/",
					"files": []interface{}{
						map[string]interface{}{"name": "main.go", "is_dir": false, "size": 1234},
						map[string]interface{}{"name": "src", "is_dir": true, "size": 0},
					},
				})
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	testPass := "testpass12345"

	bot, err := newBotClient(wsURL, "test-session-dl", testPass, "test-bot", false)
	if err != nil {
		t.Fatalf("newBotClient failed: %v", err)
	}
	defer bot.Close()

	// 1. Verify ExecCommand completes without deadlock
	stdout, exitCode, err := bot.ExecCommand("uname -a", 5, false, "")
	if err != nil {
		t.Fatalf("ExecCommand returned error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("ExecCommand expected exitCode 0, got %d", exitCode)
	}
	if stdout != "output-of: uname -a" {
		t.Fatalf("ExecCommand expected %q, got %q", "output-of: uname -a", stdout)
	}

	// 2. Verify GetInfo completes without deadlock
	info, err := bot.GetInfo()
	if err != nil {
		t.Fatalf("GetInfo returned error: %v", err)
	}
	if info["os"] != "linux" || info["allow_exec"] != true {
		t.Fatalf("GetInfo unexpected payload: %+v", info)
	}

	// 3. Verify ListFiles completes without deadlock
	entries, err := bot.ListFiles("/")
	if err != nil {
		t.Fatalf("ListFiles returned error: %v", err)
	}
	if len(entries) != 2 || entries[0].Name != "main.go" || entries[1].Name != "src" {
		t.Fatalf("ListFiles unexpected entries: %+v", entries)
	}
}

var _ = json.Marshal
