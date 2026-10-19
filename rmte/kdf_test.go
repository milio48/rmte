package main

import (
	"encoding/hex"
	"testing"
)

// Known-answer vector computed independently with Python's hashlib/hmac.
// ui/app.js must produce the same values.
func TestDeriveSessionKeysKnownAnswer(t *testing.T) {
	k, err := deriveSessionKeys("correct horse battery", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"enc":     "5ed215a82e8f161d37fd79618cef88c9385eba062688e8133159aed930bafe90",
		"auth":    "7f22dd1c0576b4e51f3ad34277f1cfbaf3d68212f484b16da3601f539fbcb856",
		"preview": "802bd44f07c5e5de2e1eb67331f707467840f0a6589c295fbc16b8404a340ae7",
	}
	got := map[string]string{
		"enc":     hex.EncodeToString(k.enc),
		"auth":    k.authToken,
		"preview": hex.EncodeToString(k.preview),
	}
	for name := range want {
		if got[name] != want[name] {
			t.Errorf("%s subkey = %s, want %s", name, got[name], want[name])
		}
	}
}

// A restarted host derives exactly the same keys and relay token from the
// same id and password, so it can resume its session without stored state.
func TestSessionKeysSurviveRestart(t *testing.T) {
	first, err := deriveKeys("pw", "kantor", testKDFIterations)
	if err != nil {
		t.Fatal(err)
	}
	keyCacheMu.Lock()
	keyCache = map[string]*sessionKeys{} // simulate a new process
	keyCacheMu.Unlock()
	again, err := deriveKeys("pw", " Kantor ", testKDFIterations)
	if err != nil {
		t.Fatal(err)
	}
	if again.authToken != first.authToken || hex.EncodeToString(again.enc) != hex.EncodeToString(first.enc) {
		t.Fatal("same id and password must give the same keys (ids are case-insensitive, like the relay)")
	}
}

func TestSessionKeysBindPasswordAndSession(t *testing.T) {
	base, _ := deriveKeys("pw", "sess-a", testKDFIterations)
	if hex.EncodeToString(base.enc) == base.authToken || string(base.enc) == string(base.preview) {
		t.Fatal("subkeys must be independent")
	}
	otherSession, _ := deriveKeys("pw", "sess-b", testKDFIterations)
	otherPassword, _ := deriveKeys("pw2", "sess-a", testKDFIterations)
	if otherSession.authToken == base.authToken || otherPassword.authToken == base.authToken {
		t.Fatal("keys must depend on both the session id and the password")
	}
}
