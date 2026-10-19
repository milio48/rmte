package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

var (
	aesGCM cipher.AEAD
	// previewKey authenticates Web Preview tickets and cookies (HKDF subkey).
	previewKey []byte
)

// Password key derivation.
//
// master  = PBKDF2-HMAC-SHA256(password, salt = "rmte-kdf-v1:" + session id, 600000, 32)
// enc     = HKDF-SHA256(master, info "rmte enc v1")      AES-256-GCM key
// auth    = HKDF-SHA256(master, info "rmte auth v1")     token the relay checks
// preview = HKDF-SHA256(master, info "rmte preview v1")  preview ticket/cookie MAC key
//
// The salt is the (lower-cased) session id, so nothing has to be stored or
// exchanged: the same id and password always give the same keys, which lets
// a host resume its session after a restart or reboot. The salt is not secret;
// it makes each session's derivation different, and the iteration count, fixed
// in code so a relay cannot lower it, makes every password guess expensive.
// The relay only ever sees the auth subkey. Must match ui/app.js.
const kdfIterations = 600000

type sessionKeys struct {
	enc       []byte
	authToken string
	preview   []byte
}

// normalizeSessionID matches how the relay canonicalises session ids.
func normalizeSessionID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

var (
	keyCacheMu sync.Mutex
	keyCache   = map[string]*sessionKeys{}
)

// deriveSessionKeys derives the session keys for password and sessionID
// (cached, since reconnects reuse them).
func deriveSessionKeys(password, sessionID string) (*sessionKeys, error) {
	return deriveKeys(password, sessionID, kdfIterations)
}

func deriveKeys(password, sessionID string, iterations int) (*sessionKeys, error) {
	sessionID = normalizeSessionID(sessionID)
	cacheKey := fmt.Sprintf("%x|%s|%d", sha256.Sum256([]byte(password)), sessionID, iterations)
	keyCacheMu.Lock()
	defer keyCacheMu.Unlock()
	if k, ok := keyCache[cacheKey]; ok {
		return k, nil
	}

	master, err := pbkdf2.Key(sha256.New, password, []byte("rmte-kdf-v1:"+sessionID), iterations, 32)
	if err != nil {
		return nil, err
	}
	sub := func(info string) ([]byte, error) { return hkdf.Key(sha256.New, master, nil, info, 32) }
	k := &sessionKeys{}
	if k.enc, err = sub("rmte enc v1"); err != nil {
		return nil, err
	}
	auth, err := sub("rmte auth v1")
	if err != nil {
		return nil, err
	}
	k.authToken = hex.EncodeToString(auth)
	if k.preview, err = sub("rmte preview v1"); err != nil {
		return nil, err
	}
	keyCache[cacheKey] = k
	return k, nil
}

// installSessionKeys makes k the process-wide session keys.
func installSessionKeys(k *sessionKeys) error {
	block, err := aes.NewCipher(k.enc)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	aesGCM = gcm
	previewKey = append([]byte(nil), k.preview...)
	return nil
}

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
