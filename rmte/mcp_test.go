package main

import (
	"encoding/json"
	"io"
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

func TestPerMessageLimitReader(t *testing.T) {
	// 100 bytes limit per message
	// Total input is 240 bytes across 3 messages (80 bytes each)
	msg1 := strings.Repeat("A", 80)
	msg2 := strings.Repeat("B", 80)
	msg3 := strings.Repeat("C", 80)
	input := msg1 + msg2 + msg3

	r := strings.NewReader(input)
	limiter := newPerMessageLimitReader(r, 100)

	buf := make([]byte, len(msg1))
	// Read msg1
	n, err := io.ReadFull(limiter, buf)
	if err != nil || n != len(msg1) {
		t.Fatalf("msg1 read failed: n=%d err=%v", n, err)
	}
	limiter.Reset()

	// Read msg2 (total read is now 160 bytes, which exceeds 100 limit, but resets per message!)
	n, err = io.ReadFull(limiter, buf)
	if err != nil || n != len(msg2) {
		t.Fatalf("msg2 read failed: n=%d err=%v", n, err)
	}
	limiter.Reset()

	// Read msg3 (total read is now 240 bytes)
	n, err = io.ReadFull(limiter, buf)
	if err != nil || n != len(msg3) {
		t.Fatalf("msg3 read failed: n=%d err=%v", n, err)
	}

	// Test an oversized single message exceeding limit (150 bytes > 100 bytes limit)
	bigMsg := strings.Repeat("X", 150)
	bigLimiter := newPerMessageLimitReader(strings.NewReader(bigMsg), 100)
	sink := make([]byte, 150)
	_, err = io.ReadFull(bigLimiter, sink)
	if err == nil {
		t.Fatalf("expected error for oversized message, got nil")
	}
}
