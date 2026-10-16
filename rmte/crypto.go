package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
)

var (
	aesGCM           cipher.AEAD
	rawEncryptionKey []byte
)

// Frame direction, bound into the AEAD additional data so the relay cannot
// reflect a host->viewer frame back to the host as input (or vice versa).
const (
	dirToHost   byte = 1
	dirToViewer byte = 2
)

// frameAADPrefix versions the additional-data layout. Must match ui/app.js.
const frameAADPrefix = "rmte-v6"

// frameAAD binds direction, tab id and transfer id to the ciphertext so the
// relay cannot rewrite the cleartext frame header (e.g. redirect keystrokes
// to another tab, or re-wrap a terminal frame as a file-channel frame).
// Terminal frames use transferID 0.
func frameAAD(dir, tabID byte, transferID uint32) []byte {
	aad := make([]byte, len(frameAADPrefix)+2+4)
	n := copy(aad, frameAADPrefix)
	aad[n] = dir
	aad[n+1] = tabID
	binary.BigEndian.PutUint32(aad[n+2:], transferID)
	return aad
}

func setupCrypto(password string) error {
	key := sha256.Sum256([]byte(password))
	rawEncryptionKey = make([]byte, len(key))
	copy(rawEncryptionKey, key[:])
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return err
	}
	aesGCM, err = cipher.NewGCM(block)
	if err != nil {
		return err
	}
	return nil
}

// encryptBinary frames a terminal message as [tabID][12-byte nonce][ciphertext].
func encryptBinary(dir, tabID byte, plaintext []byte) ([]byte, error) {
	if aesGCM == nil {
		return nil, errors.New("crypto not initialized")
	}
	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	ciphertext := aesGCM.Seal(nil, nonce, plaintext, frameAAD(dir, tabID, 0))

	payload := make([]byte, 1+len(nonce)+len(ciphertext))
	payload[0] = tabID
	copy(payload[1:], nonce)
	copy(payload[1+len(nonce):], ciphertext)

	return payload, nil
}

// decryptBinary opens a terminal frame that was sent in direction dir.
func decryptBinary(dir byte, payload []byte) (byte, []byte, error) {
	if aesGCM == nil {
		return 0, nil, errors.New("crypto not initialized")
	}
	if len(payload) < 1+12 {
		return 0, nil, io.ErrUnexpectedEOF
	}

	tabID := payload[0]
	nonce := payload[1:13]
	ciphertext := payload[13:]

	plaintext, err := aesGCM.Open(nil, nonce, ciphertext, frameAAD(dir, tabID, 0))
	if err != nil {
		return 0, nil, err
	}

	return tabID, plaintext, nil
}

// encryptDataChannel frames a file-channel message as
// [dataChannelTabID][4-byte transfer id][12-byte nonce][ciphertext].
// The transfer id lets concurrent file operations from different viewers
// be told apart, since the relay broadcasts binary frames to every viewer.
func encryptDataChannel(dir byte, transferID uint32, plaintext []byte) ([]byte, error) {
	if aesGCM == nil {
		return nil, errors.New("crypto not initialized")
	}
	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	ciphertext := aesGCM.Seal(nil, nonce, plaintext, frameAAD(dir, dataChannelTabID, transferID))

	out := make([]byte, 1+4+len(nonce)+len(ciphertext))
	out[0] = dataChannelTabID
	binary.BigEndian.PutUint32(out[1:5], transferID)
	copy(out[5:], nonce)
	copy(out[5+len(nonce):], ciphertext)

	return out, nil
}

// decryptDataChannel opens a file-channel frame that was sent in direction dir.
func decryptDataChannel(dir byte, payload []byte) (uint32, []byte, error) {
	if aesGCM == nil {
		return 0, nil, errors.New("crypto not initialized")
	}
	if len(payload) < 1+4+12 {
		return 0, nil, io.ErrUnexpectedEOF
	}

	transferID := binary.BigEndian.Uint32(payload[1:5])
	nonce := payload[5:17]
	ciphertext := payload[17:]

	plaintext, err := aesGCM.Open(nil, nonce, ciphertext, frameAAD(dir, dataChannelTabID, transferID))
	if err != nil {
		return 0, nil, err
	}

	return transferID, plaintext, nil
}

func generateAuthToken(password string) string {
	hash := sha256.Sum256([]byte("rmte-auth:" + password))
	return hex.EncodeToString(hash[:])
}
