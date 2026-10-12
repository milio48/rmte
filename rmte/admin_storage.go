package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"runtime"
	"sync"
	"time"
)

const (
	maxPersistedHistory = 100
	maxPersistedBans    = 1000
	maxStateFileSize    = 1024 * 1024 // 1 MB
)

// CumulativeStats holds lifetime metrics persisted across process reboots.
type CumulativeStats struct {
	TotalSessions  uint64 `json:"total_sessions"`
	TotalBytesRx   uint64 `json:"total_bytes_rx"`
	TotalBytesTx   uint64 `json:"total_bytes_tx"`
	TotalFileBytes uint64 `json:"total_file_bytes"`
}

// AdminPersistentState represents the root schema stored in rmte-<id>.json.
type AdminPersistentState struct {
	Version         int                  `json:"version"`
	UpdatedAt       time.Time            `json:"updated_at"`
	CumulativeStats CumulativeStats      `json:"cumulative"`
	BannedIPs       []BannedIPInfo       `json:"banned_ips"`
	SessionHistory  []SessionHistoryInfo `json:"session_history"`
}

var (
	adminStorageID string
	adminStorageMu sync.Mutex

	cumulative   CumulativeStats
	cumulativeMu sync.RWMutex
)

// initAdminStorage initializes the persistence engine for the instance.
func initAdminStorage(instanceID string) {
	if instanceID == "" {
		instanceID = "relay"
	}
	adminStorageMu.Lock()
	adminStorageID = instanceID
	adminStorageMu.Unlock()

	loadAdminState()
}

func adminStorageFilename() string {
	adminStorageMu.Lock()
	id := adminStorageID
	adminStorageMu.Unlock()
	if id == "" {
		id = "relay"
	}
	return fmt.Sprintf("rmte-%s.json", id)
}

// loadAdminState loads persistent dashboard state from disk into in-memory structures.
func loadAdminState() {
	filename := adminStorageFilename()
	fi, err := os.Stat(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		log.Printf("[AdminStorage] Warning: unable to stat %s: %v", filename, err)
		return
	}

	if fi.Size() > maxStateFileSize {
		log.Printf("[AdminStorage] Warning: state file %s exceeds 1MB limit (%d bytes), ignoring to prevent unbounded memory usage", filename, fi.Size())
		return
	}

	data, err := os.ReadFile(filename)
	if err != nil {
		log.Printf("[AdminStorage] Warning: failed to read %s: %v", filename, err)
		return
	}

	var state AdminPersistentState
	if err := json.Unmarshal(data, &state); err != nil {
		log.Printf("[AdminStorage] Corrupted state file %s (%v), moving to .bak and starting fresh", filename, err)
		_ = os.Rename(filename, filename+".bak")
		return
	}

	// 1. Populate Cumulative Stats
	cumulativeMu.Lock()
	cumulative = state.CumulativeStats
	cumulativeMu.Unlock()

	// 2. Populate Banned IPs (up to maxPersistedBans)
	bannedMu.Lock()
	count := 0
	for _, b := range state.BannedIPs {
		if count >= maxPersistedBans {
			break
		}
		if b.IP != "" {
			bannedIPs[b.IP] = b
			count++
		}
	}
	bannedMu.Unlock()

	// 3. Populate Session History (up to maxPersistedHistory)
	historyMu.Lock()
	if len(state.SessionHistory) > maxPersistedHistory {
		sessionHistory = state.SessionHistory[len(state.SessionHistory)-maxPersistedHistory:]
	} else {
		sessionHistory = state.SessionHistory
	}
	historyMu.Unlock()

	log.Printf("[AdminStorage] Loaded persistent state from %s (history: %d, banned: %d, lifetime sessions: %d)",
		filename, len(sessionHistory), len(bannedIPs), state.CumulativeStats.TotalSessions)
}

// saveAdminState writes a snapshot of history, bans, and cumulative stats to disk atomically.
// Must be called WITHOUT holding historyMu, bannedMu, sessionMu, or sess.Mutex to prevent deadlocks.
func saveAdminState() {
	filename := adminStorageFilename()

	// Take snapshots under individual read-locks
	cumulativeMu.RLock()
	snapCumulative := cumulative
	cumulativeMu.RUnlock()

	historyMu.RLock()
	snapHistory := make([]SessionHistoryInfo, len(sessionHistory))
	copy(snapHistory, sessionHistory)
	historyMu.RUnlock()

	bannedMu.RLock()
	snapBanned := make([]BannedIPInfo, 0, len(bannedIPs))
	for _, b := range bannedIPs {
		snapBanned = append(snapBanned, b)
		if len(snapBanned) >= maxPersistedBans {
			break
		}
	}
	bannedMu.RUnlock()

	// Ensure history does not exceed maximum
	if len(snapHistory) > maxPersistedHistory {
		snapHistory = snapHistory[len(snapHistory)-maxPersistedHistory:]
	}

	state := AdminPersistentState{
		Version:         1,
		UpdatedAt:       time.Now().UTC(),
		CumulativeStats: snapCumulative,
		BannedIPs:       snapBanned,
		SessionHistory:  snapHistory,
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		log.Printf("[AdminStorage] Error marshaling state: %v", err)
		return
	}

	adminStorageMu.Lock()
	defer adminStorageMu.Unlock()

	if err := atomicWriteJSON(filename, data); err != nil {
		log.Printf("[AdminStorage] Error writing state to %s: %v", filename, err)
	}
}

// atomicWriteJSON writes data to a temporary file, syncs to disk, and atomically renames it.
func atomicWriteJSON(filename string, data []byte) error {
	tmpFile := filename + ".tmp"

	// Create with 0600 permission (owner read/write only)
	f, err := os.OpenFile(tmpFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to open temp file: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to write data: %w", err)
	}

	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to sync data: %w", err)
	}

	if err := f.Close(); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to close file: %w", err)
	}

	// Atomic replace
	var renameErr error
	for attempt := 0; attempt < 3; attempt++ {
		renameErr = os.Rename(tmpFile, filename)
		if renameErr == nil {
			return nil
		}
		if runtime.GOOS == "windows" {
			// Windows may encounter transient sharing violations from antivirus or indexers
			time.Sleep(20 * time.Millisecond)
		} else {
			break
		}
	}

	_ = os.Remove(tmpFile)
	return fmt.Errorf("failed to rename %s to %s: %w", tmpFile, filename, renameErr)
}

// recordSessionClosedStats updates cumulative statistics when a session ends.
func recordSessionClosedStats(rx, tx, fileBytes uint64) {
	cumulativeMu.Lock()
	cumulative.TotalSessions++
	cumulative.TotalBytesRx += rx
	cumulative.TotalBytesTx += tx
	cumulative.TotalFileBytes += fileBytes
	cumulativeMu.Unlock()
}

// getCumulativeStats returns a snapshot of cumulative metrics.
func getCumulativeStats() CumulativeStats {
	cumulativeMu.RLock()
	defer cumulativeMu.RUnlock()
	return cumulative
}
