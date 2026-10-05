package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
)

var (
	aesGCM           cipher.AEAD
	rawEncryptionKey []byte
)

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

func encryptBinary(tabID byte, plaintext []byte) ([]byte, error) {
	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	ciphertext := aesGCM.Seal(nil, nonce, plaintext, nil)

	payload := make([]byte, 1+len(nonce)+len(ciphertext))
	payload[0] = tabID
	copy(payload[1:], nonce)
	copy(payload[1+len(nonce):], ciphertext)

	return payload, nil
}

func decryptBinary(payload []byte) (byte, []byte, error) {
	if len(payload) < 1+12 {
		return 0, nil, io.ErrUnexpectedEOF
	}

	tabID := payload[0]
	nonce := payload[1:13]
	ciphertext := payload[13:]

	plaintext, err := aesGCM.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return 0, nil, err
	}

	return tabID, plaintext, nil
}

// encryptDataChannel frames a file-channel message as
// [dataChannelTabID][4-byte transfer id][12-byte nonce][ciphertext].
// The transfer id lets concurrent file operations from different viewers
// be told apart, since the relay broadcasts binary frames to every viewer.
func encryptDataChannel(transferID uint32, plaintext []byte) ([]byte, error) {
	payload, err := encryptBinary(dataChannelTabID, plaintext)
	if err != nil {
		return nil, err
	}

	out := make([]byte, len(payload)+4)
	out[0] = dataChannelTabID
	binary.BigEndian.PutUint32(out[1:5], transferID)
	copy(out[5:], payload[1:])

	return out, nil
}

func decryptDataChannel(payload []byte) (uint32, []byte, error) {
	if len(payload) < 1+4+12 {
		return 0, nil, io.ErrUnexpectedEOF
	}

	transferID := binary.BigEndian.Uint32(payload[1:5])
	nonce := payload[5:17]
	ciphertext := payload[17:]

	plaintext, err := aesGCM.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return 0, nil, err
	}

	return transferID, plaintext, nil
}

func generateAuthToken(password string) string {
	hash := sha256.Sum256([]byte("rmte-auth:" + password))
	return hex.EncodeToString(hash[:])
}
