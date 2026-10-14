package main

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"
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

	if resMap["protocolVersion"] != "2024-11-05" {
		t.Errorf("expected protocolVersion 2024-11-05, got %v", resMap["protocolVersion"])
	}

	sInfo, ok := resMap["serverInfo"].(map[string]interface{})
	if !ok || sInfo["name"] != "rmte-mcp" {
		t.Errorf("expected serverInfo.name rmte-mcp, got %v", sInfo)
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
