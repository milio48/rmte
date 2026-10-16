package main

import (
	"os"
	"reflect"
	"testing"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		input    string
		expected []int
	}{
		{"0.5.0", []int{0, 5, 0}},
		{"v0.5.0", []int{0, 5, 0}},
		{"1.0.0-rc1", []int{1, 0, 0}},
		{"1.2", []int{1, 2}},
		{"v2.10.4+build123", []int{2, 10, 4}},
	}

	for _, tc := range tests {
		got := parseVersion(tc.input)
		if !reflect.DeepEqual(got, tc.expected) {
			t.Errorf("parseVersion(%q) = %v; want %v", tc.input, got, tc.expected)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		v1       string
		v2       string
		expected int
	}{
		{"0.5.0", "0.5.0", 0},
		{"0.5.0", "0.5.1", -1},
		{"0.5.1", "0.5.0", 1},
		{"v0.5.0", "0.5.0", 0},
		{"0.4.9", "0.5.0", -1},
		{"1.0.0", "0.9.9", 1},
		{"0.5.0", "0.5.0.1", -1},
		{"dev", "0.5.0", -1},
		{"0.5.0", "dev", 1},
	}

	for _, tc := range tests {
		got := compareVersions(tc.v1, tc.v2)
		if got != tc.expected {
			t.Errorf("compareVersions(%q, %q) = %d; want %d", tc.v1, tc.v2, got, tc.expected)
		}
	}
}

func TestIsPidAlive(t *testing.T) {
	if !isPidAlive(os.Getpid()) {
		t.Errorf("expected current pid %d to be alive", os.Getpid())
	}
	if isPidAlive(99999999) {
		t.Errorf("expected non-existent pid to not be alive")
	}
	if isPidAlive(-1) {
		t.Errorf("expected negative pid to not be alive")
	}
}

func TestIsPidRMTE(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("failed to get current executable: %v", err)
	}
	if !isPidRMTE(os.Getpid(), exe) {
		t.Errorf("expected current rmte process %d with exe %q to match rmte", os.Getpid(), exe)
	}
	if isPidRMTE(99999999, exe) {
		t.Errorf("expected non-existent pid to not match rmte")
	}
	if isPidRMTE(-1, exe) {
		t.Errorf("expected negative pid to not match rmte")
	}
}

func TestBuildRestartArgs(t *testing.T) {
	// Case 1: nil meta returns nil
	if args := buildRestartArgs("test", nil); args != nil {
		t.Errorf("expected nil args for nil meta, got %v", args)
	}

	// Case 2: hybrid serve session
	metaHybrid := &SessionMeta{
		Mode:      modeHybrid,
		Password:  "mysecret",
		Port:      2638,
		Public:    true,
		PublicURL: "my.rmte.biz.id",
		Preview:   true,
		AdminPass: "admin123",
		AdminPath: "/myadmin",
	}
	args := buildRestartArgs("rmtepusat", metaHybrid)
	expectedPrefix := []string{"serve", "--mode=hybrid", "--id=rmtepusat", "-q", "--pass=mysecret"}
	for i, exp := range expectedPrefix {
		if i >= len(args) || args[i] != exp {
			t.Errorf("args[%d] = %v; want %v", i, args[i], exp)
		}
	}

	// Case 3a: relay mode without meta (nil-meta fallback)
	argsRelayNil := buildRestartArgs("relay", nil)
	if !reflect.DeepEqual(argsRelayNil, []string{"serve", "--mode=relay", "-q"}) {
		t.Errorf("argsRelayNil = %v; want [serve --mode=relay -q]", argsRelayNil)
	}

	// Case 3b: relay mode with custom meta
	metaRelay := &SessionMeta{
		Mode:      modeRelay,
		Port:      9000,
		Public:    true,
		AdminPass: "relaypass",
		AdminPath: "/relayadmin",
	}
	argsRelayMeta := buildRestartArgs("relay", metaRelay)
	expectedRelayMeta := []string{"serve", "--mode=relay", "-q", "--port=9000", "--public", "--admin-path=/relayadmin", "--admin-pass=relaypass"}
	if !reflect.DeepEqual(argsRelayMeta, expectedRelayMeta) {
		t.Errorf("argsRelayMeta = %v; want %v", argsRelayMeta, expectedRelayMeta)
	}

	// Case 4: share session
	metaShare := &SessionMeta{
		Mode:        modeShare,
		Password:    "sharepwd",
		ServerRelay: "wss://my.rmte.biz.id/ws-rmte",
		Dir:         "/myworkspace",
	}
	argsShare := buildRestartArgs("share123", metaShare)
	expectedShare := []string{"--id=share123", "-q", "--pass=sharepwd", "--server-relay=wss://my.rmte.biz.id/ws-rmte", "--dir=/myworkspace", "--web-preview=false"}
	if !reflect.DeepEqual(argsShare, expectedShare) {
		t.Errorf("argsShare = %v; want %v", argsShare, expectedShare)
	}
}

func TestParseChecksumForAsset(t *testing.T) {
	sampleChecksums := `# SHA-256 Checksums for RMTE release
e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855  rmte-linux-amd64
a591a6d40bf420404a011733cfb7b190d62c65bf0bcda32b57b277d9ad9f146e *rmte-windows-amd64.exe
2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae   rmte-darwin-arm64
`

	// 1. Existing asset with two spaces
	h1 := parseChecksumForAsset(sampleChecksums, "rmte-linux-amd64")
	if h1 != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("got %q, want e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", h1)
	}

	// 2. Existing asset with asterisk prefix (*)
	h2 := parseChecksumForAsset(sampleChecksums, "rmte-windows-amd64.exe")
	if h2 != "a591a6d40bf420404a011733cfb7b190d62c65bf0bcda32b57b277d9ad9f146e" {
		t.Errorf("got %q, want a591a6d40bf420404a011733cfb7b190d62c65bf0bcda32b57b277d9ad9f146e", h2)
	}

	// 3. Existing asset with multiple spaces
	h3 := parseChecksumForAsset(sampleChecksums, "rmte-darwin-arm64")
	if h3 != "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae" {
		t.Errorf("got %q, want 2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae", h3)
	}

	// 4. Non-existent asset
	h4 := parseChecksumForAsset(sampleChecksums, "rmte-freebsd-amd64")
	if h4 != "" {
		t.Errorf("expected empty string for non-existent asset, got %q", h4)
	}
}
