package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// interpolate / substituteEnv (${VAR} and ${VAR:-default} syntax)
// ---------------------------------------------------------------------------

func TestInterpolate(t *testing.T) {
	t.Setenv("HELIX_TEST_VAR", "actual-value")
	// G26: a variable explicitly set to the empty string is distinct from an
	// unset variable — os.Getenv cannot tell them apart, os.LookupEnv can.
	t.Setenv("HELIX_TEST_VAR_EMPTY", "")

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain string, no placeholder", "just-a-value", "just-a-value"},
		{"set variable substituted", "${HELIX_TEST_VAR}", "actual-value"},
		{"set variable with default ignores default", "${HELIX_TEST_VAR:-fallback}", "actual-value"},
		{"unset variable with default uses default", "${HELIX_TEST_VAR_UNSET:-fallback}", "fallback"},
		{"unset variable without default becomes empty", "${HELIX_TEST_VAR_UNSET}", ""},
		{"variable embedded in larger string", "prefix-${HELIX_TEST_VAR}-suffix", "prefix-actual-value-suffix"},
		{"multiple placeholders", "${HELIX_TEST_VAR}/${HELIX_TEST_VAR_UNSET:-def}", "actual-value/def"},
		// G26 (the bug): a variable SET to the empty string with a default must
		// honor the explicit empty override, NOT fall back to the default.
		// Pre-fix (os.Getenv() != "") returns "fallback" -> RED.
		{"set-empty variable with default honors empty override not default", "${HELIX_TEST_VAR_EMPTY:-fallback}", ""},
		// G26 (regression floor, non-discriminating): set-empty without a default
		// already resolves to "" under both implementations — asserted for
		// completeness so the fix's negative space is proven, not assumed.
		{"set-empty variable without default resolves to empty", "${HELIX_TEST_VAR_EMPTY}", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := interpolate(tt.input)
			if err != nil {
				t.Fatalf("interpolate(%q) returned unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("interpolate(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSubstituteEnv_AppliesAcrossAllDocumentedFields(t *testing.T) {
	t.Setenv("HELIX_TEST_HOST", "sub-host")
	t.Setenv("HELIX_TEST_DB_PASSWORD", "sub-secret-password")

	cfg := defaultConfig()
	cfg.Server.Host = "${HELIX_TEST_HOST}"
	cfg.Database.Password = "${HELIX_TEST_DB_PASSWORD}"
	cfg.Database.Host = "${HELIX_TEST_DB_HOST_UNSET:-localhost-fallback}"
	cfg.Embedding.APIKey = "${HELIX_TEST_APIKEY_UNSET}"

	if err := substituteEnv(&cfg); err != nil {
		t.Fatalf("substituteEnv returned unexpected error: %v", err)
	}

	if cfg.Server.Host != "sub-host" {
		t.Errorf("Server.Host = %q, want %q", cfg.Server.Host, "sub-host")
	}
	if cfg.Database.Password != "sub-secret-password" {
		t.Errorf("Database.Password = %q, want %q", cfg.Database.Password, "sub-secret-password")
	}
	if cfg.Database.Host != "localhost-fallback" {
		t.Errorf("Database.Host = %q, want %q", cfg.Database.Host, "localhost-fallback")
	}
	if cfg.Embedding.APIKey != "" {
		t.Errorf("Embedding.APIKey = %q, want empty string for unset var with no default", cfg.Embedding.APIKey)
	}
}

// TestSubstituteEnv_ExplicitEmptyOverrideHonored proves the G26 fix propagates
// through the whole struct-walking substituteEnv layer, not merely the raw
// interpolate primitive: a config field whose value is ${VAR:-default} with VAR
// SET to the empty string must resolve to "" (the honored explicit override),
// never the default. Pre-fix (os.Getenv() != "") this field becomes
// "localhost-fallback" -> RED; post-fix (os.LookupEnv) it becomes "".
func TestSubstituteEnv_ExplicitEmptyOverrideHonored(t *testing.T) {
	t.Setenv("HELIX_TEST_DB_HOST_EMPTY", "")

	cfg := defaultConfig()
	cfg.Database.Host = "${HELIX_TEST_DB_HOST_EMPTY:-localhost-fallback}"

	if err := substituteEnv(&cfg); err != nil {
		t.Fatalf("substituteEnv returned unexpected error: %v", err)
	}

	if cfg.Database.Host != "" {
		t.Errorf("Database.Host = %q, want %q (explicit empty override must be honored, not the default)", cfg.Database.Host, "")
	}
}

// ---------------------------------------------------------------------------
// applyEnvOverrides (explicit HELIX_* environment overrides)
// ---------------------------------------------------------------------------

func TestApplyEnvOverrides(t *testing.T) {
	t.Setenv("HELIX_DB_HOST", "override-host")
	t.Setenv("HELIX_DB_PORT", "6543")
	t.Setenv("HELIX_DB_NAME", "override-db")
	t.Setenv("HELIX_DB_USER", "override-user")
	t.Setenv("HELIX_DB_PASSWORD", "override-password")
	t.Setenv("HELIX_DB_SSLMODE", "require")
	t.Setenv("HELIX_LOG_LEVEL", "debug")
	t.Setenv("HELIX_MCP_TRANSPORT", "http")

	cfg := defaultConfig()
	applyEnvOverrides(&cfg)

	if cfg.Database.Host != "override-host" {
		t.Errorf("Database.Host = %q, want %q", cfg.Database.Host, "override-host")
	}
	if cfg.Database.Port != 6543 {
		t.Errorf("Database.Port = %d, want %d", cfg.Database.Port, 6543)
	}
	if cfg.Database.Database != "override-db" {
		t.Errorf("Database.Database = %q, want %q", cfg.Database.Database, "override-db")
	}
	if cfg.Database.User != "override-user" {
		t.Errorf("Database.User = %q, want %q", cfg.Database.User, "override-user")
	}
	if cfg.Database.Password != "override-password" {
		t.Errorf("Database.Password = %q, want %q", cfg.Database.Password, "override-password")
	}
	if cfg.Database.SSLMode != "require" {
		t.Errorf("Database.SSLMode = %q, want %q", cfg.Database.SSLMode, "require")
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("Logging.Level = %q, want %q", cfg.Logging.Level, "debug")
	}
	if cfg.MCP.Transport != "http" {
		t.Errorf("MCP.Transport = %q, want %q", cfg.MCP.Transport, "http")
	}
}

func TestApplyEnvOverrides_InvalidPortIsIgnored(t *testing.T) {
	t.Setenv("HELIX_DB_PORT", "not-a-number")

	cfg := defaultConfig()
	originalPort := cfg.Database.Port

	applyEnvOverrides(&cfg)

	if cfg.Database.Port != originalPort {
		t.Errorf("Database.Port = %d, want unchanged %d when HELIX_DB_PORT is non-numeric", cfg.Database.Port, originalPort)
	}
}

func TestApplyEnvOverrides_UnsetVarsLeaveDefaultsUntouched(t *testing.T) {
	// Explicitly ensure none of the override vars are set in this test's env.
	for _, v := range []string{
		"HELIX_DB_HOST", "HELIX_DB_PORT", "HELIX_DB_NAME", "HELIX_DB_USER",
		"HELIX_DB_PASSWORD", "HELIX_DB_SSLMODE", "HELIX_LOG_LEVEL", "HELIX_MCP_TRANSPORT",
	} {
		t.Setenv(v, "")
		os.Unsetenv(v)
	}

	cfg := defaultConfig()
	want := defaultConfig()

	applyEnvOverrides(&cfg)

	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("applyEnvOverrides mutated config with no env vars set:\ngot  %+v\nwant %+v", cfg, want)
	}
}

// TestApplyEnvOverrides_APIKeysCommaSplit verifies the HELIX_API_KEYS override
// is comma-split, trimmed, and empty-filtered into Server.APIKeys.
// Paired-mutation: deleting the HELIX_API_KEYS branch in applyEnvOverrides
// leaves Server.APIKeys nil and this test FAILs.
func TestApplyEnvOverrides_APIKeysCommaSplit(t *testing.T) {
	t.Setenv("HELIX_API_KEYS", " key-one , key-two ,, key-three ")

	cfg := defaultConfig()
	applyEnvOverrides(&cfg)

	want := []string{"key-one", "key-two", "key-three"}
	if !reflect.DeepEqual(cfg.Server.APIKeys, want) {
		t.Errorf("Server.APIKeys = %#v, want %#v (comma-split, trimmed, empties dropped)", cfg.Server.APIKeys, want)
	}
}

// TestApplyEnvOverrides_AuthDisabled verifies the HELIX_AUTH_DISABLED override
// maps truthy values to true and everything else to false — actively setting
// the field in BOTH directions. Paired-mutation: deleting the branch leaves the
// truthy cases at their default (false) and those subtests FAIL.
func TestApplyEnvOverrides_AuthDisabled(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "True"} {
		t.Run("truthy/"+v, func(t *testing.T) {
			t.Setenv("HELIX_AUTH_DISABLED", v)
			cfg := defaultConfig() // AuthDisabled defaults to false
			applyEnvOverrides(&cfg)
			if !cfg.Server.AuthDisabled {
				t.Errorf("HELIX_AUTH_DISABLED=%q -> AuthDisabled=false, want true", v)
			}
		})
	}
	for _, v := range []string{"0", "false", "no", "anything-else"} {
		t.Run("falsy/"+v, func(t *testing.T) {
			t.Setenv("HELIX_AUTH_DISABLED", v)
			cfg := defaultConfig()
			cfg.Server.AuthDisabled = true // the override must ACTIVELY set it false
			applyEnvOverrides(&cfg)
			if cfg.Server.AuthDisabled {
				t.Errorf("HELIX_AUTH_DISABLED=%q -> AuthDisabled=true, want false", v)
			}
		})
	}
}

// TestSplitAndTrim exercises the comma-split helper used by list-valued env
// overrides: trimming and empty-filtering. Paired-mutation: removing the
// non-empty (trim-and-skip-blank) filter makes the empty-token cases FAIL.
func TestSplitAndTrim(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", []string{}},
		{"a", []string{"a"}},
		{" a , b ", []string{"a", "b"}},
		{"a,,b", []string{"a", "b"}},
		{" , , ", []string{}},
		{",a,", []string{"a"}},
	}
	for _, tt := range tests {
		if got := splitAndTrim(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("splitAndTrim(%q) = %#v, want %#v", tt.in, got, tt.want)
		}
	}
}

// TestSubstituteEnv_InterpolatesServerListFields verifies ${VAR} placeholders in
// Server.APIKeys and Server.AllowedOrigins are interpolated (not left literal).
// Paired-mutation: deleting the list-field interpolation loops in substituteEnv
// leaves the "${HELIX_TEST_*}" literals in place and this test FAILs — the exact
// defect where api_keys = ["${PROD_KEY}"] became a literal valid credential.
func TestSubstituteEnv_InterpolatesServerListFields(t *testing.T) {
	t.Setenv("HELIX_TEST_PROD_KEY", "resolved-secret-key")
	t.Setenv("HELIX_TEST_ORIGIN", "https://app.example.com")

	cfg := defaultConfig()
	cfg.Server.APIKeys = []string{"${HELIX_TEST_PROD_KEY}", "static-key"}
	cfg.Server.AllowedOrigins = []string{"${HELIX_TEST_ORIGIN}", "https://other.example.com"}

	if err := substituteEnv(&cfg); err != nil {
		t.Fatalf("substituteEnv returned unexpected error: %v", err)
	}

	wantKeys := []string{"resolved-secret-key", "static-key"}
	if !reflect.DeepEqual(cfg.Server.APIKeys, wantKeys) {
		t.Errorf("Server.APIKeys = %#v, want %#v (${VAR} must be interpolated, not literal)", cfg.Server.APIKeys, wantKeys)
	}
	wantOrigins := []string{"https://app.example.com", "https://other.example.com"}
	if !reflect.DeepEqual(cfg.Server.AllowedOrigins, wantOrigins) {
		t.Errorf("Server.AllowedOrigins = %#v, want %#v", cfg.Server.AllowedOrigins, wantOrigins)
	}
}

// TestValidate_RejectsUninterpolatedPlaceholderInSecrets verifies the
// fail-closed defense-in-depth check: an api_keys/allowed_origins entry that
// still holds a "${" placeholder after interpolation (unset var / malformed
// placeholder) is rejected rather than trusted as a literal secret/origin.
// Paired-mutation: deleting either validate() loop makes the matching subtest
// FAIL (validate would return nil).
func TestValidate_RejectsUninterpolatedPlaceholderInSecrets(t *testing.T) {
	t.Run("api_keys", func(t *testing.T) {
		cfg := defaultConfig()
		cfg.Server.APIKeys = []string{"${MISSING_CLOSING_BRACE"} // malformed: survives interpolation
		err := validate(&cfg)
		if err == nil {
			t.Fatal("validate() = nil, want an error for api_keys with an uninterpolated ${ placeholder")
		}
		if !strings.Contains(err.Error(), "server.api_keys") {
			t.Errorf("validate() error = %q, want it to mention server.api_keys", err.Error())
		}
	})
	t.Run("allowed_origins", func(t *testing.T) {
		cfg := defaultConfig()
		cfg.Server.AllowedOrigins = []string{"${MISSING_CLOSING_BRACE"}
		err := validate(&cfg)
		if err == nil {
			t.Fatal("validate() = nil, want an error for allowed_origins with an uninterpolated ${ placeholder")
		}
		if !strings.Contains(err.Error(), "server.allowed_origins") {
			t.Errorf("validate() error = %q, want it to mention server.allowed_origins", err.Error())
		}
	})
}

// ---------------------------------------------------------------------------
// validate
// ---------------------------------------------------------------------------

func TestValidate_ValidConfigPasses(t *testing.T) {
	cfg := defaultConfig()
	if err := validate(&cfg); err != nil {
		t.Errorf("validate(defaultConfig()) returned unexpected error: %v", err)
	}
}

func TestValidate_RejectsInvalidFields(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name:    "http port zero",
			mutate:  func(c *Config) { c.Server.HTTPPort = 0 },
			wantErr: "invalid server.http_port",
		},
		{
			name:    "http port too large",
			mutate:  func(c *Config) { c.Server.HTTPPort = 70000 },
			wantErr: "invalid server.http_port",
		},
		{
			name:    "http3 port negative",
			mutate:  func(c *Config) { c.Server.HTTP3Port = -1 },
			wantErr: "invalid server.http3_port",
		},
		{
			name:    "database port out of range",
			mutate:  func(c *Config) { c.Database.Port = 99999 },
			wantErr: "invalid database.port",
		},
		{
			name:    "embedding dimensions zero",
			mutate:  func(c *Config) { c.Embedding.Dimensions = 0 },
			wantErr: "invalid embedding.dimensions",
		},
		{
			name:    "jury size zero",
			mutate:  func(c *Config) { c.Validation.JurySize = 0 },
			wantErr: "invalid validation.jury_size",
		},
		{
			name:    "approval threshold zero",
			mutate:  func(c *Config) { c.Validation.ApprovalThreshold = 0 },
			wantErr: "invalid validation.approval_threshold",
		},
		{
			name:    "autoexpand max depth zero",
			mutate:  func(c *Config) { c.AutoExpand.MaxDepth = 0 },
			wantErr: "invalid autoexpand.max_depth",
		},
		{
			name:    "autoexpand max new skills per run zero",
			mutate:  func(c *Config) { c.AutoExpand.MaxNewSkillsPerRun = 0 },
			wantErr: "invalid autoexpand.max_new_skills_per_run",
		},
		{
			name:    "coverage threshold above 1",
			mutate:  func(c *Config) { c.Registry.CoverageThreshold = 1.5 },
			wantErr: "invalid registry.coverage_threshold",
		},
		{
			name:    "coverage threshold below 0",
			mutate:  func(c *Config) { c.Registry.CoverageThreshold = -0.1 },
			wantErr: "invalid registry.coverage_threshold",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultConfig()
			tt.mutate(&cfg)

			err := validate(&cfg)
			if err == nil {
				t.Fatalf("validate() = nil, want an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("validate() error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidate_ZeroMaxConnectionsGetsSafeDefaultInsteadOfError(t *testing.T) {
	cfg := defaultConfig()
	cfg.Database.MaxConnections = 0

	if err := validate(&cfg); err != nil {
		t.Fatalf("validate() returned unexpected error: %v", err)
	}
	if cfg.Database.MaxConnections != 25 {
		t.Errorf("Database.MaxConnections = %d, want the safe default 25 to be applied", cfg.Database.MaxConnections)
	}
}

// ---------------------------------------------------------------------------
// Load (file discovery, TOML decode, env substitution, override, validation)
// ---------------------------------------------------------------------------

func TestLoad_ExplicitPathWithOverridesAndSubstitution(t *testing.T) {
	t.Setenv("HELIX_TEST_LOAD_HOST", "toml-env-host")
	t.Setenv("HELIX_LOG_LEVEL", "warn") // explicit override, should win over the TOML value

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	tomlContent := `
[server]
host = "${HELIX_TEST_LOAD_HOST}"
http_port = 9090
http3_port = 9443

[logging]
level = "info"
format = "console"
`
	if err := os.WriteFile(path, []byte(tomlContent), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%q) returned unexpected error: %v", path, err)
	}

	if cfg.Server.Host != "toml-env-host" {
		t.Errorf("Server.Host = %q, want %q (env substitution)", cfg.Server.Host, "toml-env-host")
	}
	if cfg.Server.HTTPPort != 9090 {
		t.Errorf("Server.HTTPPort = %d, want %d", cfg.Server.HTTPPort, 9090)
	}
	if cfg.Logging.Level != "warn" {
		t.Errorf("Logging.Level = %q, want %q (explicit HELIX_LOG_LEVEL override wins over TOML)", cfg.Logging.Level, "warn")
	}
	if cfg.Logging.Format != "console" {
		t.Errorf("Logging.Format = %q, want %q (unset-by-override field keeps TOML value)", cfg.Logging.Format, "console")
	}
	// Sections not present in the TOML file must still carry their defaults.
	if cfg.Validation.JurySize != 3 {
		t.Errorf("Validation.JurySize = %d, want default %d for a section absent from the TOML file", cfg.Validation.JurySize, 3)
	}
}

// TestLoad_MCPTransportACPIsAccepted proves the "acp" MCP transport value
// (NEW-2 wire-in of the previously-dead internal/mcp/acp_adapter.go as a
// selectable `--mcp acp` / `mcp.transport = "acp"` transport) is accepted by
// config validation -- Load() must not reject it, and the loaded value must
// round-trip unchanged so cmd/server's transport switch actually sees "acp".
func TestLoad_MCPTransportACPIsAccepted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	tomlContent := `
[mcp]
enabled = true
transport = "acp"
`
	if err := os.WriteFile(path, []byte(tomlContent), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf(`Load() with mcp.transport = "acp" returned unexpected error: %v`, err)
	}
	if cfg.MCP.Transport != "acp" {
		t.Errorf("MCP.Transport = %q, want %q", cfg.MCP.Transport, "acp")
	}
	if !cfg.MCP.Enabled {
		t.Error("MCP.Enabled = false, want true (from TOML)")
	}
}

// TestApplyEnvOverrides_MCPTransportACP proves the HELIX_MCP_TRANSPORT
// explicit-override path (used by --mcp acp via cmd/server's CLI-flag
// override, and directly by the env var) also accepts "acp" unchanged.
func TestApplyEnvOverrides_MCPTransportACP(t *testing.T) {
	t.Setenv("HELIX_MCP_TRANSPORT", "acp")

	cfg := defaultConfig()
	applyEnvOverrides(&cfg)

	if cfg.MCP.Transport != "acp" {
		t.Errorf("MCP.Transport = %q, want %q", cfg.MCP.Transport, "acp")
	}
}

func TestLoad_MissingFileReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.toml")

	if _, err := Load(path); err == nil {
		t.Fatal("Load() with a nonexistent explicit path = nil error, want an error")
	}
}

func TestLoad_InvalidTOMLReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(path, []byte("this is not [valid toml"), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("Load() with malformed TOML = nil error, want an error")
	}
}

func TestLoad_ValidationFailurePropagates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "invalid-values.toml")
	tomlContent := `
[server]
http_port = 0
`
	if err := os.WriteFile(path, []byte(tomlContent), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() with an out-of-range http_port = nil error, want an error")
	}
	if !strings.Contains(err.Error(), "http_port") {
		t.Errorf("Load() error = %q, want it to mention the invalid field", err.Error())
	}
}

// ---------------------------------------------------------------------------
// DatabaseConfig.DSN / DSNWithTimeout
// ---------------------------------------------------------------------------

func TestDatabaseConfig_DSN(t *testing.T) {
	d := DatabaseConfig{
		Host:     "dbhost",
		Port:     5432,
		Database: "mydb",
		User:     "myuser",
		Password: "mypass",
		SSLMode:  "disable",
	}

	want := "host=dbhost port=5432 dbname=mydb user=myuser password=mypass sslmode=disable"
	if got := d.DSN(); got != want {
		t.Errorf("DSN() = %q, want %q", got, want)
	}
}

func TestDatabaseConfig_DSNWithTimeout(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
		wantSec int
	}{
		{"explicit positive timeout", 5 * time.Second, 5},
		{"zero timeout falls back to 10s", 0, 10},
		{"negative timeout falls back to 10s", -3 * time.Second, 10},
		{"sub-second timeout truncates to zero then falls back to 10s", 500 * time.Millisecond, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := DatabaseConfig{
				Host: "h", Port: 1, Database: "d", User: "u", Password: "p", SSLMode: "disable",
				ConnectTimeout: tt.timeout,
			}
			got := d.DSNWithTimeout()
			wantSuffix := " connect_timeout=" + strconv.Itoa(tt.wantSec)
			if !strings.HasSuffix(got, wantSuffix) {
				t.Errorf("DSNWithTimeout() = %q, want suffix %q", got, wantSuffix)
			}
			if !strings.HasPrefix(got, d.DSN()) {
				t.Errorf("DSNWithTimeout() = %q, want it to start with DSN() %q", got, d.DSN())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Config.ListenAddr / HTTP3ListenAddr
// ---------------------------------------------------------------------------

func TestConfig_ListenAddr(t *testing.T) {
	c := &Config{Server: ServerConfig{HTTPPort: 8080}}
	if got := c.ListenAddr(); got != ":8080" {
		t.Errorf("ListenAddr() = %q, want %q", got, ":8080")
	}
}

func TestConfig_HTTP3ListenAddr(t *testing.T) {
	c := &Config{Server: ServerConfig{HTTP3Port: 8443}}
	if got := c.HTTP3ListenAddr(); got != ":8443" {
		t.Errorf("HTTP3ListenAddr() = %q, want %q", got, ":8443")
	}
}
