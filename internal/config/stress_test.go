package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestStress_ConcurrentLoad exercises concurrent Load() calls against the
// same config file. N=100 goroutines, no races expected.
func TestStress_ConcurrentLoad(t *testing.T) {
	// Write a minimal valid TOML config.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(`
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
		t.Fatalf("write config: %v", err)
	}

	const n = 100
	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cfg, err := Load(cfgPath)
			if err != nil {
				errs <- err
				return
			}
			if cfg.Database.Host != "localhost" {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("concurrent Load failed: %v", err)
		}
	}
}

// TestStress_ConcurrentLoadWithEnvSubstitution exercises concurrent Load()
// calls that require environment variable interpolation.
func TestStress_ConcurrentLoadWithEnvSubstitution(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config_env.toml")
	if err := os.WriteFile(cfgPath, []byte(`
[database]
host = "${STRESS_DB_HOST:-localhost}"
port = 5432
name = "testdb"
user = "test"
password = "test"

[logging]
level = "info"
format = "json"
`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("STRESS_DB_HOST", "db.example.com")

	const n = 100
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cfg, err := Load(cfgPath)
			if err != nil {
				t.Errorf("concurrent Load with env: %v", err)
				return
			}
			if cfg.Database.Host != "db.example.com" {
				t.Errorf("expected host db.example.com, got %s", cfg.Database.Host)
			}
		}()
	}
	wg.Wait()
}
