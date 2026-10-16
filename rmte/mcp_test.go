package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestMCPInitialize(t *testing.T) {
	tools := getMCPToolsList()
	req := mcpRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
	}

	resp := handleMCPRequest(req, nil, tools)
	if resp == nil {
		t.Fatalf("expected response, got nil")
	}
	if resp.ID != 1 {
		t.Errorf("expected ID 1, got %v", resp.ID)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	resMap, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected result map, got: %T", resp.Result)
	}

	if resMap["protocolVersion"] != "2025-03-26" {
		t.Errorf("expected protocolVersion 2025-03-26, got %v", resMap["protocolVersion"])
	}

	instr, ok := resMap["instructions"].(string)
	if !ok || len(instr) == 0 {
		t.Errorf("expected instructions to be non-empty string, got %v", resMap["instructions"])
	}

	sInfo, ok := resMap["serverInfo"].(map[string]interface{})
	if !ok || sInfo["name"] != "rmte-mcp" {
		t.Errorf("expected serverInfo.name rmte-mcp, got %v", sInfo)
	}
}

func TestMCPToolAnnotationsSchema(t *testing.T) {
	tools := getMCPToolsList()
	data, err := json.Marshal(tools)
	if err != nil {
		t.Fatalf("failed to marshal tools: %v", err)
	}

	var rawTools []map[string]interface{}
	if err := json.Unmarshal(data, &rawTools); err != nil {
		t.Fatalf("failed to unmarshal tools JSON: %v", err)
	}

	toolMap := make(map[string]map[string]interface{})
	for _, tool := range rawTools {
		name, _ := tool["name"].(string)
		toolMap[name] = tool

		// Verification 1: title must NOT be at root level of Tool
		if _, hasRootTitle := tool["title"]; hasRootTitle {
			t.Errorf("tool %q has 'title' at root level, must only be inside 'annotations'", name)
		}

		// Verification 2: annotations object must exist
		ann, ok := tool["annotations"].(map[string]interface{})
		if !ok || ann == nil {
			t.Errorf("tool %q missing 'annotations' object", name)
			continue
		}

		// Verification 3: title must be inside annotations
		if title, ok := ann["title"].(string); !ok || title == "" {
			t.Errorf("tool %q missing or empty annotations.title", name)
		}

		// Verification 4: readOnlyHint, destructiveHint, openWorldHint must be explicitly present in JSON
		if _, ok := ann["readOnlyHint"].(bool); !ok {
			t.Errorf("tool %q missing readOnlyHint in JSON (must not be omitted)", name)
		}
		if _, ok := ann["destructiveHint"].(bool); !ok {
			t.Errorf("tool %q missing destructiveHint in JSON (must not be omitted)", name)
		}
		if _, ok := ann["openWorldHint"].(bool); !ok {
			t.Errorf("tool %q missing openWorldHint in JSON (must not be omitted)", name)
		}
	}

	// Verify specific semantics
	// rmte_read_file: readOnly=true, destructive=false, openWorld=false, idempotent=true
	if rf, ok := toolMap["rmte_read_file"]; ok {
		ann := rf["annotations"].(map[string]interface{})
		if ann["destructiveHint"] != false {
			t.Errorf("rmte_read_file destructiveHint expected false, got %v", ann["destructiveHint"])
		}
		if ann["readOnlyHint"] != true {
			t.Errorf("rmte_read_file readOnlyHint expected true, got %v", ann["readOnlyHint"])
		}
		if ann["openWorldHint"] != false {
			t.Errorf("rmte_read_file openWorldHint expected false, got %v", ann["openWorldHint"])
		}
		if ann["idempotentHint"] != true {
			t.Errorf("rmte_read_file idempotentHint expected true, got %v", ann["idempotentHint"])
		}
	}

	// rmte_exec: readOnly=false, destructive=true, openWorld=true, idempotent=false
	if exec, ok := toolMap["rmte_exec"]; ok {
		ann := exec["annotations"].(map[string]interface{})
		if ann["destructiveHint"] != true {
			t.Errorf("rmte_exec destructiveHint expected true, got %v", ann["destructiveHint"])
		}
		if ann["readOnlyHint"] != false {
			t.Errorf("rmte_exec readOnlyHint expected false, got %v", ann["readOnlyHint"])
		}
		if ann["openWorldHint"] != true {
			t.Errorf("rmte_exec openWorldHint expected true, got %v", ann["openWorldHint"])
		}
		if ann["idempotentHint"] != false {
			t.Errorf("rmte_exec idempotentHint expected false, got %v", ann["idempotentHint"])
		}
	}

	// rmte_write_file: openWorld=false, idempotent=true, destructive=true
	if wf, ok := toolMap["rmte_write_file"]; ok {
		ann := wf["annotations"].(map[string]interface{})
		if ann["openWorldHint"] != false {
			t.Errorf("rmte_write_file openWorldHint expected false, got %v", ann["openWorldHint"])
		}
		if ann["idempotentHint"] != true {
			t.Errorf("rmte_write_file idempotentHint expected true, got %v", ann["idempotentHint"])
		}
	}

	// rmte_download_file: openWorld=true (writes locally), readOnly=true (on remote)
	if df, ok := toolMap["rmte_download_file"]; ok {
		ann := df["annotations"].(map[string]interface{})
		if ann["openWorldHint"] != true {
			t.Errorf("rmte_download_file openWorldHint expected true, got %v", ann["openWorldHint"])
		}
		if ann["readOnlyHint"] != true {
			t.Errorf("rmte_download_file readOnlyHint expected true, got %v", ann["readOnlyHint"])
		}
	}
}

func TestMCPPing(t *testing.T) {
	tools := getMCPToolsList()
	req := mcpRequest{
		JSONRPC: "2.0",
		ID:      "ping-123",
		Method:  "ping",
	}

	resp := handleMCPRequest(req, nil, tools)
	if resp == nil {
		t.Fatalf("expected response, got nil")
	}
	if resp.ID != "ping-123" {
		t.Errorf("expected ID ping-123, got %v", resp.ID)
	}
}

func TestMCPToolsList(t *testing.T) {
	tools := getMCPToolsList()
	req := mcpRequest{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/list",
	}

	resp := handleMCPRequest(req, nil, tools)
	if resp == nil {
		t.Fatalf("expected response, got nil")
	}

	resMap, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected result map, got: %T", resp.Result)
	}

	toolList, ok := resMap["tools"].([]mcpTool)
	if !ok {
		t.Fatalf("expected []mcpTool, got: %T", resMap["tools"])
	}

	expectedTools := map[string]bool{
		"rmte_exec":          false,
		"rmte_read_file":     false,
		"rmte_write_file":    false,
		"rmte_upload_file":   false,
		"rmte_download_file": false,
		"rmte_list_files":    false,
		"rmte_get_info":      false,
	}

	for _, tool := range toolList {
		if _, exists := expectedTools[tool.Name]; exists {
			expectedTools[tool.Name] = true
		} else {
			t.Errorf("unexpected tool in tools/list: %s", tool.Name)
		}
	}

	for name, found := range expectedTools {
		if !found {
			t.Errorf("missing tool in tools/list: %s", name)
		}
	}
}

func TestMCPUnknownMethod(t *testing.T) {
	tools := getMCPToolsList()
	req := mcpRequest{
		JSONRPC: "2.0",
		ID:      99,
		Method:  "resources/list",
	}

	resp := handleMCPRequest(req, nil, tools)
	if resp == nil {
		t.Fatalf("expected response for unknown method with ID, got nil")
	}
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Fatalf("expected error -32601, got: %v", resp.Error)
	}
}

func TestMCPNotificationInitialized(t *testing.T) {
	tools := getMCPToolsList()
	req := mcpRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}

	resp := handleMCPRequest(req, nil, tools)
	if resp != nil {
		t.Fatalf("expected nil response for notification, got: %v", resp)
	}
}

func TestMCPToolCallMock(t *testing.T) {
	tools := getMCPToolsList()

	// Test calling rmte_exec with mock bot (nil)
	paramsJSON, _ := json.Marshal(map[string]interface{}{
		"name": "rmte_exec",
		"arguments": map[string]interface{}{
			"command": "uptime",
		},
	})

	req := mcpRequest{
		JSONRPC: "2.0",
		ID:      5,
		Method:  "tools/call",
		Params:  paramsJSON,
	}

	resp := handleMCPRequest(req, nil, tools)
	if resp == nil {
		t.Fatalf("expected response, got nil")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	tResult, ok := resp.Result.(mcpToolResult)
	if !ok {
		t.Fatalf("expected mcpToolResult, got: %T", resp.Result)
	}
	if len(tResult.Content) == 0 || !strings.Contains(tResult.Content[0].Text, "Mock") {
		t.Fatalf("expected mock result content, got: %+v", tResult)
	}
}

func TestReadMCPLine(t *testing.T) {
	// Test stream with:
	// 1. Valid line (80 bytes)
	// 2. Oversized line (150 bytes, exceeds 100 limit, includes "id": 42)
	// 3. Subsequent valid line (50 bytes)
	line1 := strings.Repeat("A", 80) + "\n"
	line2Oversized := `{"jsonrpc":"2.0","id":42,"data":"` + strings.Repeat("B", 120) + "\"}\n"
	line3 := strings.Repeat("C", 50) + "\n"
	input := line1 + line2Oversized + line3

	r := bufio.NewReader(strings.NewReader(input))

	// 1. Read line 1
	got, tooLarge, reqID, err := readMCPLine(r, 100)
	if err != nil || tooLarge || reqID != nil || string(got) != strings.Repeat("A", 80) {
		t.Fatalf("line1 failed: got=%s tooLarge=%v reqID=%v err=%v", string(got), tooLarge, reqID, err)
	}

	// 2. Read oversized line 2: should return tooLarge=true, extract reqID=42, and drain until newline
	_, tooLarge, reqID, err = readMCPLine(r, 100)
	if err != nil || !tooLarge {
		t.Fatalf("line2 expected tooLarge=true, got tooLarge=%v err=%v", tooLarge, err)
	}
	if reqID != int64(42) {
		t.Fatalf("line2 expected reqID=42, got %v (%T)", reqID, reqID)
	}

	// 3. Read line 3: should recover cleanly without error
	got, tooLarge, reqID, err = readMCPLine(r, 100)
	if err != nil || tooLarge || reqID != nil || string(got) != strings.Repeat("C", 50) {
		t.Fatalf("line3 failed to recover after oversized line: got=%s tooLarge=%v reqID=%v err=%v", string(got), tooLarge, reqID, err)
	}
}

func TestMCPToolCallWithRealBot(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer raw.Close()

		var authMsg map[string]interface{}
		if err := raw.ReadJSON(&authMsg); err != nil {
			return
		}
		_ = raw.WriteJSON(map[string]interface{}{
			"type":       "auth_success",
			"session_id": "test-mcp-real",
		})

		for {
			var msg map[string]interface{}
			if err := raw.ReadJSON(&msg); err != nil {
				return
			}
			action, _ := msg["action"].(string)
			switch action {
			case "unfocus":
			case "ai_exec":
				cmd, _ := msg["command"].(string)
				_ = raw.WriteJSON(map[string]interface{}{
					"type":        "control",
					"action":      "ai_exec_result",
					"target_conn": "test-bot",
					"stdout":      "real-exec: " + cmd,
					"exit_code":   0,
				})
			case "ai_get_info":
				_ = raw.WriteJSON(map[string]interface{}{
					"type":        "control",
					"action":      "ai_get_info_result",
					"target_conn": "test-bot",
					"info": map[string]interface{}{
						"os":         "darwin",
						"arch":       "arm64",
						"allow_exec": true,
					},
				})
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	bot, err := newBotClient(wsURL, "test-mcp-real", "password123", "test-bot", false)
	if err != nil {
		t.Fatalf("newBotClient failed: %v", err)
	}
	defer bot.Close()

	tools := getMCPToolsList()

	// 1. Test MCP tools/call rmte_exec with real bot
	callParams, _ := json.Marshal(map[string]interface{}{
		"name": "rmte_exec",
		"arguments": map[string]interface{}{
			"command": "git status",
		},
	})
	resp := handleMCPRequest(mcpRequest{
		JSONRPC: "2.0",
		ID:      101,
		Method:  "tools/call",
		Params:  callParams,
	}, bot, tools)

	if resp == nil || resp.Error != nil {
		t.Fatalf("rmte_exec tools/call unexpected resp: %+v", resp)
	}
	resTool, ok := resp.Result.(mcpToolResult)
	if !ok || resTool.IsError || len(resTool.Content) == 0 {
		t.Fatalf("rmte_exec tools/call expected success, got: %+v", resp.Result)
	}
	if !strings.Contains(resTool.Content[0].Text, "real-exec: git status") {
		t.Fatalf("rmte_exec unexpected content: %s", resTool.Content[0].Text)
	}

	// 2. Test MCP tools/call rmte_get_info with real bot
	callInfoParams, _ := json.Marshal(map[string]interface{}{
		"name":      "rmte_get_info",
		"arguments": map[string]interface{}{},
	})
	respInfo := handleMCPRequest(mcpRequest{
		JSONRPC: "2.0",
		ID:      102,
		Method:  "tools/call",
		Params:  callInfoParams,
	}, bot, tools)

	if respInfo == nil || respInfo.Error != nil {
		t.Fatalf("rmte_get_info tools/call unexpected resp: %+v", respInfo)
	}
	resInfoTool, ok := respInfo.Result.(mcpToolResult)
	if !ok || resInfoTool.IsError || len(resInfoTool.Content) == 0 {
		t.Fatalf("rmte_get_info tools/call expected success, got: %+v", respInfo.Result)
	}
	if !strings.Contains(resInfoTool.Content[0].Text, "darwin") {
		t.Fatalf("rmte_get_info unexpected content: %s", resInfoTool.Content[0].Text)
	}
}
