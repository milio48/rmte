package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// SessionMeta holds runtime parameters of an active RMTE host session.
type SessionMeta struct {
	SessionID   string `json:"session_id"`
	Password    string `json:"password"`
	ServerRelay string `json:"server_relay,omitempty"`
	Dir         string `json:"dir,omitempty"`
	Buffer      int    `json:"buffer,omitempty"`
	Preview     bool   `json:"preview"`
	PID         int    `json:"pid"`
	Mode        string `json:"mode"`
	Port        int    `json:"port,omitempty"`
	Public      bool   `json:"public,omitempty"`
	PublicURL   string `json:"public_url,omitempty"`
	WebPath     string `json:"web_path,omitempty"`
	WSPath      string `json:"ws_path,omitempty"`
	AdminPath   string `json:"admin_path,omitempty"`
	AdminPass   string `json:"admin_pass,omitempty"`
}

// getLocalSecretKey derives an AES-256 key bound to the local machine, user, and binary path.
func getLocalSecretKey() []byte {
	hostname, _ := os.Hostname()
	homeDir, _ := os.UserHomeDir()
	exePath, _ := os.Executable()
	salt := "rmte-local-session-salt-v1"

	h := sha256.New()
	h.Write([]byte(hostname + "|" + homeDir + "|" + exePath + "|" + salt))
	return h.Sum(nil)
}

// encryptLocalData encrypts bytes using AES-256-GCM with a random nonce.
func encryptLocalData(plaintext []byte) ([]byte, error) {
	key := getLocalSecretKey()
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
	return append(nonce, ciphertext...), nil
}

// decryptLocalData decrypts bytes using AES-256-GCM.
func decryptLocalData(data []byte) ([]byte, error) {
	key := getLocalSecretKey()
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, fmt.Errorf("metadata ciphertext too short")
	}
	nonce := data[:nonceSize]
	ciphertext := data[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}

func metaFilename(sessionID string) string {
	return fmt.Sprintf("rmte-%s.meta", sessionID)
}

// saveSessionMeta encrypts and writes session parameters to a restricted-permission file.
func saveSessionMeta(meta SessionMeta) error {
	if meta.SessionID == "" {
		return nil
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	encrypted, err := encryptLocalData(data)
	if err != nil {
		return err
	}
	return os.WriteFile(metaFilename(meta.SessionID), encrypted, 0600)
}

// loadSessionMeta reads and decrypts session parameters for a given session ID.
func loadSessionMeta(sessionID string) (*SessionMeta, error) {
	filename := metaFilename(sessionID)
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	decrypted, err := decryptLocalData(data)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt session metadata (possibly different machine/user): %w", err)
	}
	var meta SessionMeta
	if err := json.Unmarshal(decrypted, &meta); err != nil {
		return nil, fmt.Errorf("corrupted session metadata JSON: %w", err)
	}
	return &meta, nil
}

// removeSessionMeta removes the encrypted metadata file.
func removeSessionMeta(sessionID string) {
	if sessionID == "" {
		return
	}
	_ = os.Remove(metaFilename(sessionID))
}
