// Package config provides configuration loading from TOML files with
// environment variable overrides. It supports ${VAR} interpolation syntax
// in string values, allowing secrets and dynamic values to be injected
// via environment variables.
//
// Usage:
//
//	cfg, err := config.Load("config/config.toml")
//	if err != nil {
//	    log.Fatal(err)
//	}
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// envVarRegex matches ${VAR} or ${VAR:-default} syntax in config values.
var envVarRegex = regexp.MustCompile(`\$\{([^}]+)\}`)

// ---------------------------------------------------------------------------
// Config (root)
// ---------------------------------------------------------------------------

// Config holds all application configuration sections.
type Config struct {
	Server       ServerConfig       `toml:"server"`
	Database     DatabaseConfig     `toml:"database"`
	Embedding    EmbeddingConfig    `toml:"embedding"`
	Validation   ValidationConfig   `toml:"validation"`
	AutoExpand   AutoExpandConfig   `toml:"autoexpand"`
	CodeAnalysis CodeAnalysisConfig `toml:"codeanalysis"`
	CodeGraph    CodeGraphConfig    `toml:"codegraph"`
	MCP          MCPConfig          `toml:"mcp"`
	Registry     RegistryConfig     `toml:"registry"`
	Logging      LoggingConfig      `toml:"logging"`
	Cache        CacheConfig        `toml:"cache"`
	Metrics      MetricsConfig      `toml:"metrics"`
	Tenant       TenantConfig       `toml:"tenant"`
	SourceSync   SourceSyncConfig   `toml:"source_sync"`
}

// ---------------------------------------------------------------------------
// Section structs
// ---------------------------------------------------------------------------

// ServerConfig controls the HTTP/HTTPS server behaviour.
type ServerConfig struct {
	Host         string `toml:"host"`
	HTTPPort     int    `toml:"http_port"`
	HTTP3Port    int    `toml:"http3_port"`
	EnableHTTP3  bool   `toml:"enable_http3"`
	EnableBrotli bool   `toml:"enable_brotli"`
	TLSCert      string `toml:"tls_cert"`
	TLSKey       string `toml:"tls_key"`
	// AllowedOrigins is the CORS allowlist of exact origins permitted to make
	// cross-origin requests (e.g. "https://app.example.com"). A single "*"
	// entry allows any origin but only without credentials. Empty (the default)
	// disallows all cross-origin access.
	AllowedOrigins []string `toml:"allowed_origins"`
	// APIKeys is the set of valid keys that authenticate /api/v1 requests via
	// the X-API-Key header. Prefer providing these through the HELIX_API_KEYS
	// environment override (comma-separated) so secrets never live in tracked
	// config (§11.4.10). When APIKeys is empty AND AuthDisabled is false, the
	// server fails CLOSED and refuses every /api/v1 request.
	APIKeys []string `toml:"api_keys"`
	// AuthDisabled explicitly runs the API with NO authentication. It must be
	// set deliberately and is logged loudly at startup. Absent keys without
	// this flag is a fail-closed error, never a silent open server.
	AuthDisabled bool `toml:"auth_disabled"`
	// RateLimit configures the per-client token-bucket limiter applied on the
	// live router BEFORE authentication (§G22 DoS hardening). Disabled leaves
	// the limiter off entirely.
	RateLimit RateLimitConfig `toml:"rate_limit"`
	// MaxRequestBodyBytes caps the accepted request body (§G22). A body whose
	// declared Content-Length exceeds this is rejected with 413 before it is
	// read, and streamed bodies are truncated at the cap. A value <= 0 falls
	// back to the 100 MiB default (api.DefaultMaxBodyBytes) in the router.
	MaxRequestBodyBytes int64 `toml:"max_request_body_bytes"`
}

// RateLimitConfig controls the per-client token-bucket rate limiter (§G22).
//
// Calibration note (§11.4.6 / register G22-a): RequestsPerSecond and Burst are
// SENSIBLE DEFAULTS, not calibrated production thresholds — the concrete numbers
// for the R15 single-node deploy MUST be tuned against a real load profile, not
// hardcoded from literature. The 429/isolation BEHAVIOUR is what is guaranteed
// here; the exact rate is operator-tunable via config.
type RateLimitConfig struct {
	// Enabled installs the limiter on the live router. Off leaves the surface
	// unthrottled (only appropriate behind a trusted upstream limiter).
	Enabled bool `toml:"enabled"`
	// RequestsPerSecond is the steady-state token refill rate per client key.
	RequestsPerSecond float64 `toml:"requests_per_second"`
	// Burst is the maximum instantaneous number of requests a single client key
	// may make before being throttled (the token-bucket depth).
	Burst int `toml:"burst"`
	// TTL is the idle window after which an unused per-client limiter entry is
	// reaped (housekeeping that releases long-idle keys).
	TTL time.Duration `toml:"ttl"`
	// MaxClients is the HARD upper bound on the number of distinct client keys
	// tracked at once. It — not the TTL reap — is what makes the tracking map
	// genuinely bounded: when the cap is reached the least-recently-used entry
	// is evicted, so a distinct-IP flood cannot grow the map without bound (F2).
	// A non-positive value falls back to a safe default in the limiter.
	MaxClients int `toml:"max_clients"`
}

// DatabaseConfig controls the PostgreSQL connection pool.
type DatabaseConfig struct {
	Host           string        `toml:"host"`
	Port           int           `toml:"port"`
	Database       string        `toml:"database"`
	User           string        `toml:"user"`
	Password       string        `toml:"password"`
	SSLMode        string        `toml:"ssl_mode"`
	MaxConnections int           `toml:"max_connections"`
	ConnectTimeout time.Duration `toml:"connect_timeout"`
	// Replica holds optional read-replica configuration. When a replica DSN is
	// provided, read operations are routed to the replica and writes go to the
	// primary. When empty, all traffic goes to the primary pool.
	Replica ReplicaConfig `toml:"replica"`
}

// DSN returns a PostgreSQL keyword/value connection string for pgx or lib/pq.
func (d DatabaseConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d dbname=%s user=%s password=%s sslmode=%s",
		d.Host, d.Port, d.Database, d.User, d.Password, d.SSLMode,
	)
}

// DSNWithTimeout returns a DSN with connect_timeout included.
func (d DatabaseConfig) DSNWithTimeout() string {
	timeoutSec := int(d.ConnectTimeout.Seconds())
	if timeoutSec <= 0 {
		timeoutSec = 10
	}
	return d.DSN() + fmt.Sprintf(" connect_timeout=%d", timeoutSec)
}

// EmbeddingConfig selects the embedding provider and model.
type EmbeddingConfig struct {
	Provider      string `toml:"provider"`       // "openai" | "local"
	Dimensions    int    `toml:"dimensions"`     // e.g. 768
	Model         string `toml:"model"`          // e.g. "text-embedding-3-small"
	APIKey        string `toml:"api_key"`        // OpenAI API key (env override recommended)
	LocalEndpoint string `toml:"local_endpoint"` // URL for local model server
}

// ValidationConfig controls the skill validation pipeline.
type ValidationConfig struct {
	Enabled             bool `toml:"enabled"`
	JurySize            int  `toml:"jury_size"`          // number of validators
	ApprovalThreshold   int  `toml:"approval_threshold"` // votes required
	AutoApproveEvidence bool `toml:"auto_approve_evidence"`
	RequireHumanReview  bool `toml:"require_human_review"`
}

// AutoExpandConfig controls the automatic skill-tree expansion.
type AutoExpandConfig struct {
	Enabled            bool   `toml:"enabled"`
	MaxDepth           int    `toml:"max_depth"`
	MaxNewSkillsPerRun int    `toml:"max_new_skills_per_run"`
	LLMProvider        string `toml:"llm_provider"` // "openai" | "anthropic" | "local" | "helixllm"
	LLMModel           string `toml:"llm_model"`
	// LLMAPIKey is resolved from the environment via ${VAR} interpolation
	// (e.g. "${ANTHROPIC_API_KEY}") -- NEVER a literal secret in tracked
	// config (§11.4.10). Empty is permitted; the provider client is still
	// constructed and the first real request surfaces the auth failure.
	LLMAPIKey string `toml:"llm_api_key"`
	// LLMBaseURL is REQUIRED for the "local"/"helixllm" providers (an
	// OpenAI-compatible chat-completions base URL) and ignored otherwise.
	LLMBaseURL string `toml:"llm_base_url"`
}

// CodeAnalysisConfig controls the repository-learning subsystem.
type CodeAnalysisConfig struct {
	Enabled         bool     `toml:"enabled"`
	Languages       []string `toml:"languages"`
	MaxFileSizeKB   int      `toml:"max_file_size_kb"`
	ExcludePatterns []string `toml:"exclude_patterns"`
	// AllowedRoot is the single allowlisted filesystem root that a submitted
	// project_path (learn_from_project MCP tool, and any other code-analysis
	// entry point) MUST canonicalize inside (§G31 path-traversal / LFI
	// guard -- GAPS_AND_RISKS_REGISTER.md). Canonicalization resolves
	// symlinks (filepath.EvalSymlinks) so a symlink planted inside
	// AllowedRoot cannot be used to escape it.
	//
	// FAIL-CLOSED BY DEFAULT: an empty AllowedRoot rejects EVERY
	// project_path submission rather than silently allow-listing the whole
	// filesystem (same fail-closed posture as Server.APIKeys/AuthDisabled
	// and Server.AllowedOrigins above). Operators MUST set this deliberately
	// to the directory tree learn_from_project is meant to scan (e.g. a
	// dedicated projects/workspaces root) before the tool accepts any
	// submission. Prefer the HELIX_CODEANALYSIS_ALLOWED_ROOT environment
	// override, or ${VAR} interpolation in the TOML value, so the path
	// never needs to be hardcoded into tracked config.
	AllowedRoot string `toml:"allowed_root"`
}

// MCPConfig controls the Model Context Protocol integration.
type MCPConfig struct {
	Enabled   bool   `toml:"enabled"`
	Transport string `toml:"transport"` // "stdio" | "http"
}

// CodeGraphConfig controls the CodeGraph MCP integration for code indexing
// and sync automation (§11.4.78/§11.4.79/§11.4.80).
type CodeGraphConfig struct {
	Enabled             bool   `toml:"enabled"`
	Transport           string `toml:"transport"` // "stdio" | "http"
	Endpoint            string `toml:"endpoint"`  // HTTP endpoint (if transport=http)
	SyncIntervalSeconds int    `toml:"sync_interval_seconds"`
	AutoIndexOnLearn    bool   `toml:"auto_index_on_learn"`
	WatchEnabled        bool   `toml:"watch_enabled"` // requires fsnotify
}

// RegistryConfig controls skill-registry behaviour.
type RegistryConfig struct {
	ReviewIntervalHours int     `toml:"review_interval_hours"`
	CoverageThreshold   float64 `toml:"coverage_threshold"`
}

// LoggingConfig controls Zap logger output.
type LoggingConfig struct {
	Level  string `toml:"level"`  // "debug" | "info" | "warn" | "error"
	Format string `toml:"format"` // "json" | "console"
}

// ReplicaConfig controls the optional read-replica connection.
type ReplicaConfig struct {
	// DSN is the PostgreSQL connection string for the read replica. When
	// empty the system falls back to the primary for all operations.
	DSN string `toml:"dsn"`
	// MaxLagSeconds is the maximum acceptable replication lag in seconds.
	// If the replica's lag exceeds this threshold, reads are routed to the
	// primary until the lag subsides. A value <= 0 disables lag checking.
	MaxLagSeconds int `toml:"max_lag_seconds"`
	// MaxConnections is the connection pool size for the replica.
	MaxConnections int `toml:"max_connections"`
}

// CacheConfig controls the Redis caching layer.
type CacheConfig struct {
	// Enabled turns the cache on. When false, a NoopCache is used and all
	// cache operations are no-ops (graceful degradation).
	Enabled bool `toml:"enabled"`
	// RedisURL is the Redis connection string (e.g. "redis://localhost:6379").
	RedisURL string `toml:"redis_url"`
	// SkillTTL is the TTL for cached skill content.
	SkillTTL time.Duration `toml:"skill_ttl"`
	// SearchTTL is the TTL for cached search results.
	SearchTTL time.Duration `toml:"search_ttl"`
	// TreeTTL is the TTL for cached dependency trees.
	TreeTTL time.Duration `toml:"tree_ttl"`
}

// TenantConfig controls multi-tenant isolation (§11.4.84, 004_enterprise).
type TenantConfig struct {
	// Required makes tenant resolution mandatory. When true, requests without
	// a resolvable tenant are rejected with 403. When false, unscoped requests
	// pass through (single-tenant backward compatibility).
	Required bool `toml:"required"`
	// DefaultTenant is the UUID of the fallback tenant used when no explicit
	// tenant is provided via header or API key mapping. Empty disables the
	// fallback.
	DefaultTenant string `toml:"default_tenant"`
	// APIKeyTenants maps API key strings to tenant UUIDs. When an authenticated
	// request's API key appears in this map, the corresponding tenant is used.
	APIKeyTenants map[string]string `toml:"api_key_tenants"`
	// RateLimit controls per-tenant rate limiting (§11.4.84). When enabled,
	// each tenant receives an independent token-bucket rate limiter.
	RateLimit TenantRateLimitConfig `toml:"rate_limit"`
}

// TenantRateLimitConfig controls per-tenant rate limiting (§11.4.84).
type TenantRateLimitConfig struct {
	// Enabled installs the per-tenant rate limiter on the API router.
	Enabled bool `toml:"enabled"`
	// RequestsPerMinute is the steady-state refill rate per tenant.
	RequestsPerMinute int `toml:"requests_per_minute"`
	// BurstSize is the maximum instantaneous number of requests a tenant
	// may make before being throttled (token-bucket depth).
	BurstSize int `toml:"burst_size"`
}

// MetricsConfig controls the Prometheus metrics endpoint.
type MetricsConfig struct {
	// Enabled turns metrics collection on. When false, the /metrics
	// endpoint is not registered and counters are not incremented.
	Enabled bool `toml:"enabled"`
	// Path is the HTTP path for the metrics endpoint (default "/metrics").
	Path string `toml:"path"`
}

// SourceSyncConfig controls the skill source sync pipeline (G69/G72).
type SourceSyncConfig struct {
	// Enabled turns on the source sync worker. When false, no automatic
	// sync cycles run, but manual syncs via MCP/REST are still available.
	Enabled bool `toml:"enabled"`
	// IntervalMinutes is how often the sync worker checks for sources
	// that need re-syncing (default 60).
	IntervalMinutes int `toml:"interval_minutes"`
	// MaxConcurrentSyncs is the maximum number of sources that can be
	// synced simultaneously (default 2).
	MaxConcurrentSyncs int `toml:"max_concurrent_syncs"`
	// LicenseAllowlist is the set of SPDX license identifiers that
	// permit redistribution of upstream skill content. An empty upstream
	// license is always treated as NOT allowed. An empty allowlist means
	// ALL licenses are gated (no body redistributed).
	LicenseAllowlist []string `toml:"license_allowlist"`
	// GitHubTokenEnv is the environment variable name that holds the
	// GitHub API token for source sync (default "HELIX_SOURCE_SYNC_GITHUB_TOKEN").
	GitHubTokenEnv string `toml:"github_token_env"`
}

// ---------------------------------------------------------------------------
// Defaults
// ---------------------------------------------------------------------------

func defaultConfig() Config {
	return Config{
		Server: ServerConfig{
			Host:         "0.0.0.0",
			HTTPPort:     8080,
			HTTP3Port:    8443,
			EnableHTTP3:  false,
			EnableBrotli: true,
			RateLimit: RateLimitConfig{
				Enabled:           true,
				RequestsPerSecond: 50,
				Burst:             100,
				TTL:               10 * time.Minute,
				MaxClients:        100000,
			},
			MaxRequestBodyBytes: 100 * 1024 * 1024, // 100 MiB (matches §G22 design)
		},
		Database: DatabaseConfig{
			Host:           "localhost",
			Port:           5432,
			Database:       "skilldb",
			User:           "skill",
			Password:       "secret",
			SSLMode:        "disable",
			MaxConnections: 25,
			ConnectTimeout: 10 * time.Second,
		},
		Embedding: EmbeddingConfig{
			Provider:   "openai",
			Dimensions: 768,
			Model:      "text-embedding-3-small",
		},
		Validation: ValidationConfig{
			Enabled:             true,
			JurySize:            3,
			ApprovalThreshold:   2,
			AutoApproveEvidence: false,
			RequireHumanReview:  true,
		},
		AutoExpand: AutoExpandConfig{
			Enabled:            true,
			MaxDepth:           5,
			MaxNewSkillsPerRun: 10,
			LLMProvider:        "openai",
			LLMModel:           "gpt-4o-mini",
		},
		CodeAnalysis: CodeAnalysisConfig{
			Enabled:         true,
			Languages:       []string{"java", "kotlin", "c", "cpp", "python", "go"},
			MaxFileSizeKB:   500,
			ExcludePatterns: []string{"vendor/", "node_modules/", ".git/", "build/"},
		},
		CodeGraph: CodeGraphConfig{
			Enabled:             true,
			Transport:           "stdio",
			Endpoint:            "",
			SyncIntervalSeconds: 300,
			AutoIndexOnLearn:    true,
			WatchEnabled:        false,
		},
		MCP: MCPConfig{
			Enabled:   true,
			Transport: "stdio",
		},
		Registry: RegistryConfig{
			ReviewIntervalHours: 24,
			CoverageThreshold:   0.8,
		},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Cache: CacheConfig{
			Enabled:   false, // disabled by default — opt-in
			SkillTTL:  5 * time.Minute,
			SearchTTL: 1 * time.Minute,
			TreeTTL:   10 * time.Minute,
		},
		Metrics: MetricsConfig{
			Enabled: false, // disabled by default — opt-in
			Path:    "/metrics",
		},
		Tenant: TenantConfig{
			RateLimit: TenantRateLimitConfig{
				Enabled:           false, // disabled by default — opt-in
				RequestsPerMinute: 60,
				BurstSize:         10,
			},
		},
		SourceSync: SourceSyncConfig{
			Enabled:            false, // disabled by default — opt-in
			IntervalMinutes:    60,
			MaxConcurrentSyncs: 2,
			LicenseAllowlist:   nil, // all licenses gated by default
			GitHubTokenEnv:     "HELIX_SOURCE_SYNC_GITHUB_TOKEN",
		},
	}
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// Load reads a TOML configuration file, applies environment-variable
// substitution on all string fields, and returns the populated Config.
//
// Environment variables use the ${VAR} syntax. A default value can be
// provided with ${VAR:-default}: the default is used only when VAR is unset;
// a variable explicitly set to the empty string is honored as an empty
// override (never replaced by the default). If the variable is unset and no
// default is given, the empty string is substituted.
//
// If path is empty, Load searches for config.toml in the current directory,
// then config/config.toml, then /etc/helixskill/config.toml.
func Load(path string) (*Config, error) {
	if path == "" {
		candidates := []string{
			"config.toml",
			"config/config.toml",
			"/etc/helixskill/config.toml",
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				path = c
				break
			}
		}
	}
	if path == "" {
		return nil, fmt.Errorf("no configuration file found")
	}

	path, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}

	cfg := defaultConfig()

	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("decode TOML config from %q: %w", path, err)
	}

	// Apply environment-variable substitution.
	if err := substituteEnv(&cfg); err != nil {
		return nil, fmt.Errorf("environment variable substitution: %w", err)
	}

	// Apply explicit environment overrides.
	applyEnvOverrides(&cfg)

	// Validate critical fields.
	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}

	return &cfg, nil
}

// ---------------------------------------------------------------------------
// Environment variable substitution (${VAR} / ${VAR:-default})
// ---------------------------------------------------------------------------

// substituteEnv walks the Config struct and replaces ${VAR} placeholders
// in every string field with the corresponding environment variable value.
func substituteEnv(cfg *Config) error {
	var errs []string

	sub := func(v string) string {
		replaced, err := interpolate(v)
		if err != nil {
			errs = append(errs, err.Error())
			return v
		}
		return replaced
	}

	// Server
	cfg.Server.Host = sub(cfg.Server.Host)
	cfg.Server.TLSCert = sub(cfg.Server.TLSCert)
	cfg.Server.TLSKey = sub(cfg.Server.TLSKey)
	// Server list fields honor the same ${VAR} promise as scalar fields so a
	// TOML entry like api_keys = ["${PROD_KEY}"] is interpolated from the
	// environment rather than stored as a literal (and dangerously valid) key.
	for i := range cfg.Server.APIKeys {
		cfg.Server.APIKeys[i] = sub(cfg.Server.APIKeys[i])
	}
	for i := range cfg.Server.AllowedOrigins {
		cfg.Server.AllowedOrigins[i] = sub(cfg.Server.AllowedOrigins[i])
	}

	// Database
	cfg.Database.Host = sub(cfg.Database.Host)
	cfg.Database.Database = sub(cfg.Database.Database)
	cfg.Database.User = sub(cfg.Database.User)
	cfg.Database.Password = sub(cfg.Database.Password)
	cfg.Database.SSLMode = sub(cfg.Database.SSLMode)

	// Embedding
	cfg.Embedding.Provider = sub(cfg.Embedding.Provider)
	cfg.Embedding.Model = sub(cfg.Embedding.Model)
	cfg.Embedding.APIKey = sub(cfg.Embedding.APIKey)
	cfg.Embedding.LocalEndpoint = sub(cfg.Embedding.LocalEndpoint)

	// AutoExpand
	cfg.AutoExpand.LLMProvider = sub(cfg.AutoExpand.LLMProvider)
	cfg.AutoExpand.LLMModel = sub(cfg.AutoExpand.LLMModel)
	cfg.AutoExpand.LLMAPIKey = sub(cfg.AutoExpand.LLMAPIKey)
	cfg.AutoExpand.LLMBaseURL = sub(cfg.AutoExpand.LLMBaseURL)

	// CodeAnalysis
	cfg.CodeAnalysis.AllowedRoot = sub(cfg.CodeAnalysis.AllowedRoot)

	// CodeGraph
	cfg.CodeGraph.Transport = sub(cfg.CodeGraph.Transport)
	cfg.CodeGraph.Endpoint = sub(cfg.CodeGraph.Endpoint)

	// MCP
	cfg.MCP.Transport = sub(cfg.MCP.Transport)

	// Logging
	cfg.Logging.Level = sub(cfg.Logging.Level)
	cfg.Logging.Format = sub(cfg.Logging.Format)

	// Cache
	cfg.Cache.RedisURL = sub(cfg.Cache.RedisURL)

	// Replica
	cfg.Database.Replica.DSN = sub(cfg.Database.Replica.DSN)

	// Metrics
	cfg.Metrics.Path = sub(cfg.Metrics.Path)

	if len(errs) > 0 {
		return fmt.Errorf("errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// interpolate replaces all ${VAR} occurrences in s with their environment
// variable values. Supports ${VAR:-default} syntax.
func interpolate(s string) (string, error) {
	result := envVarRegex.ReplaceAllStringFunc(s, func(match string) string {
		inner := match[2 : len(match)-1] // strip ${ and }

		// Check for default syntax: VAR:-default
		var envKey, defaultVal string
		if idx := strings.Index(inner, ":-"); idx >= 0 {
			envKey = inner[:idx]
			defaultVal = inner[idx+2:]
		} else {
			envKey = inner
		}

		// G26: os.LookupEnv distinguishes an unset variable (ok == false) from
		// one explicitly set to the empty string (ok == true, v == ""). A
		// present-but-empty variable is an intentional override and is honored
		// as-is; only a genuinely unset variable falls back to the default.
		if v, ok := os.LookupEnv(envKey); ok {
			return v
		}
		if defaultVal != "" {
			return defaultVal
		}
		return ""
	})
	return result, nil
}

// ---------------------------------------------------------------------------
// Explicit environment overrides (HELIX_* prefix)
// ---------------------------------------------------------------------------

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("HELIX_DB_HOST"); v != "" {
		cfg.Database.Host = v
	}
	if v := os.Getenv("HELIX_DB_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.Database.Port = port
		}
	}
	if v := os.Getenv("HELIX_DB_NAME"); v != "" {
		cfg.Database.Database = v
	}
	if v := os.Getenv("HELIX_DB_USER"); v != "" {
		cfg.Database.User = v
	}
	if v := os.Getenv("HELIX_DB_PASSWORD"); v != "" {
		cfg.Database.Password = v
	}
	if v := os.Getenv("HELIX_DB_SSLMODE"); v != "" {
		cfg.Database.SSLMode = v
	}
	if v := os.Getenv("HELIX_LOG_LEVEL"); v != "" {
		cfg.Logging.Level = v
	}
	if v := os.Getenv("HELIX_MCP_TRANSPORT"); v != "" {
		cfg.MCP.Transport = v
	}
	if v := os.Getenv("HELIX_API_KEYS"); v != "" {
		cfg.Server.APIKeys = splitAndTrim(v)
	}
	if v := os.Getenv("HELIX_AUTH_DISABLED"); v != "" {
		cfg.Server.AuthDisabled = v == "1" || strings.EqualFold(v, "true")
	}
	if v := os.Getenv("HELIX_CODEANALYSIS_ALLOWED_ROOT"); v != "" {
		cfg.CodeAnalysis.AllowedRoot = v
	}
	if v := os.Getenv("HELIX_REDIS_URL"); v != "" {
		cfg.Cache.RedisURL = v
	}
	if v := os.Getenv("HELIX_CACHE_ENABLED"); v != "" {
		cfg.Cache.Enabled = v == "1" || strings.EqualFold(v, "true")
	}
	if v := os.Getenv("HELIX_METRICS_ENABLED"); v != "" {
		cfg.Metrics.Enabled = v == "1" || strings.EqualFold(v, "true")
	}
	if v := os.Getenv("HELIX_REPLICA_DSN"); v != "" {
		cfg.Database.Replica.DSN = v
	}
}

// splitAndTrim splits a comma-separated string into non-empty, trimmed values.
// Used for list-valued environment overrides such as HELIX_API_KEYS.
func splitAndTrim(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

func validate(cfg *Config) error {
	var issues []string

	if cfg.Server.HTTPPort <= 0 || cfg.Server.HTTPPort > 65535 {
		issues = append(issues, fmt.Sprintf("invalid server.http_port: %d", cfg.Server.HTTPPort))
	}
	if cfg.Server.HTTP3Port <= 0 || cfg.Server.HTTP3Port > 65535 {
		issues = append(issues, fmt.Sprintf("invalid server.http3_port: %d", cfg.Server.HTTP3Port))
	}

	// Defense-in-depth (§11.4.10): an api_keys/allowed_origins entry that still
	// contains a "${" placeholder AFTER interpolation means a referenced
	// environment variable was never set (or the placeholder was malformed).
	// Fail CLOSED rather than treat the literal placeholder as a valid secret
	// or origin. Values are never echoed — only the field name and index.
	for i, k := range cfg.Server.APIKeys {
		if strings.Contains(k, "${") {
			issues = append(issues, fmt.Sprintf("server.api_keys[%d] contains an uninterpolated ${...} placeholder (set the referenced environment variable or remove the entry)", i))
		}
	}
	for i, o := range cfg.Server.AllowedOrigins {
		if strings.Contains(o, "${") {
			issues = append(issues, fmt.Sprintf("server.allowed_origins[%d] contains an uninterpolated ${...} placeholder", i))
		}
	}

	if cfg.Database.Port <= 0 || cfg.Database.Port > 65535 {
		issues = append(issues, fmt.Sprintf("invalid database.port: %d", cfg.Database.Port))
	}
	if cfg.Database.MaxConnections <= 0 {
		cfg.Database.MaxConnections = 25 // apply safe default
	}

	if cfg.Embedding.Dimensions <= 0 {
		issues = append(issues, fmt.Sprintf("invalid embedding.dimensions: %d", cfg.Embedding.Dimensions))
	}

	if cfg.Validation.JurySize <= 0 {
		issues = append(issues, fmt.Sprintf("invalid validation.jury_size: %d", cfg.Validation.JurySize))
	}
	if cfg.Validation.ApprovalThreshold <= 0 {
		issues = append(issues, fmt.Sprintf("invalid validation.approval_threshold: %d", cfg.Validation.ApprovalThreshold))
	}

	if cfg.AutoExpand.MaxDepth <= 0 {
		issues = append(issues, fmt.Sprintf("invalid autoexpand.max_depth: %d", cfg.AutoExpand.MaxDepth))
	}
	if cfg.AutoExpand.MaxNewSkillsPerRun <= 0 {
		issues = append(issues, fmt.Sprintf("invalid autoexpand.max_new_skills_per_run: %d", cfg.AutoExpand.MaxNewSkillsPerRun))
	}

	if cfg.Registry.CoverageThreshold < 0 || cfg.Registry.CoverageThreshold > 1 {
		issues = append(issues, fmt.Sprintf("invalid registry.coverage_threshold: %f (must be 0-1)", cfg.Registry.CoverageThreshold))
	}

	// G17: Validate closed-set enum fields (ops_hardening_design.md §3.2).
	// A typo in any of these fails late, deep in the call stack; catching it
	// at startup with a clear message is the entire point.
	validEmbeddingProviders := map[string]bool{"openai": true, "local": true, "helixllm": true}
	if cfg.Embedding.Provider != "" && !validEmbeddingProviders[cfg.Embedding.Provider] {
		issues = append(issues, fmt.Sprintf("invalid embedding.provider: %q (must be one of: openai, local, helixllm)", cfg.Embedding.Provider))
	}

	validLLMProviders := map[string]bool{"openai": true, "anthropic": true, "local": true, "helixllm": true}
	if cfg.AutoExpand.LLMProvider != "" && !validLLMProviders[cfg.AutoExpand.LLMProvider] {
		issues = append(issues, fmt.Sprintf("invalid autoexpand.llm_provider: %q (must be one of: openai, anthropic, local, helixllm)", cfg.AutoExpand.LLMProvider))
	}

	validTransports := map[string]bool{"stdio": true, "http": true, "acp": true}
	if cfg.MCP.Transport != "" && !validTransports[cfg.MCP.Transport] {
		issues = append(issues, fmt.Sprintf("invalid mcp.transport: %q (must be one of: stdio, http, acp)", cfg.MCP.Transport))
	}

	validLogLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	if cfg.Logging.Level != "" && !validLogLevels[cfg.Logging.Level] {
		issues = append(issues, fmt.Sprintf("invalid logging.level: %q (must be one of: debug, info, warn, error)", cfg.Logging.Level))
	}

	if len(issues) > 0 {
		return fmt.Errorf("validation failed: %s", strings.Join(issues, "; "))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helper methods on Config
// ---------------------------------------------------------------------------

// ListenAddr returns the HTTP listen address in the form ":port".
func (c *Config) ListenAddr() string {
	return ":" + strconv.Itoa(c.Server.HTTPPort)
}

// HTTP3ListenAddr returns the HTTP/3 listen address.
func (c *Config) HTTP3ListenAddr() string {
	return ":" + strconv.Itoa(c.Server.HTTP3Port)
}
