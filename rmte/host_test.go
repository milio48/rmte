package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIsInternalRmteFile(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"rmte-1234.meta", true},
		{"rmte-abcd.pid", true},
		{"rmte-sess99.log", true},
		{"RMTE-1234.META", true},
		{"Rmte-abcd.PID", true},
		{"RMTE-sess99.LOG", true},
		{"/path/to/rmte-test.meta", true},
		{"/path/to/RMTE-test.Meta", true},
		{"C:\\workspace\\rmte-test.pid", true},
		{"C:\\workspace\\RMTE-TEST.PID", true},
		{"main.go", false},
		{"rmte.exe", false},
		{"rmte_notes.txt", false},
		{"rmte-other.txt", false},
		{"notes-rmte.meta", false},
	}

	for _, tc := range tests {
		got := isInternalRmteFile(tc.input)
		if got != tc.expected {
			t.Errorf("isInternalRmteFile(%q) = %v; want %v", tc.input, got, tc.expected)
		}
	}
}

func TestApplyPresenceTransition(t *testing.T) {
	pMap := make(map[string]ViewersPresence)

	// 1. Initial connect via unfocus (Lobby / 255)
	tabs, evt, user, shouldLog := applyPresenceTransition(pMap, "unfocus", "v1", "Alice", lobbyTabID)
	if !shouldLog || evt != "CONNECT" || user != "Alice" {
		t.Fatalf("expected initial unfocus to log CONNECT for Alice, got evt=%q, user=%q, log=%v", evt, user, shouldLog)
	}
	if len(tabs["255"]) != 1 || tabs["255"][0] != "Alice" {
		t.Fatalf("expected Alice in Lobby (255), got: %+v", tabs)
	}

	// 2. Alice switches to Tab 0 (set_focus)
	tabs, evt, user, shouldLog = applyPresenceTransition(pMap, "set_focus", "v1", "Alice", 0)
	if shouldLog {
		t.Fatalf("expected no connect log for already-connected Alice, got: %v", shouldLog)
	}
	if len(tabs["0"]) != 1 || tabs["0"][0] != "Alice" {
		t.Fatalf("expected Alice on Tab 0, got: %+v", tabs)
	}
	if len(tabs["255"]) != 0 {
		t.Fatalf("expected Alice removed from Lobby, got: %+v", tabs["255"])
	}

	// 3. Second viewer connects directly via unfocus with empty display name (fallback to viewerID)
	tabs, evt, user, shouldLog = applyPresenceTransition(pMap, "unfocus", "v2", "", lobbyTabID)
	if !shouldLog || evt != "CONNECT" || user != "v2" {
		t.Fatalf("expected connect log for v2 fallback name, got evt=%q, user=%q", evt, user)
	}
	if len(tabs["255"]) != 1 || tabs["255"][0] != "v2" {
		t.Fatalf("expected v2 in Lobby (255), got: %+v", tabs["255"])
	}
	if len(tabs["0"]) != 1 || tabs["0"][0] != "Alice" {
		t.Fatalf("expected Alice still on Tab 0, got: %+v", tabs["0"])
	}

	// 4. Alice exits back to menu (unfocus -> Lobby 255)
	tabs, _, _, shouldLog = applyPresenceTransition(pMap, "unfocus", "v1", "Alice", lobbyTabID)
	if shouldLog {
		t.Fatalf("expected no connect log when Alice exits to lobby, got: %v", shouldLog)
	}
	if len(tabs["255"]) != 2 {
		t.Fatalf("expected both Alice and v2 in Lobby (255), got: %+v", tabs["255"])
	}
	if len(tabs["0"]) != 0 {
		t.Fatalf("expected Tab 0 empty, got: %+v", tabs["0"])
	}

	// 5. Alice disconnects
	tabs, evt, user, shouldLog = applyPresenceTransition(pMap, "viewer_disconnected", "v1", "", 0)
	if !shouldLog || evt != "DISCONNECT" || user != "Alice" {
		t.Fatalf("expected disconnect log for Alice, got evt=%q, user=%q, log=%v", evt, user, shouldLog)
	}
	if len(tabs["255"]) != 1 || tabs["255"][0] != "v2" {
		t.Fatalf("expected only v2 in Lobby, got: %+v", tabs["255"])
	}
}

func TestIsSubPath(t *testing.T) {
	base := filepath.Join(os.TempDir(), "rmte-test-base")

	// Standard subpaths
	ok, err := isSubPath(base, filepath.Join(base, "sub", "file.txt"))
	if err != nil || !ok {
		t.Errorf("expected subpath to be allowed: %v, err=%v", ok, err)
	}

	// Exact base path
	ok, err = isSubPath(base, base)
	if err != nil || !ok {
		t.Errorf("expected base path to be allowed: %v, err=%v", ok, err)
	}

	// Traversal to parent
	ok, _ = isSubPath(base, filepath.Join(base, ".."))
	if ok {
		t.Errorf("expected .. to be denied")
	}

	// Traversal escaping base
	ok, _ = isSubPath(base, filepath.Join(base, "..", "other"))
	if ok {
		t.Errorf("expected ../other to be denied")
	}

	// File starting with .. (e.g. ..custom_config) inside base
	ok, err = isSubPath(base, filepath.Join(base, "..custom_config"))
	if err != nil || !ok {
		t.Errorf("expected ..custom_config to be allowed: %v, err=%v", ok, err)
	}

	// Windows specific: different drive letter fail-deny
	if runtime.GOOS == "windows" {
		ok, err = isSubPath(`C:\workspace`, `D:\other\file.txt`)
		if ok || err == nil {
			t.Errorf("expected cross-drive to return error and false, got ok=%v, err=%v", ok, err)
		}

		// Windows case-folding
		ok, err = isSubPath(`C:\Workspace\Root`, `c:\workspace\root\sub\file.txt`)
		if err != nil || !ok {
			t.Errorf("expected Windows case-folding to be allowed: %v, err=%v", ok, err)
		}
	}
}

func TestHostSandboxEdgeCases(t *testing.T) {
	tempDir := t.TempDir()
	if c, err := filepath.EvalSymlinks(tempDir); err == nil {
		tempDir = c
	}

	oldHostWorkDir := hostWorkDir
	oldSandbox := hostSandbox
	defer func() {
		hostWorkDir = oldHostWorkDir
		hostSandbox = oldSandbox
	}()

	hostWorkDir = tempDir
	hostSandbox = true

	// 1. Valid existing and non-existing file inside sandbox
	subDir := filepath.Join(tempDir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	p1, err := sanitizePath("subdir/newfile.txt")
	if err != nil {
		t.Errorf("expected allowed for subdir/newfile.txt, got err: %v", err)
	}
	expectedP1 := filepath.Join(tempDir, "subdir", "newfile.txt")
	if strings.ToLower(filepath.Clean(p1)) != strings.ToLower(filepath.Clean(expectedP1)) {
		t.Errorf("expected %s, got %s", expectedP1, p1)
	}

	// 2. File starting with .. in name (e.g. ..custom_config)
	p2, err := sanitizePath("..custom_config")
	if err != nil {
		t.Errorf("expected allowed for ..custom_config, got err: %v", err)
	}
	expectedP2 := filepath.Join(tempDir, "..custom_config")
	if strings.ToLower(filepath.Clean(p2)) != strings.ToLower(filepath.Clean(expectedP2)) {
		t.Errorf("expected %s, got %s", expectedP2, p2)
	}

	// 3. Traversal attempt ..
	_, err = sanitizePath("..")
	if err == nil || !strings.Contains(err.Error(), "escapes sandbox root") {
		t.Errorf("expected access denied for '..', got %v", err)
	}

	// 4. Traversal attempt ../escape.txt
	_, err = sanitizePath("../escape.txt")
	if err == nil || !strings.Contains(err.Error(), "escapes sandbox root") {
		t.Errorf("expected access denied for '../escape.txt', got %v", err)
	}

	// 5. Nested traversal attempt
	_, err = sanitizePath("subdir/../../escape.txt")
	if err == nil || !strings.Contains(err.Error(), "escapes sandbox root") {
		t.Errorf("expected access denied for 'subdir/../../escape.txt', got %v", err)
	}

	// 6. Cross-boundary check for rename
	// oldPath inside, newPath outside: sanitizePath(oldPath) OK, sanitizePath(newPath) DENIED
	_, errOld := sanitizePath("old_name.txt")
	_, errNew := sanitizePath("../new_name.txt")
	if errOld != nil {
		t.Errorf("expected old inside path to succeed, got %v", errOld)
	}
	if errNew == nil {
		t.Errorf("expected new outside path to fail in sandbox")
	}

	// 7. Symlink escape check
	outsideDir := t.TempDir()
	if c, err := filepath.EvalSymlinks(outsideDir); err == nil {
		outsideDir = c
	}
	secretFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("secret"), 0644); err != nil {
		t.Fatalf("failed to write secret file: %v", err)
	}

	symlinkPath := filepath.Join(tempDir, "link_to_outside")
	if err := os.Symlink(outsideDir, symlinkPath); err == nil {
		// Symlink creation succeeded
		_, err = sanitizePath("link_to_outside/secret.txt")
		if err == nil || !strings.Contains(err.Error(), "escapes sandbox root") {
			t.Errorf("expected symlink escape to be denied, got %v", err)
		}
	}
}

func TestHostChatHistoryMaxChat(t *testing.T) {
	// Reset hostChatHistory before test
	hostChatHistoryMu.Lock()
	hostChatHistory = nil
	hostChatHistoryMu.Unlock()

	// 1. With MaxChat = 3
	setHostOptions(HostOptions{
		MaxChat: 3,
	})

	for i := 1; i <= 5; i++ {
		appendHostChatHistory(map[string]interface{}{
			"type":    "control",
			"action":  "chat",
			"sender":  "Alice",
			"message": fmt.Sprintf("msg %d", i),
		})
	}

	chats := getHostChatHistory()
	if len(chats) != 3 {
		t.Fatalf("expected 3 chats retained, got %d", len(chats))
	}
	if chats[0]["message"] != "msg 3" || chats[1]["message"] != "msg 4" || chats[2]["message"] != "msg 5" {
		t.Errorf("unexpected chat messages order: %+v", chats)
	}

	// 2. With MaxChat = 0 (disabled/ephemeral mode)
	hostChatHistoryMu.Lock()
	hostChatHistory = nil
	hostChatHistoryMu.Unlock()

	setHostOptions(HostOptions{
		MaxChat: 0,
	})

	appendHostChatHistory(map[string]interface{}{
		"type":    "control",
		"action":  "chat",
		"sender":  "Bob",
		"message": "ephemeral msg",
	})

	chatsZero := getHostChatHistory()
	if len(chatsZero) != 0 {
		t.Errorf("expected 0 chats with MaxChat=0, got %d", len(chatsZero))
	}
}


