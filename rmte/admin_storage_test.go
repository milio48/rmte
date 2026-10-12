package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func cleanupTestStateFiles(id string) {
	_ = os.Remove(fmt.Sprintf("rmte-%s.json", id))
	_ = os.Remove(fmt.Sprintf("rmte-%s.json.tmp", id))
	_ = os.Remove(fmt.Sprintf("rmte-%s.json.bak", id))
}

func TestAdminStorageRoundtrip(t *testing.T) {
	testID := "testunit1"
	cleanupTestStateFiles(testID)
	defer cleanupTestStateFiles(testID)

	initAdminStorage(testID)

	// Mutate in-memory state
	banIP("192.168.1.100", "Testing ban")
	recordSessionClosedStats(5000, 15000, 2000)

	historyMu.Lock()
	sessionHistory = append(sessionHistory, SessionHistoryInfo{
		SessionID: "s-unit-1",
		HostIP:    "10.0.0.1",
		CreatedAt: time.Now().Add(-10 * time.Minute),
		ClosedAt:  time.Now(),
		Duration:  "10m",
		BytesRx:   5000,
		BytesTx:   15000,
		FileBytes: 2000,
		Reason:    "Test close",
	})
	historyMu.Unlock()

	saveAdminState()

	// Verify file was written
	filename := fmt.Sprintf("rmte-%s.json", testID)
	fi, err := os.Stat(filename)
	if err != nil {
		t.Fatalf("expected state file to exist: %v", err)
	}
	if fi.Size() == 0 {
		t.Fatalf("state file is empty")
	}

	// Wipe memory
	bannedMu.Lock()
	bannedIPs = make(map[string]BannedIPInfo)
	bannedMu.Unlock()

	historyMu.Lock()
	sessionHistory = nil
	historyMu.Unlock()

	cumulativeMu.Lock()
	cumulative = CumulativeStats{}
	cumulativeMu.Unlock()

	// Reload from disk
	loadAdminState()

	// Verify restored state
	if !isIPBanned("192.168.1.100") {
		t.Errorf("expected IP 192.168.1.100 to be banned after reload")
	}

	historyMu.RLock()
	if len(sessionHistory) != 1 || sessionHistory[0].SessionID != "s-unit-1" {
		t.Errorf("expected 1 session history with ID s-unit-1, got %v", sessionHistory)
	}
	historyMu.RUnlock()

	cum := getCumulativeStats()
	if cum.TotalSessions != 1 || cum.TotalBytesRx != 5000 || cum.TotalBytesTx != 15000 {
		t.Errorf("unexpected cumulative stats: %+v", cum)
	}
}

func TestAdminStorageMaxCaps(t *testing.T) {
	testID := "testcaps"
	cleanupTestStateFiles(testID)
	defer cleanupTestStateFiles(testID)

	initAdminStorage(testID)

	// Add 120 history items
	historyMu.Lock()
	sessionHistory = make([]SessionHistoryInfo, 0, 150)
	for i := 0; i < 120; i++ {
		sessionHistory = append(sessionHistory, SessionHistoryInfo{
			SessionID: fmt.Sprintf("s-%d", i),
			Reason:    "cap test",
		})
	}
	historyMu.Unlock()

	saveAdminState()

	// Wipe & reload
	historyMu.Lock()
	sessionHistory = nil
	historyMu.Unlock()

	loadAdminState()

	historyMu.RLock()
	if len(sessionHistory) != maxPersistedHistory {
		t.Errorf("expected capped history %d, got %d", maxPersistedHistory, len(sessionHistory))
	}
	// Verify FIFO: oldest were discarded, last is s-119
	if sessionHistory[len(sessionHistory)-1].SessionID != "s-119" {
		t.Errorf("expected latest history to be s-119, got %s", sessionHistory[len(sessionHistory)-1].SessionID)
	}
	historyMu.RUnlock()
}

func TestAdminStorageCorruptedFallback(t *testing.T) {
	testID := "testcorrupt"
	cleanupTestStateFiles(testID)
	defer cleanupTestStateFiles(testID)

	filename := fmt.Sprintf("rmte-%s.json", testID)
	_ = os.WriteFile(filename, []byte("{invalid-json-content: corrupt}"), 0600)

	initAdminStorage(testID)

	// Verify .bak file was created
	bakFile := filename + ".bak"
	if _, err := os.Stat(bakFile); err != nil {
		t.Errorf("expected backup file %s to be created on corruption: %v", bakFile, err)
	}
	// Verify original corrupt file was moved
	if _, err := os.Stat(filename); !os.IsNotExist(err) {
		t.Errorf("expected original corrupt file %s to be moved, but it still exists", filename)
	}
}

func TestAdminStorageFileExceedsLimit(t *testing.T) {
	testID := "testtoolarge"
	cleanupTestStateFiles(testID)
	defer cleanupTestStateFiles(testID)

	filename := fmt.Sprintf("rmte-%s.json", testID)
	// Create file slightly larger than 1MB
	bigData := make([]byte, maxStateFileSize+100)
	_ = os.WriteFile(filename, bigData, 0600)

	historyMu.Lock()
	sessionHistory = nil
	historyMu.Unlock()

	initAdminStorage(testID)

	historyMu.RLock()
	if len(sessionHistory) != 0 {
		t.Errorf("expected 0 history loaded from oversized file, got %d", len(sessionHistory))
	}
	historyMu.RUnlock()
}

func resetInMemoryAdminState() {
	bannedMu.Lock()
	bannedIPs = make(map[string]BannedIPInfo)
	bannedMu.Unlock()

	historyMu.Lock()
	sessionHistory = nil
	historyMu.Unlock()

	cumulativeMu.Lock()
	cumulative = CumulativeStats{}
	cumulativeMu.Unlock()
}

func TestAdminStorageConcurrentSaves(t *testing.T) {
	testID := "testrace"
	cleanupTestStateFiles(testID)
	defer cleanupTestStateFiles(testID)

	resetInMemoryAdminState()
	initAdminStorage(testID)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			banIP(fmt.Sprintf("10.0.0.%d", n), "Concurrent ban")
			recordSessionClosedStats(100, 200, 10)
			saveAdminState()
		}(i)
	}
	wg.Wait()

	// Verify reload does not panic and contains data
	loadAdminState()
	bannedMu.RLock()
	if len(bannedIPs) != 20 {
		t.Errorf("expected 20 banned IPs, got %d", len(bannedIPs))
	}
	bannedMu.RUnlock()
}

func TestInternalRmteFileFilter(t *testing.T) {
	testFiles := []struct {
		name     string
		expected bool
	}{
		{"rmte-relay.meta", true},
		{"rmte-relay.pid", true},
		{"rmte-relay.log", true},
		{"rmte-relay.json", true},
		{"rmte-rmtepusat.json", true},
		{"rmte-rmtepusat.json.tmp", true},
		{"rmte-rmtepusat.json.bak", true},
		{"RMTE-RELAY.JSON", true}, // case-folding test
		{"notes.json", false},
		{"my-rmte-notes.json", false},
		{"package.json", false},
	}

	for _, tc := range testFiles {
		got := isInternalRmteFile(tc.name)
		if got != tc.expected {
			t.Errorf("isInternalRmteFile(%q) = %v; want %v", tc.name, got, tc.expected)
		}
		// Also test with full path
		gotPath := isInternalRmteFile(filepath.Join("/home/container", tc.name))
		if gotPath != tc.expected {
			t.Errorf("isInternalRmteFile(path/%q) = %v; want %v", tc.name, gotPath, tc.expected)
		}
	}
}
