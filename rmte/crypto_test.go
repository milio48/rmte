package main

import (
	"bytes"
	"testing"
)

func TestTerminalFrameRoundTrip(t *testing.T) {
	if err := setupCrypto("test-password"); err != nil {
		t.Fatal(err)
	}

	payload, err := encryptBinary(3, []byte("hello terminal"))
	if err != nil {
		t.Fatal(err)
	}

	tabID, plaintext, err := decryptBinary(payload)
	if err != nil {
		t.Fatal(err)
	}
	if tabID != 3 {
		t.Fatalf("tab id mismatch: got %d want 3", tabID)
	}
	if !bytes.Equal(plaintext, []byte("hello terminal")) {
		t.Fatalf("plaintext mismatch: %q", plaintext)
	}
}

func TestDataChannelFrameRoundTrip(t *testing.T) {
	if err := setupCrypto("test-password"); err != nil {
		t.Fatal(err)
	}

	const wantID uint32 = 0xDEADBEEF
	want := []byte("file bytes on tab 255")

	payload, err := encryptDataChannel(wantID, want)
	if err != nil {
		t.Fatal(err)
	}
	if payload[0] != dataChannelTabID {
		t.Fatalf("first byte = %d, want dataChannelTabID (%d)", payload[0], dataChannelTabID)
	}

	transferID, plaintext, err := decryptDataChannel(payload)
	if err != nil {
		t.Fatal(err)
	}
	if transferID != wantID {
		t.Fatalf("transfer id mismatch: got %d want %d", transferID, wantID)
	}
	if !bytes.Equal(plaintext, want) {
		t.Fatalf("plaintext mismatch: %q", plaintext)
	}

	// The plain binary decoder must not accept data-channel framing, otherwise a
	// viewer would misparse file bytes as terminal output.
	if _, _, err := decryptBinary(payload); err == nil {
		t.Fatal("decryptBinary unexpectedly accepted a data-channel frame")
	}
}
