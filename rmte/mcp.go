package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

var mcpIDRegex = regexp.MustCompile(`"id"\s*:\s*([0-9]+|"[^"]*")`)

// MCP JSON-RPC 2.0 structures
type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *mcpError   `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpTool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema"`
}

type mcpToolResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

var stdoutMu sync.Mutex

func sendMCPResponse(resp mcpResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[MCP] Failed to marshal response: %v\n", err)
		return
	}
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	os.Stdout.Write(data)
	os.Stdout.Write([]byte("\n"))
}

func mcpToolText(text string, isError bool) mcpToolResult {
	return mcpToolResult{
		Content: []mcpContent{
			{
				Type: "text",
				Text: text,
			},
		},
		IsError: isError,
	}
}

func getMCPToolsList() []mcpTool {
	return []mcpTool{
		{
			Name:        "rmte_exec",
			Description: "Execute a shell command inside the remote host session ([AI] persistent tab or dedicated background tab). Returns stdout, stderr, and remote exit code.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command": map[string]interface{}{
						"type":        "string",
						"description": "Shell command to run on the remote host (e.g. 'git status', 'npm test')",
					},
					"timeout_seconds": map[string]interface{}{
						"type":        "integer",
						"description": "Command execution timeout in seconds (default: 60)",
					},
					"background": map[string]interface{}{
						"type":        "boolean",
						"description": "If true, runs command in a dedicated background tab (e.g. for long-running servers) and returns immediately",
					},
					"tab_name": map[string]interface{}{
						"type":        "string",
						"description": "Custom tab title when running in background (e.g. 'DevServer')",
					},
				},
				"required": []string{"command"},
			},
		},
		{
			Name:        "rmte_read_file",
			Description: "Read file contents as UTF-8 text from the remote host workspace.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Path to file on the remote host",
					},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        "rmte_write_file",
			Description: "Write or overwrite text content to a file on the remote host.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Path to file on the remote host",
					},
					"content": map[string]interface{}{
						"type":        "string",
						"description": "Text content to write into the file",
					},
				},
				"required": []string{"path", "content"},
			},
		},
		{
			Name:        "rmte_upload_file",
			Description: "Upload a local file from the AI runner machine to the remote host.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"local_path": map[string]interface{}{
						"type":        "string",
						"description": "Path to local file on AI runner machine",
					},
					"remote_path": map[string]interface{}{
						"type":        "string",
						"description": "Destination path on the remote host",
					},
				},
				"required": []string{"local_path", "remote_path"},
			},
		},
		{
			Name:        "rmte_download_file",
			Description: "Download a file from the remote host to the AI runner local machine.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"remote_path": map[string]interface{}{
						"type":        "string",
						"description": "Path to file on remote host",
					},
					"local_path": map[string]interface{}{
						"type":        "string",
						"description": "Destination path on local AI runner machine",
					},
				},
				"required": []string{"remote_path", "local_path"},
			},
		},
		{
			Name:        "rmte_list_files",
			Description: "List files and directories in a remote workspace folder.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Directory path on remote host (default: current workspace root)",
					},
				},
			},
		},
		{
			Name:        "rmte_get_info",
			Description: "Get metadata about the remote host environment (OS, architecture, hostname, working directory, PID, active tabs, permissions).",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	}
}

func handleMCPRequest(req mcpRequest, bot *BotClient, tools []mcpTool) *mcpResponse {
	switch req.Method {
	case "initialize":
		return &mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]interface{}{
					"tools": map[string]interface{}{},
				},
				"serverInfo": map[string]interface{}{
					"name":    "rmte-mcp",
					"version": appVersion,
				},
			},
		}

	case "notifications/initialized":
		return nil

	case "ping":
		if req.ID != nil {
			return &mcpResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  map[string]interface{}{},
			}
		}
		return nil

	case "tools/list":
		return &mcpResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]interface{}{
				"tools": tools,
			},
		}

	case "tools/call":
		var callParams struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &callParams); err != nil {
			return &mcpResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &mcpError{
					Code:    -32602,
					Message: fmt.Sprintf("Invalid params: %v", err),
				},
			}
		}

		switch callParams.Name {
		case "rmte_exec":
			if bot == nil {
				return &mcpResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  mcpToolText("Mock exec result for test", false),
				}
			}
			command, _ := callParams.Arguments["command"].(string)
			timeoutSec := 60
			if to, ok := callParams.Arguments["timeout_seconds"].(float64); ok && to > 0 {
				timeoutSec = int(to)
			}
			background, _ := callParams.Arguments["background"].(bool)
			tabName, _ := callParams.Arguments["tab_name"].(string)

			stdout, exitCode, err := bot.ExecCommand(command, timeoutSec, background, tabName)
			if err != nil {
				return &mcpResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  mcpToolText(fmt.Sprintf("%s\nError: %v (Exit code: %d)", stdout, err, exitCode), true),
				}
			} else if exitCode != 0 {
				return &mcpResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  mcpToolText(fmt.Sprintf("%s\n(Command exited with status %d)", stdout, exitCode), true),
				}
			} else {
				return &mcpResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  mcpToolText(stdout, false),
				}
			}

		case "rmte_read_file":
			if bot == nil {
				return &mcpResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  mcpToolText("Mock read result for test", false),
				}
			}
			path, _ := callParams.Arguments["path"].(string)
			data, err := bot.ReadFile(path)
			if err != nil {
				return &mcpResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  mcpToolText(fmt.Sprintf("Failed to read %s: %v", path, err), true),
				}
			}
			return &mcpResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  mcpToolText(string(data), false),
			}

		case "rmte_write_file":
			if bot == nil {
				return &mcpResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  mcpToolText("Mock write result for test", false),
				}
			}
			path, _ := callParams.Arguments["path"].(string)
			content, _ := callParams.Arguments["content"].(string)
			err := bot.WriteFile(path, []byte(content))
			if err != nil {
				return &mcpResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  mcpToolText(fmt.Sprintf("Failed to write %s: %v", path, err), true),
				}
			}
			return &mcpResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  mcpToolText(fmt.Sprintf("Successfully saved %d bytes to %s", len(content), path), false),
			}

		case "rmte_upload_file":
			if bot == nil {
				return &mcpResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  mcpToolText("Mock upload result for test", false),
				}
			}
			localPath, _ := callParams.Arguments["local_path"].(string)
			remotePath, _ := callParams.Arguments["remote_path"].(string)
			err := bot.UploadFile(localPath, remotePath)
			if err != nil {
				return &mcpResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  mcpToolText(fmt.Sprintf("Failed to upload %s to %s: %v", localPath, remotePath, err), true),
				}
			}
			return &mcpResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  mcpToolText(fmt.Sprintf("Successfully uploaded %s to %s", localPath, remotePath), false),
			}

		case "rmte_download_file":
			if bot == nil {
				return &mcpResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  mcpToolText("Mock download result for test", false),
				}
			}
			remotePath, _ := callParams.Arguments["remote_path"].(string)
			localPath, _ := callParams.Arguments["local_path"].(string)
			err := bot.DownloadFile(remotePath, localPath)
			if err != nil {
				return &mcpResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  mcpToolText(fmt.Sprintf("Failed to download %s to %s: %v", remotePath, localPath, err), true),
				}
			}
			return &mcpResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  mcpToolText(fmt.Sprintf("Successfully downloaded %s to %s", remotePath, localPath), false),
			}

		case "rmte_list_files":
			if bot == nil {
				return &mcpResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  mcpToolText("Mock list result for test", false),
				}
			}
			path, _ := callParams.Arguments["path"].(string)
			if path == "" {
				path = "."
			}
			entries, err := bot.ListFiles(path)
			if err != nil {
				return &mcpResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  mcpToolText(fmt.Sprintf("Failed to list %s: %v", path, err), true),
				}
			}
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Directory listing for '%s' (%d items):\n", path, len(entries)))
			sb.WriteString(fmt.Sprintf("%-6s  %-10s  %s\n", "TYPE", "SIZE", "NAME"))
			sb.WriteString(strings.Repeat("-", 40) + "\n")
			for _, e := range entries {
				entryType := "FILE"
				if e.IsDir {
					entryType = "DIR"
				}
				sb.WriteString(fmt.Sprintf("%-6s  %-10d  %s\n", entryType, e.Size, e.Name))
			}
			return &mcpResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  mcpToolText(sb.String(), false),
			}

		case "rmte_get_info":
			if bot == nil {
				return &mcpResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  mcpToolText("{}", false),
				}
			}
			info, err := bot.GetInfo()
			if err != nil {
				return &mcpResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  mcpToolText(fmt.Sprintf("Failed to get info: %v", err), true),
				}
			}
			raw, _ := json.MarshalIndent(info, "", "  ")
			return &mcpResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  mcpToolText(string(raw), false),
			}

		default:
			return &mcpResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &mcpError{
					Code:    -32601,
					Message: fmt.Sprintf("Tool not found: %s", callParams.Name),
				},
			}
		}

	default:
		if req.ID != nil {
			return &mcpResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &mcpError{
					Code:    -32601,
					Message: fmt.Sprintf("Method not found: %s", req.Method),
				},
			}
		}
		return nil
	}
}

// readMCPLine reads a single line (JSON-RPC message) from reader up to maxLimit bytes.
// If a message exceeds maxLimit, it drains the oversized line and returns tooLarge = true,
// attempting to extract the client's request ID so error responses match the client's promise.
func readMCPLine(r *bufio.Reader, maxLimit int64) (line []byte, tooLarge bool, reqID interface{}, err error) {
	var buf bytes.Buffer
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			if err == io.EOF && buf.Len() > 0 {
				return buf.Bytes(), false, nil, nil
			}
			return nil, false, nil, err
		}
		if int64(buf.Len()+len(chunk)) > maxLimit {
			tooLarge = true
			buf.Write(chunk)
			if m := mcpIDRegex.FindSubmatch(buf.Bytes()); len(m) > 1 {
				rawID := string(m[1])
				if strings.HasPrefix(rawID, "\"") && strings.HasSuffix(rawID, "\"") {
					reqID = strings.Trim(rawID, "\"")
				} else if num, err := strconv.ParseInt(rawID, 10, 64); err == nil {
					reqID = num
				}
			}
			for isPrefix {
				_, isPrefix, err = r.ReadLine()
				if err != nil {
					break
				}
			}
			return nil, true, reqID, nil
		}
		buf.Write(chunk)
		if !isPrefix {
			break
		}
	}
	return buf.Bytes(), false, nil, nil
}

// runMCP initiates the Model Context Protocol stdio server connected to the remote session.
func runMCP(serverURL, sessionID, password, name string, insecure bool) {
	fmt.Fprintf(os.Stderr, "[RMTE MCP] Connecting to session %s at %s...\n", sessionID, serverURL)

	bot, err := newBotClient(serverURL, sessionID, password, name, insecure)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[RMTE MCP] Connection failed: %v\n", err)
		os.Exit(1)
	}
	defer bot.Close()

	fmt.Fprintf(os.Stderr, "[RMTE MCP] Connected to session %s as %s. Serving MCP JSON-RPC on stdio.\n", sessionID, name)

	reader := bufio.NewReaderSize(os.Stdin, 64*1024)
	tools := getMCPToolsList()
	const maxMessageBytes = 25 * 1024 * 1024 // 25 MB per-message limit

	for {
		line, tooLarge, reqID, err := readMCPLine(reader, maxMessageBytes)
		if err != nil {
			if err == io.EOF {
				fmt.Fprintln(os.Stderr, "[RMTE MCP] Stdin closed, shutting down.")
				return
			}
			fmt.Fprintf(os.Stderr, "[RMTE MCP] Stdin read error: %v\n", err)
			return
		}

		if tooLarge {
			sendMCPResponse(mcpResponse{
				JSONRPC: "2.0",
				ID:      reqID,
				Error: &mcpError{
					Code:    -32600,
					Message: "Request entity too large (exceeds 25MB limit)",
				},
			})
			continue
		}

		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var req mcpRequest
		if err := json.Unmarshal(line, &req); err != nil {
			sendMCPResponse(mcpResponse{
				JSONRPC: "2.0",
				ID:      nil,
				Error: &mcpError{
					Code:    -32700,
					Message: fmt.Sprintf("Parse error: %v", err),
				},
			})
			continue
		}

		resp := handleMCPRequest(req, bot, tools)
		if resp != nil {
			sendMCPResponse(*resp)
		}
	}
}
