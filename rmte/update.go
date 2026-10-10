package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	githubRepo      = "milio48/rmte"
	githubLatestURL = "https://api.github.com/repos/" + githubRepo + "/releases/latest"
)

type githubRelease struct {
	TagName string        `json:"tag_name"`
	Name    string        `json:"name"`
	Body    string        `json:"body"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

func cmdUpdate() {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	checkOnly := fs.Bool("check", false, "Check for updates without downloading")
	c := fs.Bool("c", false, "Alias for --check")
	force := fs.Bool("force", false, "Force re-download even if already on the latest version")
	restart := fs.Bool("restart", false, "Gracefully restart running background session after update")
	r := fs.Bool("r", false, "Alias for --restart")

	if len(os.Args) > 1 {
		_ = fs.Parse(os.Args[1:])
	}

	exePath, err := os.Executable()
	if err != nil {
		fatalf("Error determining executable path: %v", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		fatalf("Error resolving symlink: %v", err)
	}

	fmt.Printf("Current version: v%s (%s/%s)\n", appVersion, runtime.GOOS, runtime.GOARCH)
	fmt.Printf("Checking for updates from https://github.com/%s ...\n", githubRepo)

	release, err := fetchLatestRelease()
	if err != nil {
		fatalf("Error checking for updates: %v", err)
	}

	latestVersion := strings.TrimPrefix(release.TagName, "v")
	cmp := compareVersions(appVersion, latestVersion)

	if cmp >= 0 && !*force {
		fmt.Printf("✅ rmte is already up to date (version v%s).\n", appVersion)
		return
	}

	fmt.Printf("✨ New version available: v%s -> v%s\n", appVersion, latestVersion)

	if *checkOnly || *c {
		fmt.Printf("Run 'rmte update' to download and install this update.\n")
		return
	}

	// Determine matching asset
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	targetAssetName := fmt.Sprintf("rmte-%s-%s%s", runtime.GOOS, runtime.GOARCH, ext)

	var targetAsset *githubAsset
	for i := range release.Assets {
		if release.Assets[i].Name == targetAssetName {
			targetAsset = &release.Assets[i]
			break
		}
	}

	if targetAsset == nil {
		fatalf("Error: No release binary found for %s/%s in release %s", runtime.GOOS, runtime.GOARCH, release.TagName)
	}

	fmt.Printf("Downloading %s (%.2f MB)...\n", targetAsset.Name, float64(targetAsset.Size)/(1024*1024))

	exeDir := filepath.Dir(exePath)
	tempFile, err := os.CreateTemp(exeDir, "rmte-update-*.tmp")
	if err != nil {
		fatalf("Error creating temporary update file in %s: %v", exeDir, err)
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)

	if err := downloadAsset(targetAsset.BrowserDownloadURL, tempFile); err != nil {
		_ = tempFile.Close()
		fatalf("Error downloading update: %v", err)
	}
	_ = tempFile.Close()

	if err := os.Chmod(tempPath, 0755); err != nil {
		fatalf("Error setting executable permissions: %v", err)
	}

	// Replace the current executable
	if err := replaceExecutable(tempPath, exePath); err != nil {
		fatalf("Error applying update: %v", err)
	}

	fmt.Printf("✅ Successfully updated rmte to v%s!\n", latestVersion)

	// If restart requested, restart running background session
	if *restart || *r {
		restartActiveSession(exePath)
	}
}

func fetchLatestRelease() (*githubRelease, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", githubLatestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", fmt.Sprintf("rmte/%s", appVersion))
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %s", resp.Status)
	}

	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func downloadAsset(url string, w io.Writer) error {
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", fmt.Sprintf("rmte/%s", appVersion))

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download server returned %s", resp.Status)
	}

	_, err = io.Copy(w, resp.Body)
	return err
}

func replaceExecutable(newBinaryPath, currentExePath string) error {
	if runtime.GOOS == "windows" {
		oldExePath := currentExePath + ".old"
		_ = os.Remove(oldExePath) // remove previous old file if present
		if err := os.Rename(currentExePath, oldExePath); err != nil {
			return fmt.Errorf("failed to backup current executable: %w", err)
		}
		if err := os.Rename(newBinaryPath, currentExePath); err != nil {
			// Rollback if possible
			_ = os.Rename(oldExePath, currentExePath)
			return fmt.Errorf("failed to place new executable: %w", err)
		}
		_ = os.Remove(oldExePath) // best effort delete
		return nil
	}

	// On Unix, atomic rename is supported
	return os.Rename(newBinaryPath, currentExePath)
}

// cleanOldBinary removes leftover .old binaries from previous updates (Windows)
func cleanOldBinary() {
	if runtime.GOOS != "windows" {
		return
	}
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	oldPath := exePath + ".old"
	if _, err := os.Stat(oldPath); err == nil {
		_ = os.Remove(oldPath)
	}
}

type restartTarget struct {
	sessionID string
	pidFile   string
	pid       int
	meta      *SessionMeta
}

// findRunningRMTEPids scans /proc on Linux for any other running rmte processes (PID != current)
func findRunningRMTEPids() []int {
	if runtime.GOOS != "linux" {
		return nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var pids []int
	currentPid := os.Getpid()
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 || pid == currentPid {
			continue
		}
		commBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
		if err != nil {
			continue
		}
		comm := strings.TrimSpace(string(commBytes))
		if comm == "rmte" {
			pids = append(pids, pid)
		}
	}
	return pids
}

// restartActiveSession searches for active sessions and relaunches them with new binary
func restartActiveSession(exePath string) {
	targets := make(map[string]*restartTarget)

	// 1. Scan PID files
	if matches, err := filepath.Glob("rmte-*.pid"); err == nil {
		for _, pf := range matches {
			data, err := os.ReadFile(pf)
			if err != nil {
				continue
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil || pid <= 0 {
				continue
			}
			sID := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(pf), "rmte-"), ".pid")
			if isPidAlive(pid) {
				targets[sID] = &restartTarget{
					sessionID: sID,
					pidFile:   pf,
					pid:       pid,
				}
			} else {
				_ = os.Remove(pf)
			}
		}
	}

	// 2. Scan meta files to attach metadata or detect sessions
	if matches, err := filepath.Glob("rmte-*.meta"); err == nil {
		for _, mf := range matches {
			sID := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(mf), "rmte-"), ".meta")
			meta, err := loadSessionMeta(sID)
			if err != nil || meta == nil {
				continue
			}
			if target, exists := targets[sID]; exists {
				target.meta = meta
			}
		}
	}

	// 3. Fallback: if no PID file found, check for running rmte process on Linux
	if len(targets) == 0 {
		runningPids := findRunningRMTEPids()
		if len(runningPids) > 0 {
			var foundSessionID string
			var foundMeta *SessionMeta
			if matches, err := filepath.Glob("rmte-*.meta"); err == nil && len(matches) > 0 {
				foundSessionID = strings.TrimSuffix(strings.TrimPrefix(filepath.Base(matches[0]), "rmte-"), ".meta")
				foundMeta, _ = loadSessionMeta(foundSessionID)
			}
			if foundSessionID == "" {
				foundSessionID = "default"
			}
			for _, p := range runningPids {
				targets[foundSessionID] = &restartTarget{
					sessionID: foundSessionID,
					pid:       p,
					meta:      foundMeta,
				}
				break // handle primary daemon
			}
		}
	}

	if len(targets) == 0 {
		fmt.Println("No active background session PID files found to restart.")
		return
	}

	for sessionID, target := range targets {
		fmt.Printf("Stopping previous session %q (PID %d)...\n", sessionID, target.pid)
		_ = killPid(target.pid)
		if target.pidFile != "" {
			_ = os.Remove(target.pidFile)
		}

		// Wait for process to fully terminate (up to 5 seconds)
		killDeadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(killDeadline) && isPidAlive(target.pid) {
			time.Sleep(100 * time.Millisecond)
		}

		meta := target.meta
		// Check if a supervisor (e.g. startup.sh loop, systemd, docker) restarts it automatically
		restartedBySupervisor := false
		if meta != nil && meta.Port > 0 {
			pollDeadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(pollDeadline) {
				conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", meta.Port), 200*time.Millisecond)
				if err == nil {
					conn.Close()
					restartedBySupervisor = true
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
		}

		if restartedBySupervisor {
			fmt.Printf("✅ Session %q restarted by supervisor. Connected web viewers will auto-reconnect!\n", sessionID)
			continue
		}

		// Relaunch if not supervised
		var args []string
		if meta != nil && (meta.Mode == modeStandalone || meta.Mode == modeHybrid) {
			args = []string{"serve", "--mode=" + meta.Mode, "--id=" + sessionID, "-q"}
			if meta.Password != "" {
				args = append(args, "--pass="+meta.Password)
			}
			if meta.Dir != "" {
				args = append(args, "--dir="+meta.Dir)
			}
			if meta.Buffer > 0 {
				args = append(args, fmt.Sprintf("--buffer=%d", meta.Buffer))
			}
			if meta.Preview {
				args = append(args, "--web-preview")
			}
			if meta.Port > 0 {
				args = append(args, fmt.Sprintf("--port=%d", meta.Port))
			}
			if meta.Public {
				args = append(args, "--public")
			}
			if meta.PublicURL != "" {
				args = append(args, "--public-url="+meta.PublicURL)
			}
			if meta.WebPath != "" {
				args = append(args, "--web-path="+meta.WebPath)
			}
			if meta.WSPath != "" {
				args = append(args, "--ws-path="+meta.WSPath)
			}
			if meta.AdminPath != "" {
				args = append(args, "--admin-path="+meta.AdminPath)
			}
			if meta.AdminPass != "" {
				args = append(args, "--admin-pass="+meta.AdminPass)
			}
			fmt.Printf("Restoring %s serve session %q with original encrypted credentials & configuration...\n", meta.Mode, sessionID)
		} else if sessionID == "relay" {
			args = []string{"serve", "--mode=relay", "-q"}
			fmt.Printf("Restoring relay session with new binary...\n")
		} else {
			args = []string{"--id=" + sessionID, "-q"}
			if meta != nil {
				if meta.Password != "" {
					args = append(args, "--pass="+meta.Password)
				}
				if meta.ServerRelay != "" {
					args = append(args, "--server-relay="+meta.ServerRelay)
				}
				if meta.Dir != "" {
					args = append(args, "--dir="+meta.Dir)
				}
				if meta.Buffer > 0 {
					args = append(args, fmt.Sprintf("--buffer=%d", meta.Buffer))
				}
				if !meta.Preview {
					args = append(args, "--web-preview=false")
				}
				fmt.Printf("Restoring share session %q with original encrypted credentials & configuration...\n", sessionID)
			} else {
				fmt.Printf("Restarting session %q with new binary...\n", sessionID)
			}
		}

		cmd := exec.Command(exePath, args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Printf("Warning: failed to restart session %q: %v\n", sessionID, err)
		} else {
			fmt.Printf("✅ Session %q restarted. Connected web viewers will auto-reconnect!\n", sessionID)
		}
	}
}

// compareVersions compares two semver strings like "0.5.0" and "0.5.1"
// Returns -1 if v1 < v2, 0 if v1 == v2, 1 if v1 > v2
func compareVersions(v1, v2 string) int {
	if v1 == "dev" {
		return -1 // dev builds are treated as older than tagged releases
	}
	if v2 == "dev" {
		return 1
	}

	p1 := parseVersion(v1)
	p2 := parseVersion(v2)

	for i := 0; i < len(p1) && i < len(p2); i++ {
		if p1[i] < p2[i] {
			return -1
		}
		if p1[i] > p2[i] {
			return 1
		}
	}
	if len(p1) < len(p2) {
		return -1
	}
	if len(p1) > len(p2) {
		return 1
	}
	return 0
}

func parseVersion(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.Split(v, ".")
	res := make([]int, 0, len(parts))
	for _, p := range parts {
		// Strip any prerelease suffix like "-beta"
		if idx := strings.IndexAny(p, "-+"); idx != -1 {
			p = p[:idx]
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			n = 0
		}
		res = append(res, n)
	}
	return res
}
