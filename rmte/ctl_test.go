package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Security regression tests for the control envelope. The threat model is a
// malicious relay: it sees and can rewrite, drop, replay or inject any frame,
// but does not know the session password.

func ctlTestPair(t *testing.T, sessionID string) (host, viewer *ctlChannel) {
	t.Helper()
	if err := setupCrypto("ctl-test-password"); err != nil {
		t.Fatal(err)
	}
	host = newCtlChannel(aesGCM, sessionID, true)
	viewer = newCtlChannel(aesGCM, sessionID, false)
	viewer.epoch = host.epoch
	return host, viewer
}

func sealOuter(t *testing.T, c *ctlChannel, v interface{}) *ctlOuter {
	t.Helper()
	raw, err := c.seal(v)
	if err != nil || raw == nil {
		t.Fatalf("seal failed: %v (raw=%v)", err, raw)
	}
	var outer ctlOuter
	if err := json.Unmarshal(raw, &outer); err != nil {
		t.Fatal(err)
	}
	return &outer
}

func TestCtlEnvelopeHidesPayloadFromRelay(t *testing.T) {
	_, viewer := ctlTestPair(t, "sess")
	raw, err := viewer.seal(map[string]interface{}{"type": "control", "action": "ai_exec", "command": "cat /etc/shadow"})
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"ai_exec", "shadow", "command"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("envelope leaks %q to the relay: %s", leak, raw)
		}
	}
}

func TestCtlHostRejectsPlaintextCommands(t *testing.T) {
	ctlTestPair(t, "sess")
	conn := &SafeConn{ctl: newCtlChannel(aesGCM, "sess", true)}

	for _, forged := range []string{
		`{"type":"control","action":"ai_exec","command":"id","target_conn":"c-1"}`,
		`{"type":"control","action":"delete_file","path":"/home/u/.ssh","target_conn":"c-1"}`,
		`{"type":"control","action":"rename_file","old_path":"a","new_path":"b"}`,
		`{"type":"control","action":"prepare_save","path":"/home/u/.bashrc","transfer_id":1}`,
		`{"type":"control","action":"req_dir","path":"/"}`,
		`{"type":"control","action":"chat","message":"hi"}`,
	} {
		if msg, ok := hostAcceptControl(conn, []byte(forged)); ok {
			t.Errorf("host accepted forged plaintext control message: %v", msg)
		}
	}

	if _, ok := hostAcceptControl(conn, []byte(`{"type":"control","action":"viewer_disconnected","viewer_id":"v"}`)); !ok {
		t.Error("host should still accept the relay's own viewer_disconnected notice")
	}
}

func TestCtlRoundTripAndRelayStampedRoute(t *testing.T) {
	host, viewer := ctlTestPair(t, "sess")
	outer := sealOuter(t, viewer, map[string]interface{}{"type": "control", "action": "req_dir", "path": "/tmp"})
	if outer.TargetConn != "" {
		t.Fatalf("viewer must not choose a route, got %q", outer.TargetConn)
	}
	msg, _, err := host.open(outer)
	if err != nil {
		t.Fatal(err)
	}
	if msg["action"] != "req_dir" || msg["path"] != "/tmp" {
		t.Fatalf("unexpected message: %v", msg)
	}

	reply := sealOuter(t, host, map[string]interface{}{"type": "control", "action": "dir_data", "target_conn": "c-42"})
	if reply.TargetConn != "c-42" {
		t.Fatalf("host reply must expose its route to the relay, got %q", reply.TargetConn)
	}
	if _, _, err := viewer.open(reply); err != nil {
		t.Fatal(err)
	}
}

func TestCtlRejectsReplay(t *testing.T) {
	host, viewer := ctlTestPair(t, "sess")
	outer := sealOuter(t, viewer, map[string]interface{}{"type": "control", "action": "delete_file", "path": "x"})
	if _, _, err := host.open(outer); err != nil {
		t.Fatal(err)
	}
	if _, _, err := host.open(outer); !errors.Is(err, errCtlReplay) {
		t.Fatalf("replayed delete_file: got %v, want errCtlReplay", err)
	}

	// Out-of-order delivery of an older message is a replay too.
	first := sealOuter(t, viewer, map[string]interface{}{"type": "control", "action": "get_tabs"})
	second := sealOuter(t, viewer, map[string]interface{}{"type": "control", "action": "get_tabs"})
	if _, _, err := host.open(second); err != nil {
		t.Fatal(err)
	}
	if _, _, err := host.open(first); !errors.Is(err, errCtlReplay) {
		t.Fatalf("older message after newer: got %v, want errCtlReplay", err)
	}

	// Host -> viewer replay (e.g. an old ai_exec_result) is rejected as well.
	res := sealOuter(t, host, map[string]interface{}{"type": "control", "action": "ai_exec_result", "stdout": "ok"})
	if _, _, err := viewer.open(res); err != nil {
		t.Fatal(err)
	}
	if _, _, err := viewer.open(res); !errors.Is(err, errCtlReplay) {
		t.Fatalf("replayed host message: got %v, want errCtlReplay", err)
	}
}

func TestCtlRejectsMessagesFromPreviousHostProcess(t *testing.T) {
	oldHost, viewer := ctlTestPair(t, "sess")
	captured := sealOuter(t, viewer, map[string]interface{}{"type": "control", "action": "ai_exec", "command": "rm -rf build"})
	if _, _, err := oldHost.open(captured); err != nil {
		t.Fatal(err)
	}

	// Host restarts with the same password: the captured command must not run again.
	newHost := newCtlChannel(aesGCM, "sess", true)
	if _, _, err := newHost.open(captured); !errors.Is(err, errCtlStaleEpoch) {
		t.Fatalf("cross-process replay: got %v, want errCtlStaleEpoch", err)
	}

	// ctl_hello is the only message accepted without the current epoch.
	stale := newCtlChannel(aesGCM, "sess", false)
	stale.epoch = oldHost.epoch
	hello := sealOuter(t, stale, map[string]interface{}{"type": "control", "action": ctlHelloAction})
	if _, _, err := newHost.open(hello); err != nil {
		t.Fatalf("ctl_hello with stale epoch should be accepted: %v", err)
	}
}

func TestCtlRejectsTamperingReflectionAndCrossSession(t *testing.T) {
	host, viewer := ctlTestPair(t, "sess")

	// Bit flip in the ciphertext.
	outer := sealOuter(t, viewer, map[string]interface{}{"type": "control", "action": "get_tabs"})
	raw, _ := base64.StdEncoding.DecodeString(outer.Data)
	raw[len(raw)-1] ^= 1
	tampered := &ctlOuter{Type: ctlEnvelopeType, Data: base64.StdEncoding.EncodeToString(raw)}
	if _, _, err := host.open(tampered); !errors.Is(err, errCtlDecrypt) {
		t.Fatalf("tampered envelope: got %v, want errCtlDecrypt", err)
	}

	// Reflection: a viewer's own message bounced back at viewers.
	other := newCtlChannel(aesGCM, "sess", false)
	reflected := sealOuter(t, viewer, map[string]interface{}{"type": "control", "action": "chat", "message": "x"})
	if _, _, err := other.open(reflected); !errors.Is(err, errCtlDecrypt) {
		t.Fatalf("reflected viewer message: got %v, want errCtlDecrypt", err)
	}

	// Same password, different session id.
	otherSessionHost := newCtlChannel(aesGCM, "other", true)
	cross := sealOuter(t, viewer, map[string]interface{}{"type": "control", "action": "get_tabs"})
	if _, _, err := otherSessionHost.open(cross); !errors.Is(err, errCtlDecrypt) {
		t.Fatalf("cross-session envelope: got %v, want errCtlDecrypt", err)
	}

	// Wrong password.
	if err := setupCrypto("not-the-password"); err != nil {
		t.Fatal(err)
	}
	attacker := newCtlChannel(aesGCM, "sess", false)
	attacker.epoch = host.epoch
	forged := sealOuter(t, attacker, map[string]interface{}{"type": "control", "action": "ai_exec", "command": "id"})
	if _, _, err := host.open(forged); !errors.Is(err, errCtlDecrypt) {
		t.Fatalf("envelope under wrong key: got %v, want errCtlDecrypt", err)
	}
}

func TestCtlViewerQueuesUntilEpochKnown(t *testing.T) {
	host, _ := ctlTestPair(t, "sess")
	viewer := newCtlChannel(aesGCM, "sess", false)

	if raw, err := viewer.seal(map[string]interface{}{"type": "control", "action": "get_tabs"}); err != nil || raw != nil {
		t.Fatalf("message before epoch should be queued, got raw=%s err=%v", raw, err)
	}
	if raw, err := viewer.seal(map[string]interface{}{"type": "control", "action": ctlHelloAction}); err != nil || raw == nil {
		t.Fatalf("ctl_hello must be sent immediately, got raw=%v err=%v", raw, err)
	}

	hello := sealOuter(t, host, map[string]interface{}{"type": "control", "action": ctlHelloAction})
	_, flush, err := viewer.open(hello)
	if err != nil {
		t.Fatal(err)
	}
	if len(flush) != 1 || viewer.epoch != host.epoch {
		t.Fatalf("expected one queued message flushed and epoch learned, got %d / %q", len(flush), viewer.epoch)
	}
}

// End to end through the real relay: plaintext viewer control never reaches
// the host, while sealed messages do, with the relay-stamped route.
func TestRelayForwardsOnlySealedViewerControl(t *testing.T) {
	origCfg := serverCfg
	t.Cleanup(func() {
		serverCfg = origCfg
		sessionMu.Lock()
		sessions = make(map[string]*Session)
		sessionMu.Unlock()
	})
	serverCfg = &ServeConfig{Mode: modeHybrid, WebPath: "/", WSPath: "/ws-rmte"}
	sessionMu.Lock()
	sessions = make(map[string]*Session)
	sessionMu.Unlock()

	srv := httptest.NewServer(http.HandlerFunc(handleWS))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	token := generateAuthToken("relaytest")
	hostRaw, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer hostRaw.Close()
	_ = hostRaw.WriteJSON(map[string]interface{}{"type": "auth", "role": "host", "session_id": "sealbox", "auth_token": token})
	var hResp map[string]interface{}
	if err := hostRaw.ReadJSON(&hResp); err != nil || hResp["type"] != "auth_success" {
		t.Fatalf("host auth: %v %v", err, hResp)
	}

	viewerRaw, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer viewerRaw.Close()
	_ = viewerRaw.WriteJSON(map[string]interface{}{"type": "auth", "role": "viewer", "session_id": "sealbox", "viewer_id": "v", "auth_token": token})
	var vResp map[string]interface{}
	if err := viewerRaw.ReadJSON(&vResp); err != nil || vResp["type"] != "auth_success" {
		t.Fatalf("viewer auth: %v %v", err, vResp)
	}

	if err := setupCrypto("relaytest"); err != nil {
		t.Fatal(err)
	}
	host := sealPeer(hostRaw, "sealbox", true)
	viewer := sealPeer(viewerRaw, "sealbox", false)
	linkEpoch(host, viewer)

	_ = viewerRaw.WriteJSON(map[string]interface{}{"type": "control", "action": "delete_file", "path": "/etc/passwd"})
	_ = viewerRaw.WriteJSON(map[string]interface{}{"type": "control", "action": "ai_exec", "command": "id"})
	_ = viewer.WriteJSON(map[string]interface{}{"type": "control", "action": "req_dir", "path": "/srv"})

	msg, err := readSealed(host, 2*time.Second)
	if err != nil {
		t.Fatalf("host did not receive sealed message: %v", err)
	}
	if msg["action"] != "req_dir" {
		t.Fatalf("host received %v before the sealed req_dir; plaintext leaked through the relay", msg)
	}
	if msg["target_conn"] != vResp["conn_id"] {
		t.Fatalf("relay should stamp sender route %v, got %v", vResp["conn_id"], msg["target_conn"])
	}
}
