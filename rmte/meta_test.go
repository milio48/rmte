package main

import (
	"bytes"
	"os"
	"reflect"
	"testing"
)

func TestLocalDataEncryptionRoundTrip(t *testing.T) {
	plaintext := []byte("secret-session-password-12345")

	encrypted, err := encryptLocalData(plaintext)
	if err != nil {
		t.Fatalf("encryptLocalData error: %v", err)
	}

	if bytes.Equal(encrypted, plaintext) {
		t.Fatal("ciphertext matches plaintext; not encrypted!")
	}

	decrypted, err := decryptLocalData(encrypted)
	if err != nil {
		t.Fatalf("decryptLocalData error: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted %q; want %q", decrypted, plaintext)
	}
}

func TestSaveAndLoadSessionMeta(t *testing.T) {
	testMeta := SessionMeta{
		SessionID:   "testunit01",
		Password:    "securePass!99",
		ServerRelay: "wss://my.rmte.biz.id/ws-rmte",
		Dir:         "/test/dir",
		Buffer:      2,
		Preview:     true,
		PID:         12345,
		Mode:        "share",
	}

	defer removeSessionMeta(testMeta.SessionID)

	if err := saveSessionMeta(testMeta); err != nil {
		t.Fatalf("saveSessionMeta error: %v", err)
	}

	// Verify file is indeed encrypted and not raw JSON
	rawBytes, err := os.ReadFile(metaFilename(testMeta.SessionID))
	if err != nil {
		t.Fatalf("ReadFile meta error: %v", err)
	}
	if bytes.Contains(rawBytes, []byte("securePass!99")) {
		t.Fatal("Password found in plaintext inside .meta file!")
	}

	loaded, err := loadSessionMeta(testMeta.SessionID)
	if err != nil {
		t.Fatalf("loadSessionMeta error: %v", err)
	}

	if !reflect.DeepEqual(*loaded, testMeta) {
		t.Fatalf("loaded meta %+v; want %+v", *loaded, testMeta)
	}
}
