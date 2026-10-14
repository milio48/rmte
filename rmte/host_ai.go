package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"regexp"
	"runtime"
	"sync"
	"time"
)

var (
	aiTabMu     sync.Mutex
	aiExecMutex sync.Mutex
	aiTabID     byte = 0
	aiTabActive bool
	aiAnsiRegex = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\].*?(\x07|\x1b\\)|\x1b[()][A-Z0-9]|\r`)
)

func stripANSI(in []byte) string {
	return aiAnsiRegex.ReplaceAllString(string(in), "")
}

func ensureAITab(conn *SafeConn) (byte, *TabSession, error) {
	aiTabMu.Lock()
	if aiTabActive {
		tabsMu.RLock()
		tab := tabs[aiTabID]
		tabsMu.RUnlock()
		if tab != nil && tab.Cmd != nil && tab.Cmd.Process != nil && tab.Cmd.ProcessState == nil {
			id := aiTabID
			aiTabMu.Unlock()
			return id, tab, nil
		}
		// Shell process has exited or is dead, clean up old tab entry
		if tab != nil {
			tab.Close()
			tabsMu.Lock()
			delete(tabs, aiTabID)
			tabsMu.Unlock()
		}
		aiTabActive = false
	}

	tabsMu.Lock()
	newID := nextTabID
	nextTabID++
	tabsMu.Unlock()

	createTabWithTitle(newID, conn, "[AI]", true)

	tabsMu.RLock()
	tab := tabs[newID]
	tabsMu.RUnlock()
	if tab == nil {
		aiTabMu.Unlock()
		return 0, nil, fmt.Errorf("failed to create [AI] tab")
	}

	aiTabID = newID
	aiTabActive = true
	aiTabMu.Unlock()

	// Readiness probe: send __RMTE_READY__ and wait for it to be echoed/executed
	if tab.WriteCloser != nil {
		readyChan := make(chan []byte, 10)
		doneChan := make(chan struct{})

		tab.AIListenerMu.Lock()
		tab.AIListener = readyChan
		tab.AIDoneChan = doneChan
		tab.AIListenerMu.Unlock()

		_, _ = tab.WriteCloser.Write([]byte("printf \"__RMTE_READY__\\n\"\n"))
		readyTimeout := time.After(3 * time.Second)
		ready := false
		for !ready {
			select {
			case chunk := <-readyChan:
				if bytes.Contains(chunk, []byte("__RMTE_READY__")) {
					ready = true
				}
			case <-readyTimeout:
				ready = true // proceed without deadlock
			case <-tab.AIProcessDone:
				ready = true
			}
		}

		tab.AIListenerMu.Lock()
		tab.AIListener = nil
		tab.AIDoneChan = nil
		tab.AIListenerMu.Unlock()
		close(doneChan)
	}

	logEvent(conn, "AI_TAB_INIT", "host", fmt.Sprintf("Initialized [AI] Tab %d", newID))
	return newID, tab, nil
}

func executeAICommand(tab *TabSession, command string, timeoutSec int) (string, int, error) {
	if tab == nil || tab.WriteCloser == nil {
		return "", 1, fmt.Errorf("AI tab not available")
	}

	outChan := make(chan []byte, 1000)
	doneChan := make(chan struct{})

	tab.AIListenerMu.Lock()
	tab.AIListener = outChan
	tab.AIDoneChan = doneChan
	tab.AIListenerMu.Unlock()

	defer func() {
		tab.AIListenerMu.Lock()
		tab.AIListener = nil
		tab.AIDoneChan = nil
		tab.AIListenerMu.Unlock()
		close(doneChan)
	}()

	// Generate unique random nonce to avoid sentinel collision
	nonceBytes := make([]byte, 8)
	_, _ = rand.Read(nonceBytes)
	nonce := hex.EncodeToString(nonceBytes)

	startMarker := []byte(fmt.Sprintf("__RMTE_OUT_START_%s__", nonce))
	endPrefix := []byte(fmt.Sprintf("__RMTE_OUT_END_%s_", nonce))

	// POSIX wrapper:
	// printf "\n__RMTE_OUT_START_%s__\n" "<nonce>"
	// <command>
	// __rmte_ec=$?
	// printf "\n__RMTE_OUT_END_%s_%d__\n" "<nonce>" "$__rmte_ec"
	wrapper := fmt.Sprintf("printf \"\\n__RMTE_OUT_START_%%s__\\n\" \"%s\"\n%s\n__rmte_ec=$?\nprintf \"\\n__RMTE_OUT_END_%%s_%%d__\\n\" \"%s\" \"$__rmte_ec\"\n", nonce, command, nonce)

	if _, err := tab.WriteCloser.Write([]byte(wrapper)); err != nil {
		return "", 1, fmt.Errorf("failed to write to AI tab: %w", err)
	}

	if timeoutSec <= 0 {
		timeoutSec = 60
	}
	timeoutTimer := time.NewTimer(time.Duration(timeoutSec) * time.Second)
	defer timeoutTimer.Stop()

	var rawBuf bytes.Buffer
	started := false
	exitCode := 0
	finished := false

	for !finished {
		select {
		case chunk, ok := <-outChan:
			if !ok {
				finished = true
				break
			}
			rawBuf.Write(chunk)
			b := rawBuf.Bytes()

			if !started {
				if idx := bytes.Index(b, startMarker); idx >= 0 {
					started = true
					// Truncate before startMarker + length
					afterStart := b[idx+len(startMarker):]
					// Strip leading newline if present
					if len(afterStart) > 0 && afterStart[0] == '\r' {
						afterStart = afterStart[1:]
					}
					if len(afterStart) > 0 && afterStart[0] == '\n' {
						afterStart = afterStart[1:]
					}
					rawBuf.Reset()
					rawBuf.Write(afterStart)
					b = rawBuf.Bytes()
				}
			}

			if started {
				if endIdx := bytes.Index(b, endPrefix); endIdx >= 0 {
					// Output is everything before endIdx
					outputBytes := b[:endIdx]
					// Extract exit code string
					afterEnd := b[endIdx+len(endPrefix):]
					ecStr := ""
					for i, ch := range afterEnd {
						if ch == '_' || ch == '\r' || ch == '\n' {
							ecStr = string(afterEnd[:i])
							break
						}
					}
					if ecStr != "" {
						_, _ = fmt.Sscanf(ecStr, "%d", &exitCode)
					}
					rawBuf.Reset()
					rawBuf.Write(outputBytes)
					finished = true
				}
			}

		case <-tab.AIProcessDone:
			// Shell exited before printing END (e.g. exit 1 or process killed)
			exitCode = 1
			if tab.Cmd != nil && tab.Cmd.ProcessState != nil {
				exitCode = tab.Cmd.ProcessState.ExitCode()
			}
			finished = true

		case <-timeoutTimer.C:
			// Command timed out: send Ctrl+C (SIGINT) to PTY slave
			_, _ = tab.WriteCloser.Write([]byte{3})
			time.Sleep(200 * time.Millisecond)
			_, _ = tab.WriteCloser.Write([]byte{3, '\n'})

			// Check if process/shell returned or died; fallback kill if shell is completely stuck
			time.Sleep(200 * time.Millisecond)
			select {
			case <-tab.AIProcessDone:
				// Process died cleanly
			default:
				// If shell does not respond after timeout, kill tab process to prevent stuck state
				// for future commands
				if tab.Cmd != nil && tab.Cmd.Process != nil {
					_ = tab.Cmd.Process.Kill()
				}
				tab.Close()
				aiTabMu.Lock()
				aiTabActive = false
				aiTabMu.Unlock()
			}

			cleanOutput := stripANSI(rawBuf.Bytes())
			if len(cleanOutput) > 1024*1024 {
				cleanOutput = cleanOutput[:1024*1024] + "\n[Output truncated by RMTE buffer limit]"
			}
			return cleanOutput + fmt.Sprintf("\n[RMTE Error: Command timed out after %ds (exit 124). For background jobs/servers, set background: true or use 'nohup ... &']", timeoutSec), 124, nil
		}
	}

	cleanOutput := stripANSI(rawBuf.Bytes())
	if len(cleanOutput) > 1024*1024 {
		cleanOutput = cleanOutput[:1024*1024] + "\n[Output truncated by RMTE buffer limit]"
	}
	return cleanOutput, exitCode, nil
}

func handleAIExec(ctrl map[string]interface{}, conn *SafeConn) {
	targetConn, _ := ctrl["target_conn"].(string)
	opts := getHostOptions()
	if !opts.AllowExec {
		log.Printf("[Security] Rejected ai_exec request from %s: host execution is disabled (start with --allow-exec)", targetConn)
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "ai_exec_result",
			"target_conn": targetConn,
			"error":       "Remote command execution is disabled on this host. Start host with --allow-exec to enable.",
			"exit_code":   1,
		})
		return
	}

	if runtime.GOOS == "windows" {
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "ai_exec_result",
			"target_conn": targetConn,
			"error":       "Remote execution in v1 requires a Linux/POSIX host (Docker/Kubernetes/VPS). Windows host execution will be available in v2.",
			"exit_code":   1,
		})
		return
	}

	command, _ := ctrl["command"].(string)
	if command == "" {
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "ai_exec_result",
			"target_conn": targetConn,
			"error":       "command parameter is required",
			"exit_code":   1,
		})
		return
	}

	background, _ := ctrl["background"].(bool)
	timeoutSec := 60
	if to, ok := ctrl["timeout_seconds"].(float64); ok && to > 0 {
		timeoutSec = int(to)
	}

	logEvent(conn, "SECURITY_AI_EXEC", targetConn, fmt.Sprintf("AI Exec: %s (bg: %v)", command, background))

	if background {
		tabsMu.Lock()
		newID := nextTabID
		nextTabID++
		tabsMu.Unlock()

		tabName := "DevServer"
		if tn, ok := ctrl["tab_name"].(string); ok && tn != "" {
			tabName = tn
		}
		title := fmt.Sprintf("[%s]", tabName)
		createTabWithTitle(newID, conn, title, false)

		tabsMu.RLock()
		bgTab := tabs[newID]
		tabsMu.RUnlock()
		if bgTab != nil {
			time.Sleep(50 * time.Millisecond)
			_, _ = bgTab.WriteCloser.Write([]byte(command + "\n"))
		}

		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "ai_exec_result",
			"target_conn": targetConn,
			"stdout":      fmt.Sprintf("[RMTE] Started background process in Tab %d (%s):\n%s", newID, title, command),
			"exit_code":   0,
		})
		return
	}

	// Synchronous execution in persistent [AI] tab
	aiExecMutex.Lock()
	defer aiExecMutex.Unlock()

	tabID, tab, err := ensureAITab(conn)
	if err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "ai_exec_result",
			"target_conn": targetConn,
			"error":       fmt.Sprintf("Failed to initialize [AI] tab: %v", err),
			"exit_code":   1,
		})
		return
	}
	_ = tabID

	stdout, exitCode, err := executeAICommand(tab, command, timeoutSec)
	if err != nil {
		conn.WriteJSON(map[string]interface{}{
			"type":        "control",
			"action":      "ai_exec_result",
			"target_conn": targetConn,
			"error":       err.Error(),
			"stdout":      stdout,
			"exit_code":   exitCode,
		})
		return
	}

	conn.WriteJSON(map[string]interface{}{
		"type":        "control",
		"action":      "ai_exec_result",
		"target_conn": targetConn,
		"stdout":      stdout,
		"exit_code":   exitCode,
	})
}

func handleAIGetInfo(targetConn string, conn *SafeConn) {
	hostname, _ := os.Hostname()
	wd, _ := os.Getwd()
	if hostWorkDir != "" {
		wd = hostWorkDir
	}

	tabsMu.RLock()
	activeList := make([]map[string]interface{}, 0)
	for id, t := range tabs {
		title := t.Title
		if title == "" {
			title = fmt.Sprintf("Terminal %d", id)
		}
		activeList = append(activeList, map[string]interface{}{
			"id":    id,
			"title": title,
			"is_ai": t.IsAI,
		})
	}
	tabsMu.RUnlock()

	opts := getHostOptions()
	info := map[string]interface{}{
		"os":         runtime.GOOS,
		"arch":       runtime.GOARCH,
		"hostname":   hostname,
		"work_dir":   wd,
		"pid":        os.Getpid(),
		"preview":    opts.Preview,
		"tabs":       activeList,
		"allow_exec": opts.AllowExec,
		"version":    appVersion,
	}

	conn.WriteJSON(map[string]interface{}{
		"type":        "control",
		"action":      "ai_get_info_result",
		"target_conn": targetConn,
		"info":        info,
	})
}
