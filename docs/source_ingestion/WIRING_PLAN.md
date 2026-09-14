# WIRING_PLAN — GitHub-Skills-Ingestion integration into the real codebase

**Revision:** 2
**Last modified:** 2026-07-16T00:00:00Z
**Status:** DESIGN-GROUNDING ONLY. No implementation landed. This document
closes the gap DESIGN.md left open (§ "Gap analysis" below): DESIGN.md names
*which packages* the feature touches; this document names *which real
files/functions/types* each new piece attaches to, with `file:line` citations
captured 2026-07-16 against the current working tree (no commit hash was
requested; the tree at inspection time is the one holding the `main.go`
`buildRouter` / `runner.go` / `config.go` shapes cited below).
**Honest boundary (§11.4.6):** every citation below was read directly from
the file named; nothing is inferred from memory or from DESIGN.md's prose
alone. Where DESIGN.md's proposal needed a concrete refinement to match how
the real code actually works (e.g. the re-scan ticker cannot have a
per-source dynamic interval — Go's `time.Ticker` has one fixed period), the
refinement is called out explicitly as a DELTA vs DESIGN.md, not a silent
rewrite.

---

## 0. Gap analysis vs DESIGN.md (Task 2 of this assignment)

Re-reading `DESIGN.md` end-to-end against the required checklist:

| Required coverage | DESIGN.md status | Verdict |
|---|---|---|
| Source registry (`skill_sources` table/model) | §2.A, full column list, migration `004_skill_sources` named | **COVERED** |
| Fetch/clone | §2.B (GitHub REST API first, shallow-clone fallback, ETag/SHA gating, rate-limit handling) | **COVERED** |
| Parse/analyze SKILL.md + frontmatter | §2.B parser contract (`ParsedSkill`), CATALOG.md §2 authoritative frontmatter table | **COVERED** |
| Dedup vs existing skills | §2.C, 3-outcome policy (NEW/DUPLICATE/VARIANT), conservative-default rule | **COVERED** |
| Import-new | §2.C `Store.ImportSkillModel` (D4) | **COVERED** |
| Enhance with game-changer deltas | §2.C enhance section, `skill_enhancement_proposals` (migration `005`), never-blind-overwrite | **COVERED** |
| Periodic re-scan worker job | §2.D `source_rescan` JobType on `internal/worker.Runner` | **COVERED (design-level)** |
| Config surfaces: CLI + REST + clients | §2.A config seeds + DB SoT; package-family intro line names `cmd/server`/`internal/mcp`/`cmd/cli`/`cmd/tui` as surfaces | **GAP — no concrete endpoint/command/tool list.** DESIGN.md never enumerates actual REST paths, CLI subcommands, or MCP tool names, and after the intro sentence `cmd/tui` is never mentioned again anywhere in §2 (no TUI screen, no TUI data flow). This is the gap this document (§§4–7 below) closes. |
| Full anti-bluff test plan | §2.E (unit/integration/e2e/stress/chaos/Challenges/HelixQA/anti-bluff) | **COVERED** |

Two concrete gaps carried forward into this plan:

- **GAP-1 (REST/CLI/MCP concreteness).** No endpoint paths, no CLI verb tree,
  no MCP tool names were specified. §§4–6 below supply them, each grounded in
  the exact file the new code attaches to.
- **GAP-2 (TUI is a named client with zero design).** The feature brief says
  "configurable via CLI + REST API + all clients." The TUI (`cmd/tui`) is a
  client (per DESIGN.md §1.4(4): "read-only browse/search/tree/registry")
  that DESIGN.md's architecture section never returns to. §7 below gives it
  a minimal read-only design consistent with the TUI's existing scope, and
  the tracked-item breakdown (`TRACKED_ITEMS.md`) puts it last/lowest-priority
  since it is additive, not load-bearing for the ingestion pipeline itself.

A third, smaller point worth flagging as a DELTA (not a gap — DESIGN.md's
prose is silent on it, this plan makes it explicit): DESIGN.md's §2.D re-scan
worker line reads "interval = `min(enabled sources' poll_interval)` clamped
to a floor, or a global default" — that is not directly implementable with
`time.Ticker` (one process-wide ticker has one fixed period; per-source
intervals can only be *evaluated*, not used to *reconfigure the ticker
period*, without tearing down and rebuilding the ticker on every source
add/edit). §5.3 below adopts the same fixed-tick-and-evaluate-due-ness
pattern the codebase already uses for `registryReviewWorker`
(`internal/worker/runner.go:568-586`, confirmed below) instead.

---

## 1. Verified real-code anchors (Task 3)

All paths relative to `$REL` = repo root (promoted from
`docs/research/mvp/Agent_AI_Skill_Tree_Development/project` under HXC-159 T-P3.01).

### 1.1 Module identity
- `go.mod:1` — `module github.com/HelixDevelopment/skills`, `go 1.25.5`.
- `go.mod` — **no GitHub API client library and no `go-git` dependency exist
  today** (full dependency list read; only `BurntSushi/toml`, `gin-gonic/gin`,
  `jackc/pgx/v5`, `mark3labs/mcp-go`, `pgvector-go`, `robfig/cron/v3`,
  `spf13/cobra`, `toon-format/toon-go`, `go.uber.org/zap`, etc.). This is a
  **design decision point**, resolved in §3.1 below by following the
  project's own existing precedent rather than adding an SDK dependency.

### 1.2 Skill model + store (the identity/name/graph substrate)
- `internal/models/skill.go:66-84` — `Skill{ID, Name (UNIQUE), Version,
  Title, Description, Content, Metadata json.RawMessage, Status, Kind,
  CreatedAt, UpdatedAt, Dependencies[], Resources[], Embedding, TreeDepth}`.
  No `origin`/provenance field exists — confirms DESIGN.md §1.2 exactly.
- `internal/skill/store.go:57-71` — `type Store struct { pool *db.Pool }`,
  `func NewStore(pool *db.Pool) *Store`. This is the concrete receiver every
  new `Store` method (§3.2) attaches to.
- `internal/skill/store.go:28-55` — sentinel errors (`ErrSkillNotFound`,
  `ErrSkillExists`, `ErrInvalidSkill`, `ErrDependencyNotFound`,
  `ErrCycleDetected`, `ErrPartOfUnsupported`) — the error-handling convention
  new code must follow (`errors.New` + `%w` wrapping, never bare strings).
- `internal/skill/import_export.go:22-342` — `Store.ImportFromTOML`: single
  `s.pool.WithTx(ctx, func(tx pgx.Tx) error {...})` transaction that inserts
  into `skills`, `skill_registry` (line 262-268: `INSERT INTO skill_registry
  (skill_id, skill_name, missing_deps, stale, last_review, auto_expand,
  coverage) VALUES ($1, $2, '{}', false, NOW(), true, 0.0)`), then
  `skill_dependencies`, then `resources`, then calls
  `s.recalcMissingDeps`/`s.recalcCoverage`, then `s.logAudit(ctx, tx,
  "skill.imported", &skill.ID, map[string]interface{}{...})`. **This is the
  load-bearing precedent for §3.2's `ImportSkillModel`**: it proves
  `internal/skill.Store` already writes into a table it does not otherwise
  "own" reads for (`skill_registry` — owned for reads/reviews by
  `internal/registry.Registry`) directly inside its own transaction. The
  new `skill_source_mappings` write follows the exact same shape.
- `internal/skill/resources.go:19,68,100,128,187,193,213,221,259` —
  existing `Store.AddResource`, `GetResources`, `UpdateResourceHash`,
  `DeleteResource`, `TouchSkillUpdatedAt`, `GetResourceByID`,
  `InvalidateResourceCache`, `BulkAddResources`,
  `GetResourcesNeedingValidation`. `BulkAddResources` (line 221) is directly
  reusable for writing a parsed skill's `references`/`assets`-derived
  resource rows without a new bespoke insert loop.
- `internal/skill/graph.go:22` — `Store.AddDependency` (cycle-checked) is
  reusable if/when a Phase-2 enhancement decides to auto-add the advisory
  `related_to`/`alternative_to` edge for a VARIANT (DESIGN.md §2.C outcome 3)
  outside the main import transaction.

### 1.3 Migrations (naming convention + runner)
- `internal/db/migrations.go:20-23` — doc comment: "Migration files must be
  named as `NNN_description.up.sql` and `NNN_description.down.sql` where NNN
  is a zero-padded version number." Confirmed files on disk:
  `001_initial.{up,down}.sql`, `002_granularity.{up,down}.sql`,
  `003_pg_trgm.{up,down}.sql`. **The next migration is `004_*`,** exactly as
  DESIGN.md's `004_skill_sources` names it.
- `internal/db/migrations.go:265-271` — `schema_migrations` table
  auto-created; `329` `runMigrationSQL` applies each file's SQL inside one
  transaction (matches the `002_granularity.up.sql:1-3` header comment
  "Runs in ONE transaction ... all-or-nothing").
- `migrations_embed.go` (module root) — `//go:embed migrations/*.sql` +
  `MigrationsFS fs.FS`, consumed by `cmd/server/main.go:164`
  (`startupMigrationsFS`) and `cmd/server/main.go:99`
  (`migrateOnStartup(ctx, pool, startupMigrationsFS(), logger)`), which is
  **fatal on error** (`logger.Fatal("Database migration failed; refusing to
  serve", ...)`) — the new `004_skill_sources.up.sql` / `.down.sql` files
  need no separate wiring; dropping them into `migrations/` is picked up
  automatically by the existing embed + fail-closed startup path.
- `migrations/002_granularity.up.sql:11-38` — style precedent: additive
  `ALTER TABLE ... ADD COLUMN ... DEFAULT ...`, `CREATE INDEX`, widened
  `CHECK` via `DROP CONSTRAINT IF EXISTS` + `ADD CONSTRAINT`. The new
  migration's DDL (§3.1 below) follows this exact style.

### 1.4 REST — THE LIVE SURFACE IS `cmd/server/main.go`, NOT `internal/api`
- `cmd/server/main.go:187-379` — `buildRouter(cfg *config.Config, pool
  *db.Pool, store *skill.Store, reg *registry.Registry, mcpServer
  *mcp.MCPServer, logger *zap.Logger) *gin.Engine`. This is the **one**
  hardened Gin router actually served (`setupAPI`, line 386-410, calls
  `buildRouter` then `srv.ListenAndServe()`).
- Confirmed by `GAPS_AND_RISKS_REGISTER.md` **G01**: `internal/api.Server`
  (full CRUD, `internal/api/server.go:94-248`, `internal/api/skills_handler.go`)
  has **zero importers** anywhere in the tree and is explicitly tracked as
  **STILL OPEN — dead-server consolidation (O3)** as of the register's
  2026-07-15 status update (only the *security-hole* half of G01 — wildcard
  CORS / no-auth on the live router — was closed; the *second server exists
  as unreachable dead code* half remains open). **New REST endpoints for
  this feature MUST be added to `cmd/server/main.go`'s `buildRouter`, never
  to `internal/api`** — adding routes to the dead package would ship code
  that compiles but is never served, the exact §11.4.108 SOURCE≠RUNTIME
  class this project's own register already flags.
- `cmd/server/main.go:264-268` — the auth-gated group: `v1 :=
  router.Group("/api/v1"); if authMW != nil { v1.Use(authMW) }`. `authMW` is
  resolved once (`api.ResolveAPIKeyAuth(cfg.Server.APIKeys,
  cfg.Server.AuthDisabled, logger)`, line 243) and is **fail-closed**
  (confirmed by the register's G01 remediation narrative: empty key set +
  auth not explicitly disabled ⇒ 503 on every `/api/v1` request). **New
  skill-source routes inherit this same fail-closed auth automatically by
  being registered under the same `v1` group** — no new auth code needed.
- `cmd/server/main.go:270-344` — the **existing live routes are 100%
  read-only** (`GET /skills`, `GET /skills/search`, `GET /skills/:name`,
  `GET /skills/:name/tree`, `GET /coverage`, `GET /missing`). There is
  currently **no** `POST`/`PATCH`/`DELETE` route anywhere in the live
  router. The skill-source management routes (§4 below) will be the
  **first write-capable routes** in `buildRouter` — worth flagging in
  review, not a blocker (they inherit the same fail-closed auth).
- `cmd/server/main.go:352` — `mcpServer.RegisterHTTPRoutes(router, authMW)`
  mounts `/mcp/v1/*` on the SAME router/auth. New skill-source MCP tools
  (§6) ride this same mount point automatically once registered.

### 1.5 MCP tool registration pattern
- `internal/mcp/server.go:33-49` — `type MCPServer struct {...}`,
  `func NewMCPServer(pool *db.Pool, store *skill.Store, reg
  *registry.Registry, cfg *config.Config, logger *zap.Logger) *MCPServer`.
- `internal/mcp/server.go:134` — `func (s *MCPServer) RegisterTools()` is
  the single call site (`cmd/server/main.go:111`:
  `mcpServer.RegisterTools()`) that must gain new `s.registerSkillSource*()`
  calls.
- `internal/mcp/tools.go:19,98,179,257,310,388,451` — the 7 existing tool
  registration functions (`registerSkillSearch`, `registerSkillGet`,
  `registerSkillTree`, `registerSkillCreate`, `registerLearnFromProject`,
  `registerMissingSkills`, `registerGetCoverage`). `registerSkillCreate`
  (lines 257-304) is the concrete pattern to mirror: `mcp_go.NewTool(name,
  mcp_go.WithDescription(...), mcp_go.WithString(argName,
  mcp_go.Required(), mcp_go.Description(...)))` then
  `s.server.AddTool(tool, func(ctx, request) (*mcp_go.CallToolResult,
  error) {...})`, returning via `s.newToolResult(map[string]interface{}{...})`
  or `s.newToolError(msg)` (helpers at `server.go:234,271`).

### 1.6 CLI command registration pattern
- `cmd/cli/main.go:142-147` — `rootCmd.AddCommand(commands.NewSkillCommand(),
  commands.NewSearchCommand(), commands.NewRegistryCommand(),
  commands.NewExpandCommand(), commands.NewLearnCommand(),
  newConfigCommand())`. A new `commands.NewSourceCommand()` slots in here.
- `cmd/cli/commands/registry.go:16-70` — the concrete cobra-group pattern:
  `func NewRegistryCommand() *cobra.Command` builds a parent `Use: "registry"`
  command, then `cmd.AddCommand(statusCmd, missingCmd, staleCmd, reviewCmd,
  ...)` where each subcommand's `RunE` calls a package-local `run*` helper
  that builds an `*APIClient`-equivalent (in this package, `commands.go`'s
  own HTTP helper — confirmed via `commands.SetAuthHeader`, referenced from
  `cmd/cli/main.go:70`) and hits the REST endpoints from §1.4/§4.
- `cmd/cli/commands/common.go` exists (3243 bytes) and is the shared HTTP/
  auth-header helper file new `source.go`/`source_test.go` commands reuse
  (mirrors `commands.SetAuthHeader` cited at `cmd/cli/main.go:70`).

### 1.7 Worker / scheduler seam
- `internal/worker/runner.go:27-34` — `type JobType string` with
  `JobTypeAutoExpand`, `JobTypeValidate`, `JobTypeCodeAnalysis`,
  `JobTypeRegistryReview`. A new `JobTypeSourceRescan JobType =
  "source_rescan"` constant is added here.
- `internal/worker/runner.go:67-88` — `type Runner struct { pool *db.Pool;
  store *skill.Store; cfg config.Config; logger *zap.Logger; ...; jobChan
  chan Job; ...; registry registryReviewer; ... }`. `jobChan` is
  `make(chan Job, 100)` (line 145) — confirms DESIGN.md §1.4(5)'s "in-memory
  jobChan (cap 100)" claim exactly.
- `internal/worker/runner.go:345` — `func (r *Runner) SubmitJob(ctx
  context.Context, jobType JobType, payload json.RawMessage) (*Job, error)`
  — the existing, reusable async-submit entry point. The new `POST
  /api/v1/skill-sources/:name/sync` REST handler (§4) and the `source sync`
  CLI/MCP commands (§5/§6) all call this, exactly like any future
  `expand`/`learn`-style trigger would, rather than inventing a second
  submission path.
- `internal/worker/runner.go:428-448` — `executeJob`'s `switch job.Type`
  dispatch (`case JobTypeAutoExpand: ...; case JobTypeValidate: ...; case
  JobTypeCodeAnalysis: ...; case JobTypeRegistryReview: ...`) gains a new
  `case JobTypeSourceRescan: return r.handleSourceRescan(ctx, job)` arm.
- `internal/worker/runner.go:568-586` — `registryReviewWorker`: `interval :=
  time.Duration(r.cfg.Registry.ReviewIntervalHours) * time.Hour; if interval
  < time.Minute { interval = time.Minute }`, then a `time.NewTicker(interval)`
  loop selecting on `ctx.Done()` / `ticker.C`. **This exact shape is the
  precedent for the new `sourceRescanWorker`** (§5.3) — config-driven
  interval with a floor, ticker loop, `ctx.Done()` graceful shutdown.
- `internal/worker/runner.go:160-233` — `Start()`/`Stop()`/`IsRunning()`
  manage the goroutine set via `supervise` (line 261, panic-firewalled per
  the `G11` comment at line 65-66); a new `go r.supervise(ctx,
  "source_rescan_worker", r.sourceRescanWorker)` call is added into `Start()`
  alongside the existing `autoExpandWorker`/`validationWorker`/
  `registryReviewWorker` launches (exact call sites inside `Start()` were
  not re-quoted here beyond the confirmed `supervise` signature at line
  261; the launch block itself is a direct, low-risk 1-line addition).

### 1.8 Config
- `internal/config/config.go:34-44` — root `Config` struct: `Server`,
  `Database`, `Embedding`, `Validation`, `AutoExpand`, `CodeAnalysis`, `MCP`,
  `Registry`, `Logging`. A new `SourceSync SourceSyncConfig \`toml:"source_sync"\``
  field is added here (§5.1).
- `internal/config/config.go:161-173` (`AutoExpandConfig.LLMAPIKey`) and
  `:178-198` (`CodeAnalysisConfig.AllowedRoot`) are the two precedents the
  new `SourceSyncConfig` must follow exactly: (a) a secret field resolved
  via the SAME `${VAR}` interpolation mechanism (`interpolate`, line 417-445,
  called from `Load`, line ~360) — **never** a literal token in tracked
  config (§11.4.10); (b) a filesystem-root field that is **fail-closed by
  default** (empty ⇒ reject every path, exactly like `AllowedRoot`), for the
  clone-fallback's temp-directory root.
- `internal/config/config.go:448-486` — the `HELIX_*` env-override block
  (`HELIX_DB_HOST`, `HELIX_API_KEYS`, `HELIX_CODEANALYSIS_ALLOWED_ROOT`,
  etc.). New overrides `HELIX_SOURCE_SYNC_GITHUB_TOKEN` and
  `HELIX_SOURCE_SYNC_CLONE_ROOT` are added in this exact block.
- `internal/config/config.go:506+` (`validate(cfg *Config)`) — rejects any
  string still containing an uninterpolated `${` placeholder post-
  interpolation. The new `SourceSyncConfig.GitHubToken` field is added to
  this same validation sweep.
- `config/config.toml` (only on-disk config example file, 2494 bytes) gets
  a new `[source_sync]` example section in the same commit that adds the Go
  struct (keeps the tracked example in sync, per this project's own
  existing convention of one section per config struct).

### 1.9 LLM client precedent (for the "enhance" novelty-scoring step)
- `internal/autoexpand/llm.go:28` — `type LLMClient interface {...}`.
- `internal/autoexpand/llm.go:44` — `func NewLLMClientFromConfig(cfg
  config.AutoExpandConfig, logger *zap.Logger) (LLMClient, error)` — matches
  the commit-log entry "G28 Anthropic Messages API LLM provider +
  NewLLMClientFromConfig factory" (this session's own git log). The
  enhancement package (§3.5) takes an `LLMClient` as a constructor
  parameter (dependency injection, no new provider plumbing needed) to
  optionally score a delta's novelty — reusing the SAME interface and
  factory, never a second LLM client type.
- `internal/autoexpand/llm.go:74-115` (`OpenAILLM`) and `:423-463`
  (`AnthropicLLM`) are **hand-rolled `net/http` clients** (constructor +
  `SetBaseURL`/`SetHTTPClient`/`SetRateLimit` test-injection setters), NOT
  built on a vendor SDK. This is the concrete precedent §3.1 follows for the
  GitHub fetch client instead of adding `google/go-github` as a dependency.

### 1.10 Path-guard precedent (for the clone-fallback's temp-dir safety)
- `internal/codeanalysis/pathguard.go:42` — `func ValidateProjectPath
  (projectPath, allowedRoot string) (string, error)` (canonicalizes via
  `filepath.EvalSymlinks`, confirmed by the `CodeAnalysisConfig.AllowedRoot`
  doc comment at `config.go:178-186`). The clone-fallback (§3.1) reuses this
  exact function against the new `SourceSync.CloneAllowedRoot` config field
  rather than writing a second path-traversal guard.

### 1.11 Audit event convention
- `internal/db/audit.go:19-37` — the closed `AuditEvent*` string-constant
  set (`AuditEventSkillCreated = "skill.created"`, ...,
  `AuditEventMigrationApplied = "migration.applied"`). New constants
  `AuditEventSkillSourceRegistered = "skill_source.registered"`,
  `AuditEventSkillSourceSynced = "skill_source.synced"`,
  `AuditEventSkillImportedFromSource = "skill.imported_from_source"`,
  `AuditEventSkillEnhancementProposed = "skill.enhancement_proposed"`,
  `AuditEventSkillEnhancementApplied = "skill.enhancement_applied"` are
  added to this same block, following the exact `"noun.verb"` naming
  convention already in use (and distinct from the existing
  `"skill.imported"` emitted by `ImportFromTOML` at
  `import_export.go:317`, so a TOML-authored import and a GitHub-sourced
  import remain distinguishable in `audit_log`).

### 1.12 Test-harness precedent (anti-bluff grounding for §8)
- `testdb_helper_test.go` files confirmed present in `internal/worker`,
  `internal/mcp`, `internal/registry`, `internal/db` — the live-Postgres
  integration-test bootstrap convention (§11.4.27: no mocks beyond unit
  tests) new `internal/source/*` packages' integration tests reuse.
- `qa-results/` on-disk subdirectories confirmed: `g32_f1`,
  `g32_reviewscheduler`, `infra_fix`, `p1t1_remediation`, `pg_trgm_fix`,
  `post_landing_68e7d2f` — the `qa-results/<run-id>/` evidence-capture
  convention this feature's e2e/stress/chaos runs land evidence under
  (e.g. `qa-results/gh_skills_ingestion_e2e/`).

### 1.13 Tracked-item ID scheme
- `GAPS_AND_RISKS_REGISTER.md` — highest existing formal entry is **G51**;
  **G52** is already consumed (referenced from
  `cmd/cli/health_g52_test.go:12` and `cmd/tui/health_g52_test.go`, not yet
  back-filled into the register's own heading list at inspection time).
  `G47` is an **open operator decision** (§11.4.66/§11.4.54) on G-scheme vs
  `ATM-NNN` going forward. `TRACKED_ITEMS.md` therefore numbers this
  feature's items `G53` onward as **provisional working labels**, explicitly
  flagged as advisory pending the G47 resolution — never claimed as an
  authoritative allocation.

---

## 2. Package layout (adopting DESIGN.md §2's family, made concrete)

```
internal/
  skill/
    source_import.go        # NEW: Store.ImportSkillModel (§3.2) — sibling
                             #   to ImportFromTOML, same *Store receiver,
                             #   same s.pool.WithTx(...) transaction shape.
  skillsource/               # NEW package — skill_sources CRUD/registry
    store.go                #   Store{pool *db.Pool}; Register/List/Get/
                             #   Update/Delete/MarkSyncStatus.
    store_test.go
  source/
    github/
      client.go              # NEW: hand-rolled REST client (§3.1)
      client_test.go
    skillmd/
      parse.go                # NEW: frontmatter+body parser (§3.3)
      parse_test.go
    mapper/
      map.go                   # NEW: ParsedSkill -> models.Skill (§3.4)
      map_test.go
    dedup/
      classify.go               # NEW: NEW/DUPLICATE/VARIANT (§3.4)
      classify_test.go
    enhance/
      delta.go                   # NEW: delta extraction + proposal write (§3.5)
      delta_test.go
    sync/
      orchestrator.go              # NEW: per-source scan cycle (§3.6)
      orchestrator_test.go
  worker/
    runner.go                      # EDIT: +JobTypeSourceRescan, +sourceRescanWorker
  config/
    config.go                      # EDIT: +SourceSyncConfig
  mcp/
    source_tools.go                # NEW: 6 new MCP tools (§6)
  db/
    audit.go                       # EDIT: +5 AuditEvent* constants
cmd/
  server/
    main.go                        # EDIT: +registerSkillSourceRoutes call in buildRouter
    skillsource_routes.go          # NEW: route handlers (§4), package main
  cli/
    commands/
      source.go                    # NEW: NewSourceCommand() (§5)
      source_test.go
    main.go                        # EDIT: +rootCmd.AddCommand(commands.NewSourceCommand())
  tui/
    sources.go                     # NEW: read-only source/mapping browse view (§7)
migrations/
  004_skill_sources.up.sql         # NEW
  004_skill_sources.down.sql       # NEW
  005_skill_enhancement_proposals.up.sql   # NEW
  005_skill_enhancement_proposals.down.sql # NEW
```

---

## 3. Data + core logic wiring

### 3.1 GitHub fetch client — `internal/source/github/client.go`
Mirrors `internal/autoexpand/llm.go`'s hand-rolled-client shape exactly
(§1.9): `type Client struct { token string; baseURL string; httpClient
*http.Client; logger *zap.Logger }`, `func NewClient(token string, logger
*zap.Logger) *Client`, `SetBaseURL`/`SetHTTPClient` setters for test
injection (mirrors `OpenAILLM.SetBaseURL`/`SetHTTPClient` at
`llm.go:105-114`). Methods: `ListTreeRecursive(ctx, owner, repo, ref)
(*ListTreeResult, error)` where `ListTreeResult{Entries []TreeEntry,
Truncated bool}` (round-3 W-b remediation — Truncated is now returned,
not merely logged) (`GET /repos/{owner}/{repo}/git/trees/{ref}?recursive=1`),
`GetHeadSHA(ctx, owner, repo, ref) (string, error)` (`GET
/repos/{owner}/{repo}/commits/{ref}`), `FetchBlob(ctx, owner, repo, path,
ref, etag string) (*BlobResult, error)` where `BlobResult{Content []byte,
SHA string, ETag string, NotModified bool}` (round-4 F3 remediation — the
signature now landed carries an explicit `ref` parameter, distinct from
`GetHeadSHA`'s, and returns a single result struct rather than a bare
4-tuple, so `ETag` round-trips for conditional-request caching) (`GET
/repos/{owner}/{repo}/contents/{path}` with `If-None-Match`),
`RateLimitStatus(resp *http.Response) RateLimit` (reads
`X-RateLimit-Remaining`/`X-RateLimit-Reset`). **Decision (resolves the
GAP flagged at §1.1):** no `google/go-github` dependency is added; this is
a deliberate minimal hand-rolled client, matching the project's own
established pattern rather than introducing a new heavyweight SDK
dependency for what is, per CATALOG.md §2, three simple REST calls.
Clone-fallback (`ShallowClone(ctx, repoURL, ref, allowedRoot string)
(dir string, cleanup func(), err error)`) shells out to the `git` binary
(`exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--branch", ref,
repoURL, dir)`) into a directory validated by
`codeanalysis.ValidateProjectPath` (§1.10) against
`cfg.SourceSync.CloneAllowedRoot`; `cleanup()` is registered via `defer`
at every call site per §11.4.14.

### 3.2 `Store.ImportSkillModel` — `internal/skill/source_import.go`
New file in the EXISTING `skill` package (not a new package) because it
must share `Store`'s `pool *db.Pool` field and the private helpers
`recalcMissingDeps`/`recalcCoverage`/`logAudit` that `ImportFromTOML`
already uses (`import_export.go:309-323`) — those are unexported methods
on `*Store`, unreachable from a sibling package. Signature:
`func (s *Store) ImportSkillModel(ctx context.Context, sk *models.Skill,
mapping SourceMapping) (*models.Skill, error)`, where `SourceMapping`
carries `SourceID uuid.UUID`, `SourcePath string`, `UpstreamName string`,
`UpstreamContentHash string`, `UpstreamLicense string`. Body: same
`s.pool.WithTx(ctx, func(tx pgx.Tx) error {...})` shape as
`ImportFromTOML` (`import_export.go:251-341`) — INSERT `skills` (with
`origin='imported'`, a new column added by the same migration as
`skill_sources`, §3.7), INSERT `skill_registry` (identical statement to
`import_export.go:262-268`), INSERT `resources` via the loop pattern at
`import_export.go:296-306` (or delegate to `BulkAddResources`,
`resources.go:221`), then a NEW `INSERT INTO skill_source_mappings
(id, source_id, source_path, skill_id, upstream_name,
upstream_content_hash, upstream_license, last_action, first_imported_at,
last_seen_at) VALUES (...)`, then `s.recalcMissingDeps`/`recalcCoverage`
(reused verbatim), then `s.logAudit(ctx, tx,
db.AuditEventSkillImportedFromSource, &sk.ID, map[string]interface{}{...})`
(new constant, §1.11). No dependency edges are created (§1.7 of DESIGN.md
— imported skills are atomic leaves; this file does not touch
`skill_dependencies` at all, matching DESIGN.md's explicit "we do NOT
invent edges" decision).

### 3.3 SKILL.md parser — `internal/source/skillmd/parse.go`
Pure function, no I/O, no DB: `func Parse(raw []byte, sourcePath string)
(*ParsedSkill, error)`. Splits `---\n...\n---\n` YAML frontmatter from the
markdown body via `github.com/goccy/go-yaml` (round-4 F3 remediation —
`goccy/go-yaml` has now LANDED as a DIRECT dependency, `go.mod`'s main
`require (...)` block, no `// indirect` marker, confirmed 2026-07-16; the
choice this section originally flagged as an open implementation
decision between it and `gopkg.in/yaml.v3` is RESOLVED — `goccy/go-yaml`
was picked and is imported directly by `parse.go`). Output
`ParsedSkill{Name, Description, License,
RawFrontmatter map[string]any, Body, Scripts[]string, References[]string,
Assets[]string, SourcePath, ContentHash string}` exactly per DESIGN.md
§2.B/CATALOG.md §2's frontmatter table. `ContentHash` = sha256 of the
full raw normalized file text (frontmatter + body, after BOM-strip and
CRLF/CR->LF normalization) — round-3 N1/N3 remediation of an earlier
`Name + "\x00" + normalizedBody`-shaped formula that missed
Description/Version/License-adjacent fields and was forgeable via an
embedded NUL byte — giving the
`skill_source_mappings.upstream_content_hash` value §3.2 persists.

### 3.4 Mapper + dedup — `internal/source/mapper/map.go`,
`internal/source/dedup/classify.go`
`mapper.Map(parsed *skillmd.ParsedSkill, sourceSlug string,
licenseAllowlist []string, sourcePermalink string) (*mapper.Result, error)`
(round-4 F3 remediation — the signature LANDED takes the source's slug,
license allowlist, and permalink as three plain arguments, not a
`*skillsource.Source` struct — no `skillsource` package exists in this
tree yet, §2.A's `skill_sources` table remains DB-schema-only; and it
returns `*mapper.Result`, not a bare `*models.Skill` — `Result{Skill
*models.Skill, LicenseSkipped bool, ContentHash string, SourcePath
string, UpstreamName string, UpstreamLicense string}` bundles the mapped
skill together with EVERY provenance field a future `sync` orchestrator
needs to write the `skill_source_mappings` row §3.7 describes, in one
return value) builds the namespaced `Name = sourceSlug + "." +
parsed.Name`
(D1 in DESIGN.md §3), sets `Title`/`Description`/`Content` directly,
folds `RawFrontmatter` into `Skill.Metadata` (JSON, lossless), and applies
the license gate (`licenseAllowlist`) — on a disallowed/unknown
license it still returns a `Skill` but with `Content` REPLACED by a short
"see upstream" stub and a `Resources` entry pointing at `sourcePermalink`,
plus `Result.LicenseSkipped bool` (a field on the returned `Result`, not a
separate out-parameter) that the caller (`sync`) uses to
set `skill_source_mappings.last_action='skipped_license'` (never persisting
the disallowed body). `dedup.Classify(ctx, store *skill.Store, mapping
existingMapping *models.SkillSourceMapping, candidate *models.Skill)
(dedup.Outcome, *models.Skill, error)` implements DESIGN.md §2.C's 3-outcome
policy by querying `store.GetByName` (existing method,
confirmed by `import_export.go:81` `s.GetByName(ctx, wrapper.Skill.Name)`)
for the namespaced name AND a trigram-similarity query against
`skills.name`/`title` (reusing the SAME `similarity(...)` SQL idiom already
in `Store.Search`, `store.go:77-84`, since `pg_trgm` is already enabled by
`003_pg_trgm.up.sql`) for the cross-source/native match.

### 3.5 Enhancement proposals — `internal/source/enhance/delta.go` +
migration `005_skill_enhancement_proposals`
`func ExtractDelta(existing *models.Skill, upstream *skillmd.ParsedSkill)
Delta` (structured diff — new sections/steps present upstream and absent
in `existing.Content`); optionally scored via the injected
`autoexpand.LLMClient` (§1.9) for a novelty score. `func
(s *ProposalStore) CreateProposal(ctx, targetSkillID uuid.UUID, sourceID
uuid.UUID, delta Delta, upstreamLicense string) error` inserts one row into
the new `skill_enhancement_proposals` table (`status='proposed'`) —
**never** writes to `skills.content` directly (§11.4.122 no-blind-overwrite,
DESIGN.md D5). Approval (via REST/CLI/MCP, §4-§6) is a SEPARATE, explicit,
operator-gated write that appends the delta to `Content`, bumps `Version`,
and re-persists with `Status='draft'` (never auto-promoted).

### 3.6 Sync orchestrator — `internal/source/sync/orchestrator.go`
`func (o *Orchestrator) ScanOne(ctx context.Context, src *skillsource.Source)
(ScanResult, error)`: (1) cheap HEAD-SHA gate via `github.Client.GetHeadSHA`
— if `== src.LastCommitSHA`, return a no-op `ScanResult` immediately; (2)
`ListTreeRecursive` filtered by `src.PathGlob`; (3) per changed/new
`SKILL.md` path, `FetchBlob` → `skillmd.Parse` → `mapper.Map` →
`dedup.Classify` → `skill.Store.ImportSkillModel` (NEW) or
`enhance.CreateProposal` (DUPLICATE) or import-namespaced+advisory-edge
(VARIANT); (4) update `skillsource.Store.MarkSyncStatus(ctx, src.ID,
lastCommitSHA, status, errStr)`. Single-owner-per-source is a Postgres
session-level advisory lock: `SELECT pg_try_advisory_lock(hashtext($1))`
keyed on `src.ID.String()`, released via `defer ... pg_advisory_unlock`. No
existing advisory-lock usage was found elsewhere in this codebase
(**UNCONFIRMED absence** — a repo-wide search for `pg_advisory` returned no
hits in the files inspected; this is new machinery, not a reused
precedent, and is called out as such rather than mis-cited as existing).

### 3.7 New migration `004_skill_sources`
Per DESIGN.md §2.A's field list, in the exact style of
`002_granularity.up.sql` (§1.3):
```sql
CREATE TABLE skill_sources (
    id                     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                   TEXT NOT NULL UNIQUE,
    repo_url               TEXT NOT NULL,
    host                   TEXT NOT NULL DEFAULT 'github',
    owner                  TEXT,
    repo                   TEXT,
    ref                    TEXT NOT NULL DEFAULT 'main',
    path_glob              TEXT NOT NULL DEFAULT '**/SKILL.md',
    source_class           TEXT NOT NULL DEFAULT 'unknown'
                             CHECK (source_class IN ('vendored','marketplace','link_index','unknown')),
    auth_ref               TEXT,
    enabled                BOOLEAN NOT NULL DEFAULT TRUE,
    poll_interval_minutes  INT NOT NULL DEFAULT 1440,
    license_default        TEXT,
    last_synced_at         TIMESTAMPTZ,
    last_commit_sha        TEXT,
    last_status            TEXT,
    last_error             TEXT,
    created_at             TIMESTAMPTZ DEFAULT NOW(),
    updated_at             TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX idx_skill_sources_enabled ON skill_sources(enabled);

CREATE TABLE skill_source_mappings (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_id                UUID NOT NULL REFERENCES skill_sources(id) ON DELETE CASCADE,
    source_path              TEXT NOT NULL,
    skill_id                 UUID REFERENCES skills(id) ON DELETE SET NULL,
    upstream_name            TEXT,
    upstream_content_hash    TEXT,
    upstream_license         TEXT,
    last_action              TEXT CHECK (last_action IN
                               ('imported','enhanced','deduped','skipped_license','unchanged','error')),
    first_imported_at        TIMESTAMPTZ DEFAULT NOW(),
    last_seen_at             TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(source_id, source_path)
);
CREATE INDEX idx_source_mappings_skill ON skill_source_mappings(skill_id);

ALTER TABLE skills
    ADD COLUMN origin TEXT NOT NULL DEFAULT 'native'
    CHECK (origin IN ('native','imported'));
CREATE INDEX idx_skills_origin ON skills(origin);
```
Down migration drops in reverse (`ALTER TABLE skills DROP COLUMN origin`,
then `DROP TABLE skill_source_mappings`, `DROP TABLE skill_sources`),
mirroring `002_granularity.down.sql`'s reverse-order convention.

Migration `005_skill_enhancement_proposals` (separate file, same style):
```sql
CREATE TABLE skill_enhancement_proposals (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    target_skill_id    UUID NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    source_id          UUID NOT NULL REFERENCES skill_sources(id) ON DELETE CASCADE,
    source_path        TEXT NOT NULL,
    delta              JSONB NOT NULL,
    upstream_license   TEXT,
    novelty_score      FLOAT,
    status             TEXT NOT NULL DEFAULT 'proposed'
                         CHECK (status IN ('proposed','approved','rejected','applied')),
    created_at         TIMESTAMPTZ DEFAULT NOW(),
    decided_at         TIMESTAMPTZ,
    decided_by         TEXT
);
CREATE INDEX idx_enhancement_proposals_target ON skill_enhancement_proposals(target_skill_id);
CREATE INDEX idx_enhancement_proposals_status ON skill_enhancement_proposals(status);
```

---

## 4. REST wiring — `cmd/server/skillsource_routes.go` (package `main`)

New file, package `main` (same package as `cmd/server/main.go`), because
`buildRouter` and all its collaborators (`skill.Store`, `registry.Registry`)
already live there and the live router is assembled in that one function
(§1.4). Exposes:

```go
func registerSkillSourceRoutes(
    v1 *gin.RouterGroup,
    sourceStore *skillsource.Store,
    runner *worker.Runner,
    logger *zap.Logger,
)
```

called from `buildRouter` (`cmd/server/main.go`) immediately after the
existing `v1.GET("/missing", ...)` block (line ~344) and before
`mcpServer.RegisterHTTPRoutes(...)` (line 352) — inside the SAME
already-auth-gated `v1` group, so no new auth wiring is needed (§1.4).

| Method | Path | Handler purpose |
|---|---|---|
| `POST` | `/api/v1/skill-sources` | Register a new source (body: name, repo_url, ref, path_glob, poll_interval_minutes, auth_ref, license_default). |
| `GET` | `/api/v1/skill-sources` | List all sources + last_status/last_synced_at/last_commit_sha. |
| `GET` | `/api/v1/skill-sources/:name` | One source's detail. |
| `PATCH` | `/api/v1/skill-sources/:name` | Update enabled/poll_interval_minutes/path_glob/license_default. |
| `DELETE` | `/api/v1/skill-sources/:name` | Remove a source (mappings CASCADE per FK). |
| `POST` | `/api/v1/skill-sources/:name/sync` | Submit a `source_rescan` job scoped to this source via `runner.SubmitJob(ctx, worker.JobTypeSourceRescan, payload)` (§1.7) — async, bounded by the existing `jobChan` cap-100. |
| `GET` | `/api/v1/skill-sources/:name/mappings` | Paginated `skill_source_mappings` rows for this source. |
| `GET` | `/api/v1/skill-enhancement-proposals` | List proposals, filterable by `?status=`. |
| `POST` | `/api/v1/skill-enhancement-proposals/:id/approve` | Approve — applies the delta as a new draft version (§3.5). |
| `POST` | `/api/v1/skill-enhancement-proposals/:id/reject` | Reject — marks `status='rejected'`, no mutation. |

Responses use the same inline `gin.H{...}`/`c.JSON(http.StatusOK, ...)`
idiom `buildRouter`'s existing handlers already use (§1.4) — **not** the
unwired `internal/api/response.go` / TOON-negotiation helpers, since those
live in the dead `internal/api` package this file must not import (doing so
would pull in a whole parallel `Pool` interface, §1.4's G01 finding).

---

## 5. CLI wiring — `cmd/cli/commands/source.go`

`func NewSourceCommand() *cobra.Command` mirrors
`NewRegistryCommand()`'s exact shape (§1.6): a parent `Use: "source"`
command with subcommands, each with a package-local `run*` helper hitting
the REST endpoints in §4 via the same HTTP/auth-header plumbing
`registry.go`/`common.go` already use:

- `skill-system source add <repo-url> --name --ref --path-glob
  --poll-interval --auth-ref --license-default`
- `skill-system source list`
- `skill-system source show <name>`
- `skill-system source enable <name>` / `source disable <name>`
- `skill-system source remove <name>`
- `skill-system source sync <name>`
- `skill-system source mappings <name>`
- `skill-system source proposals list [--status]`
- `skill-system source proposals approve <id>`
- `skill-system source proposals reject <id>`

Added to `cmd/cli/main.go:142-147`'s `rootCmd.AddCommand(...)` list.

---

## 6. MCP wiring — `internal/mcp/source_tools.go`

Six new tools, each following `registerSkillCreate`'s exact pattern
(§1.5): `skill_source_register`, `skill_source_list`, `skill_source_sync`,
`skill_source_get_mappings`, `skill_enhancement_proposals_list`,
`skill_enhancement_proposal_approve`. Each registered from a new
`(s *MCPServer) registerSkillSource*()` method, all called from
`RegisterTools()` (`internal/mcp/server.go:134`) alongside the 7 existing
registrations.

---

## 7. TUI wiring — `cmd/tui/sources.go` (closes GAP-2)

Read-only, mirroring `cmd/tui/registry.go`'s existing browse-list pattern
(confirmed present at `cmd/tui/registry.go`, part of the bubbletea
`model.go` view-switching already in place per §1.4(4) of DESIGN.md): a
new list view showing `name | source_class | enabled | last_status |
last_synced_at`, with a drill-down into a source's
`skill_source_mappings` and, separately, a read-only browse of pending
`skill_enhancement_proposals` (status/target skill/novelty score) — no
mutating actions from the TUI in this phase (matches the TUI's existing
scope, DESIGN.md §1.4(4): "read-only browse/search/tree/registry"). Wired
into `cmd/tui/model.go`'s existing view-switch alongside the current
browse/search/tree/registry views. Approvals/registration/sync-trigger
from the TUI are explicitly OUT of scope for the first landing (tracked as
a distinct, lower-priority item in `TRACKED_ITEMS.md` rather than bundled
in) — this is a conscious scope cut, not an oversight, because the CLI/
REST/MCP surfaces already give full read+write coverage and the TUI's own
established role in this codebase is browse-only.

---

## Sources verified 2026-07-16

All facts in this document are either (a) read directly from the named
file/line in the working tree at the repo root (module
`github.com/HelixDevelopment/skills`; pre-promotion base
`docs/research/mvp/Agent_AI_Skill_Tree_Development/project`) during this session, or (b)
carried forward from `CATALOG.md`/`DESIGN.md`'s own verified-sources
footers (external SKILL.md format facts). No external repo or library API
claim is made in this document beyond what CATALOG.md already verified;
the one new external fact used here — that the project's `go.mod` has no
GitHub API client / go-git dependency — was verified by reading the full
`go.mod` file directly (quoted in §1.1), not assumed.
