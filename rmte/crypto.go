package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"io"
)

var aesGCM cipher.AEAD

func setupCrypto(password string) error {
	key := sha256.Sum256([]byte(password))
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
