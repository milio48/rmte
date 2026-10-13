package main

import "testing"

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

