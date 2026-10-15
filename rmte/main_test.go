package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveCredential(t *testing.T) {
	tmpDir := t.TempDir()
	passFilePath := filepath.Join(tmpDir, "test_secret.txt")
	if err := os.WriteFile(passFilePath, []byte("  secret_from_file\r\n  "), 0600); err != nil {
		t.Fatalf("failed to write test pass file: %v", err)
	}

	emptyFilePath := filepath.Join(tmpDir, "empty_secret.txt")
	if err := os.WriteFile(emptyFilePath, []byte("   \n  "), 0600); err != nil {
		t.Fatalf("failed to write empty test pass file: %v", err)
	}

	envKey := "TEST_RMTE_PASS_RESOLVE"
	t.Setenv(envKey, "secret_from_env")

	// 1. Flag takes highest precedence
	val, src, err := resolveCredential("flag_pass", true, passFilePath, envKey, "default_pass")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "flag_pass" || src != CredSourceFlag {
		t.Errorf("expected flag_pass with CredSourceFlag, got %q (src=%v)", val, src)
	}

	// 2. PassFile takes precedence over Env
	val, src, err = resolveCredential("", false, passFilePath, envKey, "default_pass")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "secret_from_file" || src != CredSourceFile {
		t.Errorf("expected secret_from_file with CredSourceFile, got %q (src=%v)", val, src)
	}

	// 3. Env takes precedence over Default when Flag & File are empty
	val, src, err = resolveCredential("", false, "", envKey, "default_pass")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "secret_from_env" || src != CredSourceEnv {
		t.Errorf("expected secret_from_env with CredSourceEnv, got %q (src=%v)", val, src)
	}

	// 4. Default fallback when all empty
	val, src, err = resolveCredential("", false, "", "NON_EXISTENT_ENV_KEY", "default_pass")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "default_pass" || src != CredSourceDefault {
		t.Errorf("expected default_pass with CredSourceDefault, got %q (src=%v)", val, src)
	}

	// 5. Error when file does not exist
	_, _, err = resolveCredential("", false, filepath.Join(tmpDir, "missing.txt"), envKey, "default_pass")
	if err == nil {
		t.Errorf("expected error for missing pass file, got nil")
	}

	// 6. Error when file is empty
	_, _, err = resolveCredential("", false, emptyFilePath, envKey, "default_pass")
	if err == nil {
		t.Errorf("expected error for empty pass file, got nil")
	}
}

func TestParseShareOptions(t *testing.T) {
	// 1. With subcommand word "share" (regression test for Muse Finding #1)
	argsWithShare := []string{
		"share",
		"--id=custombox",
		"--pass=secret123",
		"--server-relay=ws://127.0.0.1:18048/ws-rmte",
		"--allow-exec",
		"--sandbox",
		"--buffer=3",
	}
	opts1, err := parseShareOptions(argsWithShare)
	if err != nil {
		t.Fatalf("unexpected error with 'share' prefix: %v", err)
	}
	if opts1.ID != "custombox" {
		t.Errorf("expected ID 'custombox', got %q", opts1.ID)
	}
	if opts1.Pass != "secret123" {
		t.Errorf("expected Pass 'secret123', got %q", opts1.Pass)
	}
	if opts1.DialURL != "ws://127.0.0.1:18048/ws-rmte" {
		t.Errorf("expected DialURL 'ws://127.0.0.1:18048/ws-rmte', got %q", opts1.DialURL)
	}
	if !opts1.AllowExec {
		t.Errorf("expected AllowExec to be true")
	}
	if !opts1.Sandbox {
		t.Errorf("expected Sandbox to be true")
	}
	if opts1.Buffer != 3 {
		t.Errorf("expected Buffer 3, got %d", opts1.Buffer)
	}

	// 2. Bare invocation without "share" word
	argsBare := []string{
		"--id=barebox",
		"--pass=barepass",
		"--enable-ai",
		"--dir-only",
	}
	opts2, err := parseShareOptions(argsBare)
	if err != nil {
		t.Fatalf("unexpected error with bare args: %v", err)
	}
	if opts2.ID != "barebox" || opts2.Pass != "barepass" || !opts2.AllowExec || !opts2.Sandbox {
		t.Errorf("unexpected opts for bare args: %+v", opts2)
	}

	// 3. Default fallback (nil or empty args)
	optsDefault, err := parseShareOptions(nil)
	if err != nil {
		t.Fatalf("unexpected error with nil args: %v", err)
	}
	if optsDefault.DialURL != defaultPublicRelay {
		t.Errorf("expected default DialURL %q, got %q", defaultPublicRelay, optsDefault.DialURL)
	}
	if optsDefault.Pass == "" || !optsDefault.PassGenerated {
		t.Errorf("expected generated password, got %q (generated=%v)", optsDefault.Pass, optsDefault.PassGenerated)
	}
	if optsDefault.MaxChat != DefaultMaxChat {
		t.Errorf("expected default MaxChat %d, got %d", DefaultMaxChat, optsDefault.MaxChat)
	}

	// 4. Invalid buffer error
	_, err = parseShareOptions([]string{"share", "--buffer=0"})
	if err == nil {
		t.Errorf("expected error for --buffer=0, got nil")
	}

	// 5. MaxChat flag tests
	optsCustomChat, err := parseShareOptions([]string{"share", "--max-chat=100"})
	if err != nil {
		t.Fatalf("unexpected error for --max-chat=100: %v", err)
	}
	if optsCustomChat.MaxChat != 100 {
		t.Errorf("expected MaxChat 100, got %d", optsCustomChat.MaxChat)
	}

	optsZeroChat, err := parseShareOptions([]string{"share", "--max-chat=0"})
	if err != nil {
		t.Fatalf("unexpected error for --max-chat=0: %v", err)
	}
	if optsZeroChat.MaxChat != 0 {
		t.Errorf("expected MaxChat 0, got %d", optsZeroChat.MaxChat)
	}

	_, err = parseShareOptions([]string{"share", "--max-chat=-1"})
	if err == nil {
		t.Errorf("expected error for --max-chat=-1, got nil")
	}
}
