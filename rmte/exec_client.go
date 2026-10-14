package main

import (
	"fmt"
	"os"
	"strings"
)

// runExec connects to the remote host as a headless bot and executes a command,
// streaming stdout to the local stdout, errors to stderr, and returning the remote exit code.
func runExec(serverURL, sessionID, password, name, cmd string, timeout int, background bool, insecure bool) int {
	if insecure {
		setupTLSConfig(true)
	}

	bot, err := newBotClient(serverURL, sessionID, password, name, insecure)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to session %s: %v\n", sessionID, err)
		return 1
	}
	defer bot.Close()

	stdout, exitCode, err := bot.ExecCommand(cmd, timeout, background, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Execution error: %v\n", err)
		if stdout != "" {
			fmt.Print(stdout)
			if !strings.HasSuffix(stdout, "\n") {
				fmt.Println()
			}
		}
		if exitCode != 0 {
			return exitCode
		}
		return 1
	}

	if stdout != "" {
		fmt.Print(stdout)
		if !strings.HasSuffix(stdout, "\n") {
			fmt.Println()
		}
	}
	return exitCode
}
