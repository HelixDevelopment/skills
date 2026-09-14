package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestChaos_InvalidTOML_Recovered verifies that Load() returns an error on
// invalid TOML and does not panic. Subsequent valid loads still succeed.
func TestChaos_InvalidTOML_Recovered(t *testing.T) {
	dir := t.TempDir()

	invalidInputs := []struct {
		name    string
		content string
	}{
		{"empty file", ""},
		{"broken TOML", "[database\nhost = "},
		{"binary garbage", "\x00\xFF\xDE\xAD"},
		{"unclosed quote", `[database]\nhost = "unclosed`},
		{"huge value", "[database]\nhost = \"" + string(make([]byte, 100000)) + "\""},
	}

	for _, tc := range invalidInputs {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath := filepath.Join(dir, "chaos_"+tc.name+".toml")
			if err := os.WriteFile(cfgPath, []byte(tc.content), 0644); err != nil {
				t.Fatalf("write: %v", err)
			}
			// Must not panic — error is expected.
			_, _ = Load(cfgPath)
		})
	}

	// After the barrage, a valid config must still load correctly.
	validPath := filepath.Join(dir, "recovery.toml")
	if err := os.WriteFile(validPath, []byte(`
[database]
host = "localhost"
port = 5432
name = "testdb"
user = "test"
password = "test"

[logging]
level = "info"
format = "json"
`), 0644); err != nil {
		t.Fatalf("write valid config: %v", err)
	}
	cfg, err := Load(validPath)
	if err != nil {
		t.Fatalf("recovery load failed: %v", err)
	}
	if cfg.Database.Host != "localhost" {
		t.Errorf("recovery host: want localhost, got %s", cfg.Database.Host)
	}
}

// TestChaos_MissingFile_NoPanic verifies that Load() on a non-existent path
// returns an error without panicking.
func TestChaos_MissingFile_NoPanic(t *testing.T) {
	_, err := Load("/nonexistent/path/config.toml")
	if err == nil {
		t.Error("expected error for missing config file, got nil")
	}
}

// TestChaos_NilPointerSafety exercises edge cases around empty env vars
// and default substitution with empty strings.
func TestChaos_NilPointerSafety(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "env_chaos.toml")
	if err := os.WriteFile(cfgPath, []byte(`
[database]
host = "${NONEXISTENT_VAR_12345:-default-host}"
port = 5432
name = "testdb"
user = "test"
password = "test"

[logging]
level = "info"
format = "json"
`), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load with missing env var: %v", err)
	}
	if cfg.Database.Host != "default-host" {
		t.Errorf("expected default-host, got %s", cfg.Database.Host)
	}
}
