package main

import (
	"bytes"
	"testing"
)

func TestTerminalFrameRoundTrip(t *testing.T) {
	if err := setupCrypto("test-password"); err != nil {
		t.Fatal(err)
	}

	payload, err := encryptBinary(dirToHost, 3, []byte("hello terminal"))
	if err != nil {
		t.Fatal(err)
	}

	tabID, plaintext, err := decryptBinary(dirToHost, payload)
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

	payload, err := encryptDataChannel(dirToHost, wantID, want)
	if err != nil {
		t.Fatal(err)
	}
	if payload[0] != dataChannelTabID {
		t.Fatalf("first byte = %d, want dataChannelTabID (%d)", payload[0], dataChannelTabID)
	}

	transferID, plaintext, err := decryptDataChannel(dirToHost, payload)
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
	if _, _, err := decryptBinary(dirToHost, payload); err == nil {
		t.Fatal("decryptBinary unexpectedly accepted a data-channel frame")
	}
}

// The relay sees the cleartext frame header, so every header field it could
// rewrite must be bound to the ciphertext via AAD.
func TestFrameHeaderTamperingRejected(t *testing.T) {
	if err := setupCrypto("test-password"); err != nil {
		t.Fatal(err)
	}

	term, err := encryptBinary(dirToHost, 3, []byte("rm -rf ~\r"))
	if err != nil {
		t.Fatal(err)
	}

	redirected := append([]byte(nil), term...)
	redirected[0] = 4
	if _, _, err := decryptBinary(dirToHost, redirected); err == nil {
		t.Error("terminal frame redirected to another tab was accepted")
	}

	output, err := encryptBinary(dirToViewer, 3, []byte("host output"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := decryptBinary(dirToHost, output); err == nil {
		t.Error("host output reflected back to host was accepted as input")
	}

	// Re-wrap a terminal frame's nonce+ciphertext as a data-channel frame.
	rewrapped := make([]byte, 0, len(term)+4)
	rewrapped = append(rewrapped, dataChannelTabID, 0, 0, 0, 7)
	rewrapped = append(rewrapped, term[1:]...)
	if _, _, err := decryptDataChannel(dirToHost, rewrapped); err == nil {
		t.Error("terminal frame re-wrapped as data-channel frame was accepted")
	}

	file, err := encryptDataChannel(dirToViewer, 7, []byte("file contents"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := decryptDataChannel(dirToHost, file); err == nil {
		t.Error("downloaded file reflected back to host as upload was accepted")
	}

	retargeted := append([]byte(nil), file...)
	retargeted[4] = 8
	if _, _, err := decryptDataChannel(dirToViewer, retargeted); err == nil {
		t.Error("data-channel frame with rewritten transfer id was accepted")
	}
}
