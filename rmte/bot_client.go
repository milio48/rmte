package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// FileInfoEntry represents a file or directory entry returned by ListFiles.
type FileInfoEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

type pendingTransfer struct {
	readyChan chan struct{}
	doneChan  chan struct{}
	dataChan  chan []byte
	errChan   chan error
}

// BotClient provides a secure, headless viewer client for AI agents (MCP & CLI exec).
type BotClient struct {
	conn       *SafeConn
	viewerID   string
	sessionID  string
	name       string
	nextXferID atomic.Uint32

	execMu sync.Mutex // serializes ExecCommand callers
	infoMu sync.Mutex // serializes GetInfo callers
	dirMu  sync.Mutex // serializes ListFiles callers

	respMu      sync.Mutex // guards pendingExec, pendingInfo, pendingDir
	pendingExec chan map[string]interface{}
	pendingInfo chan map[string]interface{}
	pendingDir  chan map[string]interface{}

	xferMu      sync.Mutex
	pendingXfer map[uint32]*pendingTransfer

	closed chan struct{}
}

func newBotClient(serverURL, sessionID, password, name string, insecure bool) (*BotClient, error) {
	if insecure {
		setupTLSConfig(true)
	}

	if err := setupCrypto(password); err != nil {
		return nil, fmt.Errorf("crypto setup failed: %w", err)
	}

	viewerID := generateViewerID()
	if name == "" {
		name = "rmte-bot"
	}

	conn, err := dialAndAuthViewer(serverURL, sessionID, password, viewerID, name)
	if err != nil {
		return nil, fmt.Errorf("relay auth failed: %w", err)
	}

	bot := &BotClient{
		conn:        conn,
		viewerID:    viewerID,
		sessionID:   sessionID,
		name:        name,
		pendingXfer: make(map[uint32]*pendingTransfer),
		closed:      make(chan struct{}),
	}

	// Announce bot presence immediately (so host marks bot and updates presence)
	_ = bot.conn.WriteJSON(map[string]interface{}{
		"type":        "control",
		"action":      "unfocus",
		"viewer_id":   viewerID,
		"viewer_name": name,
		"is_bot":      true,
	})

	go bot.readLoop()

	return bot, nil
}

func (b *BotClient) readLoop() {
	defer close(b.closed)
	for {
		mt, data, err := b.conn.ReadMessage()
		if err != nil {
			b.broadcastError(err)
			return
		}

		if mt == websocket.BinaryMessage {
			if len(data) > 0 && data[0] == dataChannelTabID {
				transferID, plaintext, err := decryptDataChannel(dirToViewer, data)
				if err == nil {
					b.xferMu.Lock()
					pt := b.pendingXfer[transferID]
					b.xferMu.Unlock()
					if pt != nil && pt.dataChan != nil {
						select {
						case pt.dataChan <- plaintext:
						default:
						}
					}
				}
			}
			continue
		}

		if mt == websocket.TextMessage {
			var msg map[string]interface{}
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}

			action, _ := msg["action"].(string)
			switch action {
			case "ai_exec_result":
				b.respMu.Lock()
				ch := b.pendingExec
				b.respMu.Unlock()
				if ch != nil {
					select {
					case ch <- msg:
					default:
					}
				}

			case "ai_get_info_result":
				b.respMu.Lock()
				ch := b.pendingInfo
				b.respMu.Unlock()
				if ch != nil {
					select {
					case ch <- msg:
					default:
					}
				}

			case "ready_for_data":
				tid := ctrlTransferID(msg)
				b.xferMu.Lock()
				pt := b.pendingXfer[tid]
				b.xferMu.Unlock()
				if pt != nil && pt.readyChan != nil {
					select {
					case pt.readyChan <- struct{}{}:
					default:
					}
				}

			case "file_saved":
				tid := ctrlTransferID(msg)
				b.xferMu.Lock()
				pt := b.pendingXfer[tid]
				b.xferMu.Unlock()
				if pt != nil && pt.doneChan != nil {
					select {
					case pt.doneChan <- struct{}{}:
					default:
					}
				}

			case "read_file_start":
				// Binary payload on Tab 255 follows shortly

			case "dir_data":
				b.respMu.Lock()
				ch := b.pendingDir
				b.respMu.Unlock()
				if ch != nil {
					select {
					case ch <- msg:
					default:
					}
				}

			case "fm_error":
				errMsg, _ := msg["message"].(string)
				if errMsg == "" {
					errMsg = "file operation error"
				}
				tid := ctrlTransferID(msg)
				if tid > 0 {
					b.xferMu.Lock()
					pt := b.pendingXfer[tid]
					b.xferMu.Unlock()
					if pt != nil && pt.errChan != nil {
						select {
						case pt.errChan <- fmt.Errorf("%s", errMsg):
						default:
						}
					}
				}
				// Also notify pending directory listing if active
				b.respMu.Lock()
				ch := b.pendingDir
				b.respMu.Unlock()
				if ch != nil {
					select {
					case ch <- msg:
					default:
					}
				}
			}
		}
	}
}

func (b *BotClient) broadcastError(err error) {
	b.respMu.Lock()
	pExec := b.pendingExec
	pInfo := b.pendingInfo
	pDir := b.pendingDir
	b.respMu.Unlock()

	if pExec != nil {
		select {
		case pExec <- map[string]interface{}{"error": err.Error(), "exit_code": float64(1)}:
		default:
		}
	}

	if pInfo != nil {
		select {
		case pInfo <- map[string]interface{}{"error": err.Error()}:
		default:
		}
	}

	if pDir != nil {
		select {
		case pDir <- map[string]interface{}{"action": "fm_error", "message": err.Error()}:
		default:
		}
	}

	b.xferMu.Lock()
	for _, pt := range b.pendingXfer {
		if pt.errChan != nil {
			select {
			case pt.errChan <- err:
			default:
			}
		}
	}
	b.xferMu.Unlock()
}

// ExecCommand executes a command on the remote host tab.
func (b *BotClient) ExecCommand(command string, timeoutSec int, background bool, tabName string) (string, int, error) {
	b.execMu.Lock()
	defer b.execMu.Unlock()

	ch := make(chan map[string]interface{}, 1)
	b.respMu.Lock()
	b.pendingExec = ch
	b.respMu.Unlock()

	defer func() {
		b.respMu.Lock()
		b.pendingExec = nil
		b.respMu.Unlock()
	}()

	msg := map[string]interface{}{
		"type":            "control",
		"action":          "ai_exec",
		"command":         command,
		"background":      background,
		"timeout_seconds": timeoutSec,
	}
	if tabName != "" {
		msg["tab_name"] = tabName
	}

	if err := b.conn.WriteJSON(msg); err != nil {
		return "", 1, fmt.Errorf("failed to send ai_exec: %w", err)
	}

	waitDuration := time.Duration(timeoutSec+15) * time.Second
	select {
	case res := <-ch:
		exitCode := 0
		if ec, ok := res["exit_code"].(float64); ok {
			exitCode = int(ec)
		}
		stdout, _ := res["stdout"].(string)
		errMsg, _ := res["error"].(string)
		if errMsg != "" && exitCode == 0 {
			exitCode = 1
		}
		if errMsg != "" && stdout == "" {
			return "", exitCode, fmt.Errorf("%s", errMsg)
		}
		if errMsg != "" {
			return fmt.Sprintf("%s\nError: %s", stdout, errMsg), exitCode, nil
		}
		return stdout, exitCode, nil

	case <-time.After(waitDuration):
		return "", 124, fmt.Errorf("ai_exec timed out waiting for host response after %v", waitDuration)
	case <-b.closed:
		return "", 1, fmt.Errorf("connection closed while waiting for ai_exec response")
	}
}

// GetInfo retrieves system and runtime information from the host.
func (b *BotClient) GetInfo() (map[string]interface{}, error) {
	b.infoMu.Lock()
	defer b.infoMu.Unlock()

	ch := make(chan map[string]interface{}, 1)
	b.respMu.Lock()
	b.pendingInfo = ch
	b.respMu.Unlock()

	defer func() {
		b.respMu.Lock()
		b.pendingInfo = nil
		b.respMu.Unlock()
	}()

	if err := b.conn.WriteJSON(map[string]interface{}{
		"type":   "control",
		"action": "ai_get_info",
	}); err != nil {
		return nil, fmt.Errorf("failed to send ai_get_info: %w", err)
	}

	select {
	case res := <-ch:
		info, _ := res["info"].(map[string]interface{})
		if info == nil {
			info = res
		}
		return info, nil
	case <-time.After(15 * time.Second):
		return nil, fmt.Errorf("ai_get_info timed out")
	case <-b.closed:
		return nil, fmt.Errorf("connection closed")
	}
}

// ReadFile fetches file contents from the remote host.
func (b *BotClient) ReadFile(remotePath string) ([]byte, error) {
	transferID := b.nextXferID.Add(1)
	pt := &pendingTransfer{
		dataChan: make(chan []byte, 1),
		errChan:  make(chan error, 1),
	}

	b.xferMu.Lock()
	b.pendingXfer[transferID] = pt
	b.xferMu.Unlock()

	defer func() {
		b.xferMu.Lock()
		delete(b.pendingXfer, transferID)
		b.xferMu.Unlock()
	}()

	if err := b.conn.WriteJSON(map[string]interface{}{
		"type":        "control",
		"action":      "req_read_file",
		"path":        remotePath,
		"transfer_id": transferID,
	}); err != nil {
		return nil, fmt.Errorf("failed to send req_read_file: %w", err)
	}

	select {
	case data := <-pt.dataChan:
		return data, nil
	case err := <-pt.errChan:
		return nil, err
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("req_read_file timed out waiting for data")
	case <-b.closed:
		return nil, fmt.Errorf("connection closed")
	}
}

// WriteFile writes content to a file on the remote host.
func (b *BotClient) WriteFile(remotePath string, data []byte) error {
	transferID := b.nextXferID.Add(1)
	pt := &pendingTransfer{
		readyChan: make(chan struct{}, 1),
		doneChan:  make(chan struct{}, 1),
		errChan:   make(chan error, 1),
	}

	b.xferMu.Lock()
	b.pendingXfer[transferID] = pt
	b.xferMu.Unlock()

	defer func() {
		b.xferMu.Lock()
		delete(b.pendingXfer, transferID)
		b.xferMu.Unlock()
	}()

	if err := b.conn.WriteJSON(map[string]interface{}{
		"type":        "control",
		"action":      "prepare_save",
		"path":        remotePath,
		"transfer_id": transferID,
	}); err != nil {
		return fmt.Errorf("failed to send prepare_save: %w", err)
	}

	select {
	case <-pt.readyChan:
	case err := <-pt.errChan:
		return err
	case <-time.After(15 * time.Second):
		return fmt.Errorf("prepare_save timed out waiting for host ready signal")
	case <-b.closed:
		return fmt.Errorf("connection closed")
	}

	payload, err := encryptDataChannel(dirToHost, transferID, data)
	if err != nil {
		return fmt.Errorf("failed to encrypt file payload: %w", err)
	}

	if err := b.conn.WriteMessage(websocket.BinaryMessage, payload); err != nil {
		return fmt.Errorf("failed to write data channel payload: %w", err)
	}

	select {
	case <-pt.doneChan:
		return nil
	case err := <-pt.errChan:
		return err
	case <-time.After(30 * time.Second):
		return fmt.Errorf("write timed out waiting for file_saved confirmation")
	case <-b.closed:
		return fmt.Errorf("connection closed")
	}
}

// ListFiles lists directory contents on the remote host.
func (b *BotClient) ListFiles(remotePath string) ([]FileInfoEntry, error) {
	b.dirMu.Lock()
	defer b.dirMu.Unlock()

	ch := make(chan map[string]interface{}, 1)
	b.respMu.Lock()
	b.pendingDir = ch
	b.respMu.Unlock()

	defer func() {
		b.respMu.Lock()
		b.pendingDir = nil
		b.respMu.Unlock()
	}()

	if err := b.conn.WriteJSON(map[string]interface{}{
		"type":   "control",
		"action": "req_dir",
		"path":   remotePath,
	}); err != nil {
		return nil, fmt.Errorf("failed to send req_dir: %w", err)
	}

	select {
	case res := <-ch:
		action, _ := res["action"].(string)
		if action == "fm_error" {
			errMsg, _ := res["message"].(string)
			return nil, fmt.Errorf("%s", errMsg)
		}
		rawFiles, _ := res["files"].([]interface{})
		var entries []FileInfoEntry
		for _, rf := range rawFiles {
			if fm, ok := rf.(map[string]interface{}); ok {
				name, _ := fm["name"].(string)
				isDir, _ := fm["is_dir"].(bool)
				sizeVal, _ := fm["size"].(float64)
				entries = append(entries, FileInfoEntry{
					Name:  name,
					IsDir: isDir,
					Size:  int64(sizeVal),
				})
			}
		}
		return entries, nil
	case <-time.After(15 * time.Second):
		return nil, fmt.Errorf("req_dir timed out")
	case <-b.closed:
		return nil, fmt.Errorf("connection closed")
	}
}

// UploadFile uploads a file from the local machine to the remote host.
func (b *BotClient) UploadFile(localPath, remotePath string) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return fmt.Errorf("cannot read local file %s: %w", localPath, err)
	}
	return b.WriteFile(remotePath, data)
}

// DownloadFile downloads a file from the remote host to the local machine.
func (b *BotClient) DownloadFile(remotePath, localPath string) error {
	data, err := b.ReadFile(remotePath)
	if err != nil {
		return fmt.Errorf("cannot read remote file %s: %w", remotePath, err)
	}
	dir := filepath.Dir(localPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("cannot create local directory %s: %w", dir, err)
	}
	if err := os.WriteFile(localPath, data, 0644); err != nil {
		return fmt.Errorf("cannot write local file %s: %w", localPath, err)
	}
	return nil
}

// Close closes the underlying WebSocket connection.
func (b *BotClient) Close() error {
	if b.conn != nil {
		return b.conn.Close()
	}
	return nil
}
