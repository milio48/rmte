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
