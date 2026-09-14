// cmd/server is the main entry point for the HelixKnowledge Skill System API and MCP server.
//
// Usage:
//
//	go run ./cmd/server [--config path/to/config.toml] [--mcp stdio|http|both|acp]
//
// Modes:
//   - API mode (default): Runs the HTTP REST API server on the configured port
//   - MCP stdio mode (--mcp stdio): Runs the MCP server over stdin/stdout for CLI agents
//   - MCP HTTP mode (--mcp http): Runs the MCP server over HTTP/SSE
//   - MCP both mode (--mcp both): Runs both stdio and HTTP transports
//   - MCP acp mode (--mcp acp): Runs the Agent Client Protocol (ACP) adapter
//     over stdin/stdout for ACP-speaking CLI agents (no HTTP listener)
//
// Environment variables:
//
//	HELIX_DB_HOST, HELIX_DB_PORT, HELIX_DB_NAME, HELIX_DB_USER, HELIX_DB_PASSWORD
//	HELIX_LOG_LEVEL, HELIX_MCP_TRANSPORT
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	skillsystem "github.com/helixdevelopment/skill-system"
	"github.com/helixdevelopment/skill-system/internal/api"
	"github.com/helixdevelopment/skill-system/internal/config"
	"github.com/helixdevelopment/skill-system/internal/db"
	"github.com/helixdevelopment/skill-system/internal/mcp"
	"github.com/helixdevelopment/skill-system/internal/models"
	"github.com/helixdevelopment/skill-system/internal/registry"
	"github.com/helixdevelopment/skill-system/internal/skill"
	"github.com/helixdevelopment/skill-system/internal/skillsource"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func main() {
	// Parse command-line flags
	configPath := flag.String("config", "", "Path to TOML config file (optional)")
	mcpTransport := flag.String("mcp", "", "MCP transport: stdio, http, both, acp (overrides config)")
	flag.Parse()

	// 1. Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Override MCP transport from CLI flag
	if *mcpTransport != "" {
		cfg.MCP.Transport = *mcpTransport
	}

	// 2. Initialize logger
	logger, err := newLogger(cfg.Logging)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	logger.Info("HelixKnowledge Skill System starting",
		zap.String("version", "1.0.0"),
		zap.String("mcp_transport", cfg.MCP.Transport),
	)

	// 3. Connect to database (db.New takes config value, no context needed)
	pool, err := db.New(cfg.Database)
	if err != nil {
		logger.Fatal("Failed to connect to database", zap.Error(err))
	}
	defer pool.Close()

	logger.Info("Database connected",
		zap.String("host", cfg.Database.Host),
		zap.Int("port", cfg.Database.Port),
		zap.String("database", cfg.Database.Database),
	)

	// 4. Run migrations from the EMBEDDED FS, FAIL-CLOSED before binding the
	// port (§G23). Previously this passed a cwd-relative "./migrations" and only
	// logged a Warn on failure, so a binary started from the wrong directory
	// applied nothing, and a failed migration was swallowed — the server then
	// bound the port and served every query against an un-migrated / partially
	// migrated schema (a §11.4.108 "runs green but broken" hazard). The embedded
	// FS makes discovery cwd-independent, and a non-nil error is now fatal:
	// a server that cannot bring its schema to the required version is NOT
	// serving-ready and MUST NOT pretend to be (mirrors the fatal-on-bind-failure
	// discipline in setupAPI).
	ctx := context.Background()
	if err := migrateOnStartup(ctx, pool, startupMigrationsFS(), logger); err != nil {
		logger.Fatal("Database migration failed; refusing to serve", zap.Error(err))
	}

	// 4.5. G10 boot-time embedding-dimension safety assertion (§11.4.201),
	// runs AFTER migrations (the columns must exist) and BEFORE anything reads
	// or writes an embedding column. See assertEmbeddingDimensionsOnStartup's
	// doc comment for why this is fail-closed and why it is skipped (never
	// fatal) when no embedding provider is configured.
	if err := assertEmbeddingDimensionsOnStartup(ctx, pool, cfg.Embedding, logger); err != nil {
		logger.Fatal("Embedding dimension safety check failed; refusing to serve", zap.Error(err))
	}

	// 5. Initialize skill store and registry
	skillStore := skill.NewStore(pool)
	skillRegistry := registry.NewRegistry(pool)

	logger.Info("Skill store and registry initialized")

	// 6. Setup MCP server
	mcpServer := mcp.NewMCPServer(pool, skillStore, skillRegistry, cfg, logger)
	mcpServer.RegisterTools()

	// 7. Run based on transport mode
	mode := cfg.MCP.Transport

	switch mode {
	case "stdio":
		// Pure MCP stdio mode - no HTTP listener at all.
		logger.Info("Running in MCP stdio mode (stdout reserved for JSON-RPC)")
		if _, err := runBlockingTransport(mode, mcpServer); err != nil {
			logger.Fatal("MCP stdio server failed", zap.Error(err))
		}

	case "acp":
		// Pure ACP (Agent Client Protocol) mode - no HTTP listener at all.
		// Wires the previously-unwired internal/mcp/acp_adapter.go in as a
		// selectable transport, mirroring the "stdio" case above exactly
		// (same blocking, stdout-reserved-for-JSON-RPC discipline; only the
		// wire protocol dialect differs).
		logger.Info("MCP acp mode (stdout reserved for JSON-RPC)")
		if _, err := runBlockingTransport(mode, mcpServer); err != nil {
			logger.Fatal("MCP acp server failed", zap.Error(err))
		}

	case "both":
		// ONE hardened HTTP listener (background goroutine, MCP routes mounted +
		// auth-guarded) PLUS stdio in the foreground (blocking). There is no
		// second HTTP listener: the MCP HTTP surface lives on the same router as
		// /api/v1 under the same fail-closed policy.
		srv, tenantCleanup := setupAPI(cfg, pool, skillStore, skillRegistry, mcpServer, logger)
		if err := mcpServer.RunStdio(); err != nil {
			logger.Fatal("MCP stdio server failed", zap.Error(err))
		}
		// stdio ended (EOF/stop): drain the HTTP server and the MCP server.
		gracefulShutdown(ctx, logger, mcpServer, srv, tenantCleanup)

	default:
		// "http" and the standard API mode both serve a SINGLE hardened HTTP
		// listener with the MCP routes mounted and auth-guarded, then block
		// until a shutdown signal. Exactly one server binds the port.
		srv, tenantCleanup := setupAPI(cfg, pool, skillStore, skillRegistry, mcpServer, logger)
		waitForShutdown(ctx, logger, mcpServer, srv, tenantCleanup)
	}
}

// startupMigrationsFS is the migration source cmd/server applies at startup:
// the EMBEDDED migrations FS (skillsystem.MigrationsFS), so migrations travel
// with the binary and are discovered independently of the process working
// directory (§G23). Exposed as a function (rather than inlining the reference in
// main) so a test can assert startup wires the embedded source — a regression to
// a cwd-relative os.DirFS("./migrations") here (or a call-site swap in main)
// stops applying migrations when the binary runs from any other directory, which
// the §G23 cwd-independence test catches.
func startupMigrationsFS() fs.FS { return skillsystem.MigrationsFS }

// migrateOnStartup applies the migrations in fsys and returns an error on ANY
// failure so main() can FAIL-CLOSED (abort before binding the port, §G23). It
// MUST NOT swallow a migration error (the pre-G23 warn-and-continue behaviour):
// a returned nil means the schema is at the required version and the server is
// safe to serve. Exposed as a package function so tests exercise the real
// embed-FS apply + error-propagation path in-process without spawning the
// binary.
func migrateOnStartup(ctx context.Context, pool *db.Pool, fsys fs.FS, logger *zap.Logger) error {
	if err := db.MigrateFS(ctx, pool, fsys); err != nil {
		return fmt.Errorf("apply embedded migrations: %w", err)
	}
	logger.Info("Migrations completed")
	return nil
}

// embeddingDimensionCheckTargets is the closed, ordered set of table.column
// pairs the G10 boot-time assertion covers -- every pgvector column an
// embedder's output can be written into (db.StoreSkillEmbedding /
// db.StoreEvidenceEmbedding) or read via KNN (db.FindSimilarSkills /
// db.FindSimilarEvidences, skill.Store.VectorSearch), per
// migrations/001_initial.up.sql:14,60 (both declared vector(768)). Exposed as
// a package-level var (rather than inlined in the loop) so a test can assert
// on the exact target list without duplicating it.
var embeddingDimensionCheckTargets = []struct{ Table, Column string }{
	{Table: "skills", Column: "embedding"},
	{Table: "evidences", Column: "embedding"},
}

// assertEmbeddingDimensionsOnStartup is the G10 boot-time safety guard
// (research/g10_embedding_provider_design.md §2.2, §11.4.201). It is called
// from main() AFTER migrations (so the target columns exist) and BEFORE any
// skill/evidence store or MCP server construction that could read or write an
// embedding.
//
// "Is an embedding provider configured" is derived SOLELY from
// db.NewEmbedderFromConfig's own error return -- the SAME single source of
// truth internal/mcp.NewMCPServer already relies on for its own
// store.WithEmbedder wiring (see that constructor's doc comment; Fable
// code-review remediation, finding 6a). Calling the factory again here is NOT
// a second, hand-duplicated "is it configured" policy: NewEmbedderFromConfig
// merely constructs a plain Go struct from cfg (no network I/O, no side
// effect), so invoking it a second time to read the resulting embedder's
// Dimensions() costs nothing and cannot drift from NewMCPServer's own
// decision. When no provider is configured (or misconfigured -- e.g. a
// missing OpenAI api_key), the factory itself errors and NewMCPServer never
// wires an embedder anywhere in the process; there is nothing to assert a
// dimension for, so the check is skipped (never fatal) rather than blocking
// startup on an intentionally embedder-less deployment.
//
// A configured-but-mismatched embedder is a hard, fail-closed startup error
// (never a logger.Warn-and-continue): every StoreSkillEmbedding /
// StoreEvidenceEmbedding insert and every KNN search (VectorSearch /
// HybridSearch) against a mismatched column would otherwise be silently
// broken -- either erroring opaquely deep in a background worker/API request,
// or (if pgvector still let the erroneous data through some future dimension-
// widening path) corrupting distance calculations. Catching it here, once, at
// boot, with both dimensions and their sources named in the error, is the
// entire point of this guard.
func assertEmbeddingDimensionsOnStartup(ctx context.Context, pool *db.Pool, cfg config.EmbeddingConfig, logger *zap.Logger) error {
	emb, err := db.NewEmbedderFromConfig(cfg)
	if err != nil {
		logger.Debug("embedding dimension check skipped: no embedding provider configured", zap.Error(err))
		return nil
	}

	wantDim := emb.Dimensions()
	for _, target := range embeddingDimensionCheckTargets {
		if err := db.AssertEmbeddingDimension(ctx, pool, target.Table, target.Column, wantDim); err != nil {
			return err
		}
		logger.Info("embedding dimension verified",
			zap.String("table", target.Table),
			zap.String("column", target.Column),
			zap.Int("dimension", wantDim))
	}
	return nil
}

// healthPinger is the minimal database dependency the OPEN /health handler
// needs. *db.Pool satisfies it via its own Health method. Abstracting this
// out (rather than depending on *db.Pool directly) lets a hermetic unit test
// exercise the handler's error-redaction behavior with a fake unhealthy
// dependency that returns a realistic pgx/pgxpool-shaped connection error —
// without needing an actually-broken database connection.
type healthPinger interface {
	Health(ctx context.Context) error
}

// newHealthHandler returns the OPEN, unauthenticated /health liveness-probe
// handler. It MUST be registered directly on the router (never behind
// authMW) so an orchestrator/systemd liveness check reaches it without an
// API key.
//
// SECURITY (§G24 finding 3): pgx/pgxpool connection-error strings routinely
// embed the live DSN's connection details — e.g. "database health check
// failed: failed to connect to `host=127.0.0.1 user=skillsys
// database=postgres`: dial error ...". Emitting that verbatim on this OPEN
// surface would disclose host/user/password details to ANY unauthenticated
// caller whenever the database is down. The full error is therefore logged
// server-side ONLY (zap); the wire response carries just a coarse
// "ok"/"error" constant, matching the redacted api/openapi.yaml
// HealthResponse.database contract. The overall "status"/200 behavior is
// intentionally left unchanged here (it does not vary with dbStatus) — see
// the api/openapi.yaml HealthResponse description for the tracked
// served-vs-dead-handler duplication this does not attempt to unify.
func newHealthHandler(pool healthPinger, transport string, logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		dbStatus := "ok"
		if err := pool.Health(ctx); err != nil {
			dbStatus = "error"
			logger.Warn("health check: database ping failed", zap.Error(err))
		}

		c.JSON(http.StatusOK, gin.H{
			"status":    "healthy",
			"server":    "helix-knowledge-skill-system",
			"version":   "1.0.0",
			"database":  dbStatus,
			"transport": transport,
		})
	}
}

// buildRouter assembles the SINGLE hardened Gin router used by every
// HTTP-serving mode. It wires the config-driven CORS allowlist, resolves the
// fail-closed API-key auth ONCE, and guards BOTH the /api/v1 data routes AND the
// mounted MCP /mcp/v1 routes with that same middleware. /health and / are the
// only open routes. No listener is started here (see setupAPI) so this assembly
// is directly unit-testable.
func buildRouter(cfg *config.Config, pool *db.Pool, store *skill.Store, reg *registry.Registry, mcpServer *mcp.MCPServer, logger *zap.Logger) (*gin.Engine, func()) {
	if cfg.Server.EnableBrotli {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	// Trust NO forwarding proxy (F1). In the R15 no-proxy single-node topology
	// the app is reached directly, so c.ClientIP() MUST resolve to the real TCP
	// socket peer and NEVER to a client-supplied X-Forwarded-For / X-Real-IP. Gin
	// otherwise trusts all proxies by default, which would let an unauthenticated
	// caller spoof its rate-limit identity by rotating a forwarding header.
	// ForwardedByClientIP=false short-circuits header parsing; SetTrustedProxies(nil)
	// additionally empties the trusted set (belt-and-suspenders).
	router.ForwardedByClientIP = false
	if err := router.SetTrustedProxies(nil); err != nil {
		logger.Error("failed to clear trusted proxies (rate-limit identity hardening)", zap.Error(err))
	}
	router.Use(gin.Recovery())
	router.Use(apiLoggingMiddleware(logger))
	// Hardened, config-driven CORS allowlist (internal/api.CORS): an empty
	// allowlist is fail-closed and no wildcard "*" origin is ever emitted with
	// credentials. This replaces the previous wildcard corsMiddleware().
	router.Use(api.CORS(cfg.Server.AllowedOrigins))

	// Per-client token-bucket rate limiting (§G22), installed BEFORE auth so a
	// flood is throttled with 429 ahead of any credential work. Keyed ONLY on the
	// real socket peer (never an attacker-controlled header, F1) with a map that
	// is hard-bounded by MaxClients (F2), so one client cannot starve another and
	// a distinct-IP flood cannot grow the tracking map without bound. Off only
	// when explicitly disabled in config.
	if cfg.Server.RateLimit.Enabled {
		limiter := api.NewRateLimiter(api.RateLimitConfig{
			RequestsPerSecond: cfg.Server.RateLimit.RequestsPerSecond,
			Burst:             cfg.Server.RateLimit.Burst,
			TTL:               cfg.Server.RateLimit.TTL,
			MaxClients:        cfg.Server.RateLimit.MaxClients,
		})
		router.Use(limiter.Middleware())
	}

	// Request-body cap (§G22): reject an oversized body with 413 BEFORE auth so
	// an unauthenticated flood of huge bodies cannot exhaust memory. A
	// non-positive config value falls back to the 100 MiB default.
	maxBody := cfg.Server.MaxRequestBodyBytes
	if maxBody <= 0 {
		maxBody = api.DefaultMaxBodyBytes
	}
	router.Use(api.MaxBodySize(maxBody))

	// Resolve the fail-closed auth middleware ONCE and share the SAME instance
	// across BOTH the /api/v1 data routes and the mounted MCP /mcp/v1 routes so
	// the two surfaces enforce identical authentication (and the startup log
	// fires exactly once). ResolveAPIKeyAuth is fail-CLOSED: with no API keys
	// and auth not explicitly disabled it rejects every request (503) rather
	// than serving these routes open. nil is returned ONLY in the explicit
	// auth-disabled mode.
	authMW := api.ResolveAPIKeyAuth(cfg.Server.APIKeys, cfg.Server.AuthDisabled, logger)

	// Health check (open, unauthenticated liveness probe). Wired via
	// newHealthHandler (below) rather than an inline closure so the
	// error-redaction behavior is independently unit-testable without a live
	// database connection (§G24 finding 3 remediation).
	router.GET("/health", newHealthHandler(pool, cfg.MCP.Transport, logger))

	// System telemetry endpoints (§G24). Registered UNDER the SAME fail-closed
	// authMW as /api/v1 — never at the router root — so an anonymous scrape of
	// the Prometheus exposition (goroutine/memory/uptime gauges, Go runtime
	// internals) or the build/version info is DENIED (401 with keys configured,
	// 503 when auth is unconfigured-and-not-disabled) instead of leaking internal
	// telemetry and version strings to any caller. /health stays OPEN above for
	// liveness probes — the standard open-liveness / gated-telemetry split. This
	// also aligns the live route surface with api/openapi.yaml, which marks
	// /metrics and /version as 401 (ApiKeyAuth). The dead internal/api/server.go
	// registered these three at the router root OUTSIDE auth; that path is not the
	// wired one, so the fix lands here on the LIVE buildRouter (§11.4.108).
	sys := router.Group("/")
	if authMW != nil {
		sys.Use(authMW)
	}
	sys.GET("/metrics", api.MetricsHandler())
	sys.GET("/version", api.VersionHandler())

	// All /api/v1 data routes are authenticated under the fail-closed guard.
	v1 := router.Group("/api/v1")
	if authMW != nil {
		v1.Use(authMW)
	}

	// Tenant context middleware (§11.4.84, 004_enterprise). Injects the db.Pool
	// into the request context first, then resolves the tenant from the request.
	// When cfg.Tenant.Required is false (default), unscoped requests pass through
	// for backward compatibility with single-tenant deployments.
	v1.Use(api.WithDBPoolMiddleware(pool))
	if cfg.Tenant.Required || cfg.Tenant.DefaultTenant != "" {
		v1.Use(api.TenantMiddleware(cfg.Tenant))
	}

	// Per-tenant rate limiting (§11.4.84). Each tenant identified by
	// TenantMiddleware receives an independent token-bucket limiter. Disabled
	// by default — operators enable it via [tenant.rate_limit] in config.
	var tenantRateLimiter *api.TenantRateLimiter
	if cfg.Tenant.RateLimit.Enabled {
		tenantRateLimiter = api.NewTenantRateLimiter(api.TenantRateLimitConfig{
			RequestsPerMinute: cfg.Tenant.RateLimit.RequestsPerMinute,
			BurstSize:         cfg.Tenant.RateLimit.BurstSize,
		})
		v1.Use(api.TenantRateLimitMiddleware(tenantRateLimiter, nil))
	}

	// Tenant audit logging (§11.4.84). Records every tenant-scoped API request
	// into the tenant_audit_log table. Runs whenever tenant resolution is
	// active (Required or DefaultTenant configured).
	var auditLogger api.AuditLogger
	if cfg.Tenant.Required || cfg.Tenant.DefaultTenant != "" {
		auditLogger = api.NewDBAuditLogger(pool, logger)
		v1.Use(api.TenantAuditMiddleware(auditLogger, nil))
	}

	// tenantSkillStore returns a *skill.TenantStore when the request carries a
	// resolved tenant context, nil otherwise.  Handlers call this once at the
	// top and branch: tenant-scoped queries when non-nil, existing unscoped
	// queries when nil (single-tenant backward compat).
	tenantSkillStore := func(c *gin.Context) *skill.TenantStore {
		if _, ok := skill.TenantFromContext(c.Request.Context()); ok {
			return skill.NewTenantStore(pool, logger)
		}
		return nil
	}

	// Skills CRUD (served via inline closures — these replace the previous
	// api.Server.SetupRoutes path that was removed during G01 consolidation.
	// The Server struct's Pool interface is satisfied across db.Pool +
	// skill.Store + registry.Registry, so a future consolidation will wire
	// them into a single handler; for now, the three-component buildRouter
	// variables (pool, store, reg) serve the inline closures directly, which
	// is correct and symmetrical with the coverage/missing routes below.
	skills := v1.Group("/skills")
	{
		skills.GET("", func(c *gin.Context) {
			ctx := c.Request.Context()
			if ts := tenantSkillStore(c); ts != nil {
				skills, err := ts.ListSkills(ctx, skill.ListOpts{Limit: 100})
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
				c.JSON(http.StatusOK, gin.H{"skills": skills, "count": len(skills)})
				return
			}
			skills, err := store.ListSkills(ctx, "", 100, 0)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"skills": skills, "count": len(skills)})
		})

		skills.GET("/search", func(c *gin.Context) {
			ctx := c.Request.Context()
			query := c.Query("q")
			if query == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Query parameter 'q' is required"})
				return
			}
			if ts := tenantSkillStore(c); ts != nil {
				tenantSkills, err := ts.SearchSkills(ctx, query, skill.ListOpts{Limit: 50})
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
				results := make([]models.SearchResult, len(tenantSkills))
				for i, sk := range tenantSkills {
					results[i] = models.SearchResult{Skill: sk}
				}
				c.JSON(http.StatusOK, gin.H{"results": results, "query": query})
				return
			}
			results, err := store.Search(ctx, query, 10)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"results": results, "query": query})
		})

		skills.GET("/:name", func(c *gin.Context) {
			ctx := c.Request.Context()
			name := c.Param("name")
			if ts := tenantSkillStore(c); ts != nil {
				sk, err := ts.GetSkill(ctx, name)
				if err != nil {
					if errors.Is(err, skill.ErrSkillNotFound) {
						c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
					} else {
						c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					}
					return
				}
				c.JSON(http.StatusOK, sk)
				return
			}
			sk, err := store.GetByName(ctx, name)
			if err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, sk)
		})

		skills.GET("/:name/tree", func(c *gin.Context) {
			ctx := c.Request.Context()
			name := c.Param("name")
			// When a tenant is active, validate access via TenantStore.GetSkill
			// before building the tree. TenantStore has no GetTree method, so we
			// fall through to the unscoped store for tree construction — the
			// tenant check above ensures the skill belongs to the tenant.
			if ts := tenantSkillStore(c); ts != nil {
				if _, err := ts.GetSkill(ctx, name); err != nil {
					if errors.Is(err, skill.ErrSkillNotFound) {
						c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
					} else {
						c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					}
					return
				}
			}
			depth := 5
			tree, err := store.GetTree(ctx, name, depth)
			if err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, tree)
		})
	}

	// Skill Sources CRUD (G84) — registered skill sources that supply
	// SKILL.md files for the source-ingestion pipeline. Routes are defined in
	// source_routes.go via RegisterSourceRoutes; the orchestrator drives the
	// full fetch -> parse -> map -> dedup -> import sync pipeline.
	sourceStore := skillsource.NewStore(pool, logger)
	sourceOrch := skillsource.NewOrchestrator(sourceStore, store, logger)

	RegisterSourceRoutes(v1, sourceStore, sourceOrch)

	// Coverage API — kept separately from the Server's /api/v1/registry/coverage
	// (which surfaces pool.GetCoverage). This route delegates to the registry
	// layer's GetCoverageReport for domain-scoped coverage data.
	v1.GET("/coverage", func(c *gin.Context) {
		ctx := c.Request.Context()
		domain := c.Query("domain")
		report, err := reg.GetCoverageReport(ctx, domain)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, report)
	})

	// Missing skills API
	v1.GET("/missing", func(c *gin.Context) {
		ctx := c.Request.Context()
		domain := c.Query("domain")
		entries, err := store.GetMissingSkills(ctx, domain)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"missing_skills": entries, "count": len(entries)})
	})

	// Mount the MCP HTTP routes (/mcp/v1/*) onto THIS same router, behind the
	// SAME fail-closed auth guard as /api/v1. Previously these ran on a SECOND
	// http.Server bound to the identical host:port with wildcard CORS and NO
	// authentication; whichever server won the bind decided the live security
	// posture, and the hardened /api/v1 routes 404'd when the MCP one won. One
	// listener now serves both surfaces under one hardened policy.
	mcpServer.RegisterHTTPRoutes(router, authMW)

	// Server info (open) — reflects the real, live, auth-guarded route surface.
	router.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"name":        "HelixKnowledge Skill Graph System",
			"version":     "1.0.0",
			"description": "API and MCP server for AI agent skill management",
			"endpoints": []string{
				"GET  /health",
				"GET  /metrics (Prometheus exposition, auth required)",
				"GET  /version (auth required)",
				"GET  /api/v1/skills (auth required)",
				"GET  /api/v1/skills/search?q=query (auth required)",
				"GET  /api/v1/skills/:name (auth required)",
				"GET  /api/v1/skills/:name/tree (auth required)",
				"POST /api/v1/sources (auth required)",
				"GET  /api/v1/sources (auth required)",
				"GET  /api/v1/sources/:id (auth required)",
				"DELETE /api/v1/sources/:id (auth required)",
				"POST /api/v1/sources/:id/sync (auth required)",
				"GET  /api/v1/coverage?domain=optional (auth required)",
				"GET  /api/v1/missing?domain=optional (auth required)",
				"POST /mcp/v1/messages (JSON-RPC, auth required)",
				"GET  /mcp/v1/sse (SSE streaming, auth required)",
				"GET  /mcp/v1/tools (auth required)",
				"POST /mcp/v1/tools/:name/call (auth required)",
				"GET  /mcp/v1/prompts (auth required)",
				"GET  /mcp/v1/prompts/:name (auth required)",
			},
		})
	})

	// Cleanup function for graceful shutdown — stops background goroutines
	// started by per-tenant middleware (rate limiter reaper, audit flusher).
	tenantCleanup := func() {
		if tenantRateLimiter != nil {
			tenantRateLimiter.Stop()
		}
		if auditLogger != nil {
			auditLogger.Stop()
		}
	}

	return router, tenantCleanup
}

// setupAPI builds the single hardened router and starts the ONE HTTP listener,
// returning the *http.Server so the caller can shut it down gracefully. A bind
// failure is FATAL: a server that cannot bind its port is NOT serving, and the
// process must not continue as if it were (that is exactly the silent
// "fixed-but-not-live" failure this remediation closes).
func setupAPI(cfg *config.Config, pool *db.Pool, store *skill.Store, reg *registry.Registry, mcpServer *mcp.MCPServer, logger *zap.Logger) (*http.Server, func()) {
	router, tenantCleanup := buildRouter(cfg, pool, store, reg, mcpServer, logger)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.HTTPPort)
	srv := &http.Server{
		Addr:         addr,
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	logger.Info("API server starting (single hardened listener; MCP mounted + auth-guarded)",
		zap.String("addr", addr))

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			// A failed bind (e.g. address already in use) means we are NOT
			// serving — fail hard instead of silently continuing.
			logger.Fatal("API server failed", zap.String("addr", addr), zap.Error(err))
		}
	}()

	return srv, tenantCleanup
}

// waitForShutdown blocks until a shutdown signal is received, then drains both
// the HTTP server and the MCP server.
func waitForShutdown(ctx context.Context, logger *zap.Logger, mcpServer *mcp.MCPServer, srv *http.Server, tenantCleanup func()) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	logger.Info("Waiting for shutdown signal (SIGTERM/SIGINT)")
	sig := <-sigCh
	logger.Info("Received shutdown signal", zap.String("signal", sig.String()))

	gracefulShutdown(ctx, logger, mcpServer, srv, tenantCleanup)
}

// gracefulShutdown drains the HTTP listener (triggering ListenAndServe to return
// http.ErrServerClosed) and then the MCP server, within a bounded timeout.
func gracefulShutdown(ctx context.Context, logger *zap.Logger, mcpServer *mcp.MCPServer, srv *http.Server, tenantCleanup func()) {
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if srv != nil {
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("HTTP server shutdown error", zap.Error(err))
		}
	}
	if err := mcpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("MCP shutdown error", zap.Error(err))
	}
	if tenantCleanup != nil {
		tenantCleanup()
	}

	logger.Info("Graceful shutdown complete")
}

// newLogger creates a zap logger based on configuration.
// When running in stdio mode, all logs are written to stderr to avoid
// interfering with stdout which is reserved for JSON-RPC messages.
func newLogger(cfg config.LoggingConfig) (*zap.Logger, error) {
	level := zap.InfoLevel
	switch cfg.Level {
	case "debug":
		level = zap.DebugLevel
	case "info":
		level = zap.InfoLevel
	case "warn":
		level = zap.WarnLevel
	case "error":
		level = zap.ErrorLevel
	}

	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	var encoder zapcore.Encoder
	if cfg.Format == "json" {
		encoder = zapcore.NewJSONEncoder(encoderConfig)
	} else {
		encoder = zapcore.NewConsoleEncoder(encoderConfig)
	}

	// Always write to stderr to avoid interfering with stdout (JSON-RPC)
	core := zapcore.NewCore(encoder, zapcore.Lock(os.Stderr), level)
	return zap.New(core, zap.AddCaller()), nil
}

// apiLoggingMiddleware logs API requests.
func apiLoggingMiddleware(logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		logger.Debug("API request",
			zap.String("method", c.Request.Method),
			zap.String("path", c.Request.URL.Path),
			zap.Int("status", c.Writer.Status()),
			zap.Duration("duration", time.Since(start)),
		)
	}
}
