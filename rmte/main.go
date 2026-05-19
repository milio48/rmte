package main

import (
	"flag"
	"fmt"
	"os"
)

const protocolVersion = "0.2"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		return
	}

	mode := os.Args[1]
	os.Args = os.Args[1:] // shift args for flags

	switch mode {
	case "serve":
		port := flag.Int("port", 8080, "Port to listen on")
		flag.Parse()
		runServer(*port)
	case "share":
		server := flag.String("server", "ws://localhost:8080/ws", "Relay server URL")
		pass := flag.String("pass", "", "Password for E2EE")
		bufferMB := flag.Int("buffer", 1, "Max buffer size in MB (applies to terminal ring buffer and file manager)")
		flag.Parse()
		if *pass == "" {
			fmt.Println("Error: --pass is required for E2EE")
			return
		}
		runHost(*server, *pass, *bufferMB)
	case "join":
		server := flag.String("server", "ws://localhost:8080/ws", "Relay server URL")
		sessionID := flag.String("id", "", "Session ID to join")
		pass := flag.String("pass", "", "Password for E2EE")
		name := flag.String("name", "", "Display name of the viewer")
		flag.Parse()
		if *sessionID == "" || *pass == "" {
			fmt.Println("Error: --id and --pass are required")
			return
		}
		runViewer(*server, *sessionID, *pass, *name)
	default:
		printUsage()
	}
}

func printUsage() {
	fmt.Println("rmte - Remote Terminal Relay")
	fmt.Println("Usage:")
	fmt.Println("  rmte serve --port=8080")
	fmt.Println("  rmte share --server=\"ws://...\" --pass=\"secret\" [--buffer=1]")
	fmt.Println("  rmte join --server=\"ws://...\" --id=\"...\" --pass=\"secret\" [--name=\"name\"]")
}
