package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
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

// restartActiveSession searches for active sessions and relaunches them with new binary
func restartActiveSession(exePath string) {
	matches, err := filepath.Glob("rmte-*.pid")
	if err != nil || len(matches) == 0 {
		fmt.Println("No active background session PID files found to restart.")
		return
	}

	for _, pidFile := range matches {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			continue
		}
		pidStr := strings.TrimSpace(string(data))
		pid, err := strconv.Atoi(pidStr)
		if err != nil || pid <= 0 {
			continue
		}

		sessionID := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(pidFile), "rmte-"), ".pid")
		fmt.Printf("Stopping previous background session %q (PID %d)...\n", sessionID, pid)
		_ = killPid(pid)
		_ = os.Remove(pidFile)

		time.Sleep(500 * time.Millisecond)

		fmt.Printf("Restarting session %q with new binary...\n", sessionID)
		cmd := exec.Command(exePath, "--id="+sessionID, "-q")
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
