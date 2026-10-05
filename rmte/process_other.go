//go:build !windows

package main

// interruptTabProcess is a no-op on Unix/Linux/macOS because pty.Start
// creates a real PTY where writing \x03 to the master delivers SIGINT to the
// foreground process group.
func interruptTabProcess(tab *TabSession) {
}
