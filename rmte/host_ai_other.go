//go:build !windows

package main

import "fmt"

func executeAICommandWindows(tab *TabSession, command string, timeoutSec int) (string, int, error) {
	return "", 1, fmt.Errorf("executeAICommandWindows is only supported on Windows")
}
