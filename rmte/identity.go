package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/user"
	"runtime"
	"strings"
	"os/exec"
)

func generateViewerID() string {
	hostname, _ := os.Hostname()
	
	currentUser, err := user.Current()
	username := "unknown"
	if err == nil {
		// On windows, username might contain domain, strip it if necessary or just use as is
		username = currentUser.Username
	}

	machineID := ""
	switch runtime.GOOS {
	case "linux":
		b, _ := os.ReadFile("/etc/machine-id")
		machineID = strings.TrimSpace(string(b))
	case "windows":
		// Powerhell way to get MachineGuid
		out, _ := exec.Command("powershell", "-Command", `(Get-ItemProperty -Path "HKLM:\SOFTWARE\Microsoft\Cryptography").MachineGuid`).Output()
		machineID = strings.TrimSpace(string(out))
	case "darwin":
		out, _ := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
		machineID = string(out) // Crude but works for hashing
	}

	rawString := fmt.Sprintf("%s|%s|%s|rmte-app", hostname, username, machineID)
	hash := sha256.Sum256([]byte(rawString))
	return fmt.Sprintf("v-%x", hash[:4])
}
