# TRACKED_ITEMS — GitHub-Skills-Ingestion workable-item breakdown

**Revision:** 2
**Last modified:** 2026-07-16T00:00:00Z
**Status:** PROPOSED. Nothing below is landed. IDs are provisional working
labels (see note on numbering).

**Numbering note (§11.4.54/§11.4.6):** `GAPS_AND_RISKS_REGISTER.md`'s
highest formal heading is `G51`; `G52` is already consumed (referenced from
`cmd/cli/health_g52_test.go:12`, not yet back-filled as a register heading
at inspection time). `G47` in that same register is an **open operator
decision** on whether the project's tracked-item scheme stays `G0x` or
migrates to `ATM-NNN` (§11.4.54). This document therefore numbers the new
items **`G53`–`G75`** as provisional working labels only — whichever scheme
the operator resolves G47 to, these items get their real IDs (`G53..` as-is,
or freshly-minted `ATM-NNN`) at landing time, never silently assumed.

Every item is sized to ≈1–3 files (a landable, independently-reviewable
unit) and cites the exact real files it touches, per `WIRING_PLAN.md`.
Dependency ordering is a DAG, not a strict sequence — items with no edge
between them are parallelizable (§11.4.58/§11.4.103 PWU pipeline).

---

## G53 — Migration: `skill_sources` + `skill_source_mappings` + `skills.origin`

**Type:** Task

**WHAT:** Add `migrations/004_skill_sources.up.sql` and
`.down.sql`, following the exact additive-superset style of
`migrations/002_granularity.up.sql` (verified: single-transaction DDL,
`ADD COLUMN ... DEFAULT`, `CREATE INDEX`, reversible `.down.sql`). Creates
`skill_sources` (source registry) and `skill_source_mappings` (per-upstream-
file identity across re-scans), plus `ALTER TABLE skills ADD COLUMN origin
TEXT NOT NULL DEFAULT 'native' CHECK (origin IN ('native','imported'))`.
Exact DDL is in `WIRING_PLAN.md` §3.7.

**Affected scope:** `migrations/004_skill_sources.up.sql` (new),
`migrations/004_skill_sources.down.sql` (new). Zero Go code. The embedded
`migrations_embed.go` `//go:embed migrations/*.sql` directive requires no
edit — it picks up any new file matching the glob automatically (verified:
`migrations_embed.go` embeds the whole `migrations/*.sql` set, not a
hardcoded file list).

**Reproduction/trigger:** N/A (schema addition). Verification is
`internal/db.MigrateFS` applying the new file cleanly against a fresh test
database (reusing `internal/db/testdb_helper_test.go`'s bootstrap) and
`db.CurrentMigrationVersion` reporting `4`.

**Acceptance criteria:** (1) `go test ./internal/db/...` green with a NEW
test asserting `004_skill_sources` applies + rolls back cleanly on a fresh
DB (mirrors the existing `internal/db/migrations_granularity_test.go`
pattern for `002`). (2) `skills.origin` defaults `'native'` for every
pre-existing row (zero data loss, per the `002_granularity` precedent's
own "additive, zero-data-loss superset" framing). (3) `UNIQUE(source_id,
source_path)` on `skill_source_mappings` enforced by a paired test
inserting a duplicate and expecting the constraint violation. (4) `.down.sql`
reverses cleanly (drop-in-reverse-order, matching
`002_granularity.down.sql`'s convention).

**Composes with:** §11.4.44 (no revision header needed — SQL files are
out of §11.4.44's tracked-Markdown scope), §11.4.108 (this migration IS
the SOURCE→ARTIFACT layer for every downstream item that writes these
tables), `internal/db/migrations.go` (`NNN_description.{up,down}.sql`
convention, `schema_migrations` table).

**Depends on:** none (first item; unblocks G57, G63, G67).

---

## G54 — Migration: `skill_enhancement_proposals`

**Type:** Task

**WHAT:** Add `migrations/005_skill_enhancement_proposals.up.sql` and
`.down.sql`. Creates the `skill_enhancement_proposals` table (gated
enhancement-delta proposals — never a blind overwrite of an existing
skill's content, per §11.4.122). Exact DDL is in `WIRING_PLAN.md` §3.7.

**Affected scope:** `migrations/005_skill_enhancement_proposals.up.sql`
(new), `migrations/005_skill_enhancement_proposals.down.sql` (new).

**Reproduction/trigger:** N/A (schema addition); same verification method
as G53.

**Acceptance criteria:** (1) applies/rolls back cleanly on a fresh test DB.
(2) `status` CHECK constraint (`proposed|approved|rejected|applied`)
enforced by a paired test. (3) `target_skill_id`/`source_id` FKs `ON DELETE
CASCADE` verified by a test that deletes a skill and confirms its proposals
vanish (no orphan rows). (4) `.down.sql` reverses cleanly.

**Composes with:** §11.4.122 (no-silent-overwrite — this table IS the
mechanical enforcement of that mandate for the enhance pipeline),
§11.4.185 (the `approved`/`rejected` states are where the manual-QA-final-
confirmation gate attaches for any enhancement that touches a
previously-active skill).

**Depends on:** none (parallel to G53; both can land independently since
neither's DDL references the other's new table). Unblocks G64, G67.

---

## G55 — `SourceSyncConfig` + env overrides + `config.toml` example

**Type:** Task

**WHAT:** Add `SourceSync SourceSyncConfig \`toml:"source_sync"\`` to the
root `Config` struct (`internal/config/config.go:34-44`). New
`SourceSyncConfig` fields: `Enabled bool`, `ScanTickMinutes int`,
`DefaultPollIntervalMinutes int`, `APIBase string`, `GitHubToken string`
(`${VAR}`-interpolated, mirroring `AutoExpandConfig.LLMAPIKey` at
`config.go:161-173`), `CloneFallbackEnabled bool`, `CloneAllowedRoot string`
(fail-closed-empty, mirroring `CodeAnalysisConfig.AllowedRoot` at
`config.go:178-198`), `LicenseAllowlist []string`, `MaxSkillsPerScan int`.
New env overrides `HELIX_SOURCE_SYNC_GITHUB_TOKEN` /
`HELIX_SOURCE_SYNC_CLONE_ROOT` added to the existing `HELIX_*` override
block (`config.go:448-486`). `GitHubToken` added to `validate()`'s
residual-`${`-placeholder sweep (`config.go:506+`). A new `[source_sync]`
section added to `config/config.toml` (the one on-disk example file).

**Affected scope:** `internal/config/config.go` (edit, ~4 non-contiguous
hunks: struct field, new type, env-override block, validate sweep),
`internal/config/config_test.go` (edit — new test cases mirroring the
existing `AutoExpand`/`CodeAnalysis` config test coverage),
`config/config.toml` (edit — new `[source_sync]` example section).

**Reproduction/trigger:** N/A (config surface). Verification loads a TOML
fixture with `source_sync.github_token = "${TEST_GH_TOKEN}"` and asserts
interpolation + the fail-closed-empty `CloneAllowedRoot` behavior (a
`ShallowClone` call against an empty root must be rejected — tested at
G59, not here; this item only proves the config VALUE resolves correctly).

**Acceptance criteria:** (1) `go test ./internal/config/...` green with new
cases for `${VAR}` interpolation of `github_token`, the
`HELIX_SOURCE_SYNC_GITHUB_TOKEN` override, and `validate()` rejecting a
residual `${UNSET_VAR}`. (2) `config/config.toml` example section present
and round-trips through `config.Load`. (3) No literal token anywhere in
tracked config (§11.4.10 — a grep-based paired mutation asserting a
`github_token = "ghp_..."` literal fails a pre-commit-style check).

**Composes with:** §11.4.10 (credentials-by-name-only), §11.4.6 (fail-
closed-empty for the clone root, no silent allow-all).

**Depends on:** none (parallel to G53/G54). Unblocks G59 (clone root),
G65 (scan-tick interval).

---

## G56 — New `AuditEvent*` constants for skill-source events

**Type:** Task

**WHAT:** Add five constants to the closed `AuditEvent*` set in
`internal/db/audit.go:19-37`: `AuditEventSkillSourceRegistered =
"skill_source.registered"`, `AuditEventSkillSourceSynced =
"skill_source.synced"`, `AuditEventSkillImportedFromSource =
"skill.imported_from_source"`, `AuditEventSkillEnhancementProposed =
"skill.enhancement_proposed"`, `AuditEventSkillEnhancementApplied =
"skill.enhancement_applied"` — following the exact `"noun.verb"`
convention already in use, and deliberately distinct from the existing
`"skill.imported"` (emitted by TOML-authored imports,
`internal/skill/import_export.go:317`) so the two import paths remain
distinguishable in `audit_log`.

**Affected scope:** `internal/db/audit.go` (single-hunk edit, ~5 lines).

**Reproduction/trigger:** N/A (constant addition).

**Acceptance criteria:** (1) constants compile and are exported. (2) A
table-driven test (extending the existing audit-event coverage, if any, or
a new minimal one) asserts each new constant's string value matches the
naming convention (no typos — `logAudit`/`LogEvent` call sites are the only
producers, so a typo'd constant would silently create an unrecognized
event string with no compiler check; the test pins the literal values).

**Composes with:** none new; this is the shared vocabulary G63 and G64
write into `audit_log`.

**Depends on:** none. Unblocks G63, G64 (both need these constants to log
correctly rather than inventing ad-hoc strings).

---

## G57 — `internal/skillsource` package: source registry CRUD

**Type:** Feature

**WHAT:** New package `internal/skillsource` with `type Store struct {pool
*db.Pool}` (mirrors `internal/skill.Store`'s and `internal/registry.Registry`'s
own `{pool *db.Pool}` shape, confirmed at `internal/skill/store.go:57-65`
and `internal/registry/registry.go:17-22`). Methods: `Register(ctx,
src *Source) (*Source, error)` (INSERT, `name` UNIQUE conflict →
`ErrSourceExists`), `List(ctx) ([]Source, error)`, `GetByName(ctx, name
string) (*Source, error)` (→ `ErrSourceNotFound` sentinel, mirroring
`skill.ErrSkillNotFound`'s convention), `Update(ctx, name string, patch
SourcePatch) (*Source, error)`, `Delete(ctx, name string) error`,
`MarkSyncStatus(ctx, id uuid.UUID, commitSHA, status, errMsg string) error`.

**Affected scope:** `internal/skillsource/store.go` (new),
`internal/skillsource/store_test.go` (new, live-Postgres integration per
§11.4.27, reusing the `testdb_helper_test.go` bootstrap pattern confirmed
present in `internal/worker`/`internal/mcp`/`internal/registry`/`internal/db`).

**Reproduction/trigger:** N/A (new CRUD package). Verification: register →
list → get → update(disable) → sync-status-mark → delete, each step
asserted against the real `skill_sources` table from G53.

**Acceptance criteria:** (1) `go test ./internal/skillsource/...` green,
live Postgres, no mocks beyond any unit-only edge case. (2) `UNIQUE(name)`
conflict returns a typed `ErrSourceExists`, never a raw Postgres error
string (§11.4.1 — callers must `errors.Is`, not string-match). (3) `Delete`
cascades to `skill_source_mappings` (proven by a test that registers,
imports a mapping row via a direct SQL insert fixture, deletes the source,
and asserts the mapping row is gone — FK `ON DELETE CASCADE` from G53).
(4) Paired §1.1 mutation: flip the `UNIQUE(name)` violation handling to
swallow the error → the conflict test goes RED.

**Composes with:** §11.4.10 (auth_ref stores an env-var NAME only, never a
token value — a unit test asserts `Register` never persists anything
matching a common token shape into `auth_ref` beyond the name itself; this
is a naming-convention check, not a secret-scanning claim).

**Depends on:** G53 (needs the `skill_sources` table to exist). Unblocks
G63, G65, G67, G69.

---

## G58 — `internal/source/github`: hand-rolled REST fetch client

**Type:** Feature

**WHAT:** New package `internal/source/github` with `type Client struct
{token, baseURL string; httpClient *http.Client; logger *zap.Logger}`,
`NewClient`, `SetBaseURL`/`SetHTTPClient` test-injection setters — mirroring
`internal/autoexpand/llm.go`'s `OpenAILLM`/`AnthropicLLM` hand-rolled-client
shape exactly (confirmed at `llm.go:74-115`, `423-463`). Methods:
`ListTreeRecursive(ctx, owner, repo, ref string) (*ListTreeResult, error)`
where `ListTreeResult{Entries []TreeEntry, Truncated bool}` surfaces
GitHub's "truncated" bit programmatically (round-3 W-b remediation — a
prior version only logged it, giving a caller no way to detect a partial
listing) (`GET /repos/{owner}/{repo}/git/trees/{ref}?recursive=1`), `GetHeadSHA(ctx,
owner, repo, ref string) (string, error)` (`GET
/repos/{owner}/{repo}/commits/{ref}`), `FetchBlob(ctx, owner, repo, path,
ref, etag string) (*BlobResult, error)` where `BlobResult{Content []byte,
SHA string, ETag string, NotModified bool}` (round-4 F3 remediation — the
landed signature carries an explicit `ref` parameter and returns a single
result struct, not a bare 4-tuple, so `ETag` round-trips for
conditional-request caching across calls) (`GET
/repos/{owner}/{repo}/contents/{path}` + conditional `If-None-Match`),
`RateLimitStatus(resp *http.Response) RateLimit` (reads
`X-RateLimit-Remaining`/`X-RateLimit-Reset`). **Deliberately no
`google/go-github` dependency** — `go.mod` was verified to have zero
GitHub/git client libraries today; this follows the project's own existing
precedent of small hand-rolled HTTP clients per external service rather
than adding an SDK for three REST calls.

**Affected scope:** `internal/source/github/client.go` (new),
`internal/source/github/client_test.go` (new, `httptest.Server`-backed
unit tests exactly mirroring the pattern `llm_anthropic_test.go` uses for
`AnthropicLLM` — HTTP-mockable via the injected client/base-URL, no live
network call in unit tests).

**Reproduction/trigger:** N/A (new client). Verification against a local
`httptest.Server` returning fixture JSON matching GitHub's real Trees/
Commits/Contents response shapes (fixtures captured from a real, small,
public repo — `anthropics/skills` per CATALOG.md's Tier-A pick — during
test authoring, never fabricated).

**Acceptance criteria:** (1) `go test ./internal/source/github/...` green,
unit-only (mockable transport, §11.4.27 — a live-network integration test
is a SEPARATE item, folded into G71's e2e run). (2) `RateLimitStatus`
correctly parses both a healthy and an exhausted (`X-RateLimit-Remaining:
0`) response, with a paired §1.1 mutation flipping the parse to always
report healthy → the exhausted-response test goes RED. (3) `FetchBlob`
honors `If-None-Match` and returns `notModified=true` on a real `304`.
(4) No token is ever logged (a test asserts `logger`'s captured output
never contains the configured token string, mirroring the credential-
non-leak discipline §11.4.10 demands).

**Composes with:** §11.4.201 (rate-limit backoff asserts the REAL header,
never a proxy/guess), §11.4.99 (GitHub REST API shapes verified against
CATALOG.md's own "Sources verified" footer, not memory).

**Depends on:** none (pure HTTP client, no DB, no config dependency beyond
a token string passed in). Unblocks G65 (fetch), G59 (shares the package).

---

## G59 — `internal/source/github`: shallow-clone fallback

**Type:** Feature

**WHAT:** `func (c *Client) ShallowClone(ctx context.Context, repoURL, ref,
allowedRoot string) (dir string, cleanup func(), err error)` in the SAME
`internal/source/github` package (a sibling file, not a new package — the
clone fallback is conceptually part of the fetch client's surface per
DESIGN.md §2.B). Shells out to the `git` binary
(`exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--branch", ref,
repoURL, dir)`) into a temp directory validated by the EXISTING
`codeanalysis.ValidateProjectPath(dir, allowedRoot)` (confirmed at
`internal/codeanalysis/pathguard.go:42`) — reusing the established
path-traversal guard rather than writing a second one. `cleanup()` removes
the temp directory; every call site registers it via `defer` (§11.4.14
test-playback-cleanup discipline generalised to any temp-resource user).

**Affected scope:** `internal/source/github/clone.go` (new),
`internal/source/github/clone_test.go` (new — asserts (a) a clone into a
path OUTSIDE `allowedRoot` is rejected before any `git` invocation runs,
(b) a successful clone against a real tiny local `file://` fixture repo
(no network dependency for the unit test) produces a `SKILL.md`-bearing
tree, (c) `cleanup()` genuinely removes the directory).

**Reproduction/trigger:** N/A (new capability). Fail-closed verification:
an empty `allowedRoot` (the G55 config default) must reject every clone
request, mirroring `CodeAnalysisConfig.AllowedRoot`'s own fail-closed-empty
contract exactly.

**Acceptance criteria:** (1) `go test ./internal/source/github/...` green
(this file's tests alongside G58's). (2) Path-traversal attempt (a
`repoURL`/`ref` combination engineered to make `git` write outside
`allowedRoot` via a malicious `--upload-pack` or symlink target) is
rejected — reusing `ValidateProjectPath`'s existing symlink-canonicalization
proof, not re-deriving it. (3) `cleanup()` is idempotent (double-call does
not panic/error) and removes the directory (verified by `os.Stat` returning
`ErrNotExist` post-cleanup).

**Composes with:** §11.4.14 (cleanup on every exit path), §11.4.111
(the allowedRoot itself is config-supplied, not index/ordinal-based).

**Depends on:** G55 (needs `CloneAllowedRoot` config field to exist for the
fail-closed test), G58 (same package, same `Client` receiver for a
consistent constructor). Unblocks G65.

---

## G60 — `internal/source/skillmd`: SKILL.md parser

**Type:** Feature

**WHAT:** New package `internal/source/skillmd`, pure function `func
Parse(raw []byte, sourcePath string) (*ParsedSkill, error)` — no I/O, no
DB. Splits `---\n...\n---\n` YAML frontmatter from the markdown body per
the authoritative field table in `CATALOG.md` §2 (`name`, `description`,
`license`, `disable-model-invocation`, `allowed-tools`,
`disallowed-tools`, `context: fork`, `arguments`, `user-invocable`, plus
tolerated unknown fields). Output `ParsedSkill{Name, Description, License,
RawFrontmatter map[string]any, Body, Scripts[]string, References[]string,
Assets[]string, SourcePath, ContentHash string}`. `ContentHash` = sha256
of the full raw normalized file text (frontmatter + body, after
BOM-strip + CRLF/CR->LF normalization) — round-3 N1/N3 remediation: a
prior `Name + "\x00" + License + "\x00" + normalizedBody` formula missed
Description/Version entirely (so a description- or version-only upstream
edit never flipped the hash) and was forgeable via an embedded NUL byte
absorbing the "\x00" join separator.

**Affected scope:** `internal/source/skillmd/parse.go` (new),
`internal/source/skillmd/parse_test.go` (new — golden-good/golden-bad
fixture pairs per §11.4.107(10): a well-formed 2-field-minimum SKILL.md, a
well-formed 8-field superset (jeremylongshore-style, per CATALOG.md B3), a
missing-description file (description derived from first paragraph per the
documented fallback), a no-frontmatter file, CRLF line endings, unicode
content, and a corrupt-YAML fixture that must error cleanly rather than
panic).

**Reproduction/trigger:** N/A (new parser). Golden fixtures are authored
directly from the frontmatter table CATALOG.md already verified against
`code.claude.com/docs/en/skills`.

**Acceptance criteria:** (1) `go test ./internal/source/skillmd/...` green.
(2) 2-field-minimum (`name`+`description` only) parses successfully — the
open-standard floor per CATALOG.md §2. (3) Unknown fields are preserved
losslessly in `RawFrontmatter` (round-trip test: parse → re-marshal the
map → byte-equivalent set of keys). (4) Corrupt YAML returns a wrapped
error, never a panic (a fuzz-style test feeding truncated/malformed
frontmatter). (5) Paired §1.1 mutation: drop the lossless-unknown-field
capture → the round-trip test goes RED.

**Composes with:** §11.4.107(10) (self-validated golden-good/golden-bad
analyzer), §11.4.99 (frontmatter field table sourced from CATALOG.md's own
verified footer).

**Depends on:** none (pure, no I/O). Unblocks G61.

---

## G61 — `internal/source/mapper`: ParsedSkill → `models.Skill` + license gate

**Type:** Feature

**WHAT:** New package `internal/source/mapper`, `func Map(parsed
*skillmd.ParsedSkill, sourceSlug string, licenseAllowlist []string,
sourcePermalink string) (*Result, error)` (round-4 F3 remediation — the
landed signature also takes `sourcePermalink` as a fourth argument, and
returns `*Result`, not a bare `(*models.Skill, licenseSkipped bool, err
error)` 3-tuple: `Result{Skill *models.Skill, LicenseSkipped bool,
ContentHash string, SourcePath string, UpstreamName string,
UpstreamLicense string}` bundles the mapped skill with every provenance
field a future importer needs to write the `skill_source_mappings` row in
one return value). Builds the namespaced
`Name = sourceSlug + "." + parsed.Name` (D1, preserves `skills.name`
`UNIQUE`), sets `Title` (frontmatter name titleized, or first `# H1` of
body), `Description`, `Content` directly, folds `RawFrontmatter` into
`Skill.Metadata` (JSON, lossless — no first-class model field for
Claude-Code-specific keys like `allowed-tools`, per DESIGN.md §1.7's
explicit honest boundary), `Kind = models.SkillKindAtomic` always (no
dependency schema in SKILL.md, per DESIGN.md §1.7/R2 — this mapper never
invents edges), `Status = models.SkillStatusDraft`, `origin='imported'`
(the G53 column). License gate: `parsed.License` ∉ `licenseAllowlist` ⇒
returns a `Result` whose `Skill.Content` is replaced by a short "see upstream"
stub, with a `Resources` entry pointing at `sourcePermalink` (when
non-empty), plus `Result.LicenseSkipped=true` (a field on the returned
`Result`, not a separate out-parameter — caller sets
`last_action='skipped_license'`
and does NOT persist the real body — §11.4.122, never redistribute
source-available content as if open).

**Affected scope:** `internal/source/mapper/map.go` (new),
`internal/source/mapper/map_test.go` (new — table-driven: allowed license
→ full content; disallowed/unknown license → stub + flag; namespacing
collision-avoidance with an existing native skill of the same base name).

**Reproduction/trigger:** N/A (new mapper). Verification directly against
CATALOG.md's own license findings (Apache-2.0/MIT/CC-BY-4.0 allowed by
default per DESIGN.md's proposed default allowlist; anthropics'
`docx/pdf/pptx/xlsx` "source-available, NOT open source" license must gate
per CATALOG.md §1 A1's own finding).

**Acceptance criteria:** (1) `go test ./internal/source/mapper/...` green.
(2) A skill whose license is in the allowlist gets its full `Content`.
(3) A skill whose license is `"source-available"` (or empty/unknown) gets
the stub + `licenseSkipped=true`, never the real body. (4) The namespaced
`Name` never collides with a native (`origin='native'`) skill sharing the
same base name (two skills named `systematic-debugging` — one native, one
sourced from `obra.systematic-debugging` — coexist under distinct
`skills.name` values). (5) Paired §1.1 mutation: remove the license check
→ the stub test goes RED (a disallowed-license skill's real content would
otherwise persist).

**Composes with:** §11.4.6 (license-allowlist threshold is config-driven,
never hardcoded from literature — reads `licenseAllowlist` from G55's
config), §11.4.122 (the stub-not-body behavior IS the no-redistribution
enforcement).

**Depends on:** G60 (needs `ParsedSkill`). Unblocks G62, G63.

---

## G62 — `internal/source/dedup`: NEW/DUPLICATE/VARIANT classifier

**Type:** Feature

**WHAT:** New package `internal/source/dedup`, `func Classify(ctx
context.Context, store *skill.Store, existingMapping *models.SkillSourceMapping,
candidate *models.Skill) (Outcome, *models.Skill, error)` implementing
DESIGN.md §2.C's 3-outcome policy: (1) exact upstream identity unchanged
(same `(source_id, source_path)` mapping, same `upstream_content_hash`) ⇒
`Unchanged`; (2) no name/content match among existing skills ⇒ `New`;
(3) strong match (same normalized base-name AND high trigram similarity,
via the SAME `similarity(...)` SQL idiom `Store.Search` already uses,
confirmed at `internal/skill/store.go:77-84`, reusing the `pg_trgm`
extension enabled by `003_pg_trgm.up.sql`) ⇒ `Duplicate` (do not
re-import; run enhancement instead); (4) same base-name, materially
different content ⇒ `Variant` (import namespaced + flag for review, never
silently merged). Threshold is a `Config` parameter, calibrated on the
project's OWN fixtures (§11.4.6 — never hardcoded from literature).

**Affected scope:** `internal/source/dedup/classify.go` (new),
`internal/source/dedup/classify_test.go` (new, live-Postgres integration —
seeds a few native + previously-imported skills, then classifies a battery
of candidates covering all 4 outcomes; a truth-table test per DESIGN.md
§2.E).

**Reproduction/trigger:** N/A (new classifier). Verification directly
grounded in the cross-repo duplication CATALOG.md documented (§3 point 3:
`systematic-debugging`/`test-driven-development` appear in `anthropics`,
`obra/superpowers`, and multiple curated lists — a real, concrete
duplicate-name scenario the test fixtures use verbatim).

**Acceptance criteria:** (1) `go test ./internal/source/dedup/...` green,
live Postgres. (2) All 4 outcomes independently exercised with a real DB
fixture (not a mock — §11.4.27). (3) The `Duplicate`-vs-`Variant` boundary
defaults to `Variant` when the similarity score sits in an ambiguous middle
band (the DESIGN.md §2.C "conservative default" — never silent merge).
(4) Paired §1.1 mutation: flip the conservative-default branch to prefer
`Duplicate` on ambiguity → a fixture engineered to sit in the ambiguous
band goes RED (proving the default actually matters).

**Composes with:** §11.4.6 (calibrated threshold, not literature-hardcoded
— the threshold constant is documented with its calibration fixture),
§11.4.122 (VARIANT/ambiguous never silently merges).

**Depends on:** G61 (needs a mapped `models.Skill` candidate). Unblocks
G63 (import path needs the classifier's verdict), G65 (orchestrator wires
this in).

---

## G63 — `Store.ImportSkillModel` (sibling to `ImportFromTOML`)

**Type:** Feature

**WHAT:** New file `internal/skill/source_import.go` in the EXISTING
`skill` package (not a new package — it needs the unexported
`recalcMissingDeps`/`recalcCoverage`/`logAudit` helpers `ImportFromTOML`
already uses, per `import_export.go:309-323`, which are unreachable from a
sibling package). `func (s *Store) ImportSkillModel(ctx context.Context, sk
*models.Skill, mapping SourceMapping) (*models.Skill, error)` — same
`s.pool.WithTx(ctx, func(tx pgx.Tx) error {...})` shape as
`ImportFromTOML` (`import_export.go:251-341`): INSERT `skills` (with
`origin='imported'`), INSERT `skill_registry` (byte-identical statement to
`import_export.go:262-268`), INSERT `resources` (via
`BulkAddResources`-equivalent inline logic, `resources.go:221` reused where
possible), a NEW `INSERT INTO skill_source_mappings (...)` row, then
`recalcMissingDeps`/`recalcCoverage` (reused verbatim), then `s.logAudit(ctx,
tx, db.AuditEventSkillImportedFromSource, &sk.ID, ...)` (G56's new
constant). **No dependency edges are created** — imported skills are
atomic leaves (DESIGN.md §1.7/R2, explicit no-invented-edges decision).

**Affected scope:** `internal/skill/source_import.go` (new),
`internal/skill/source_import_test.go` (new, live-Postgres integration —
mirrors the existing `internal/skill/g07_roundtrip_test.go`/
`graph_test.go` harness style already present in this package).

**Reproduction/trigger:** N/A (new store method). Verification: import a
candidate skill + mapping → assert `skills` row (`origin='imported'`),
`skill_registry` row, `skill_source_mappings` row, and `audit_log` row all
land in ONE transaction (a mid-transaction failure injection — e.g. a
constraint violation on the mapping insert — must roll back ALL of it,
zero half-imported skills, per DESIGN.md §2.E chaos requirement carried
forward here at the unit/integration layer).

**Acceptance criteria:** (1) `go test ./internal/skill/...` green including
the new file. (2) A single transaction covers all 4 writes (skills,
skill_registry, resources, skill_source_mappings) plus the audit log —
proven by the rollback-on-mid-tx-failure test. (3) Zero `skill_dependencies`
rows are ever written by this method (an explicit assertion, guarding
against a future accidental edge-invention regression). (4) Re-importing
the SAME `(source_id, source_path)` with an unchanged content hash is
idempotent at the `skill_source_mappings` layer (`UNIQUE(source_id,
source_path)` from G53 — a second call must UPDATE `last_seen_at`, not
attempt a second INSERT that violates the constraint).

**Composes with:** §11.4.108 (this IS the SOURCE→ARTIFACT write path for
every ingested skill), §11.4.124 (imported skills are draft — never
silently promoted).

**Depends on:** G53 (tables), G56 (audit constants), G62 (dedup verdict
decides NEW vs skip). Unblocks G65, G67.

---

## G64 — `internal/source/enhance`: delta extraction + proposal store

**Type:** Feature

**WHAT:** New package `internal/source/enhance`. `func ExtractDelta(existing
*models.Skill, upstream *skillmd.ParsedSkill) Delta` — structured diff
(sections/steps/techniques present upstream, absent in `existing.Content`;
new `allowed-tools`/scripts). Optional novelty scoring via the EXISTING
`autoexpand.LLMClient` interface (confirmed at `internal/autoexpand/llm.go:28`,
factory `NewLLMClientFromConfig` at line 44) injected as a constructor
parameter — no new LLM plumbing. `func (p *ProposalStore) CreateProposal(ctx,
targetSkillID, sourceID uuid.UUID, sourcePath string, delta Delta,
upstreamLicense string) error` — INSERT into `skill_enhancement_proposals`
(G54), `status='proposed'`. **Never writes to `skills.content` directly**
(§11.4.122). A SEPARATE `func (p *ProposalStore) Approve(ctx, proposalID
uuid.UUID, decidedBy string) error` applies the delta as an appended,
version-bumped, still-`draft` skill update — invoked only from an explicit
operator/QA action (REST/CLI/MCP, never automatically).

**Affected scope:** `internal/source/enhance/delta.go` (new),
`internal/source/enhance/proposal_store.go` (new),
`internal/source/enhance/delta_test.go` +
`internal/source/enhance/proposal_store_test.go` (new).

**Reproduction/trigger:** N/A (new pipeline stage). Verification: feed a
native skill + an upstream `ParsedSkill` with materially richer content →
assert a non-empty `Delta` → `CreateProposal` lands a `proposed` row →
`existing.Content` is byte-identical to before (never mutated by
proposal creation) → `Approve` mutates `Content`/`Version`/`Status` only
after being explicitly called.

**Acceptance criteria:** (1) `go test ./internal/source/enhance/...` green,
live Postgres for `proposal_store_test.go`, unit-only for `delta_test.go`.
(2) `CreateProposal` never touches the `skills` table (asserted directly —
a query for the target skill's `updated_at` before/after must be
unchanged). (3) `Approve` bumps `Version` and re-persists as `draft`
(never `active`/`validated` — no self-promotion, mirroring
`ImportFromTOML`'s always-draft contract). (4) Paired §1.1 mutation:
make `CreateProposal` write directly to `skills.content` → the
never-mutated-on-proposal test goes RED.

**Composes with:** §11.4.122 (the entire point of this item), §11.4.185
(an approved enhancement of a previously-`active` skill still needs the
manual-QA final-confirmation gate before that skill re-promotes — this
item lands it as `draft`, promotion is out of scope here and reuses the
existing validation lifecycle DESIGN.md §1.6 already documents).

**Depends on:** G54 (table), G56 (audit constants), G61 (needs a mapped
candidate to diff against). Unblocks G65, G67 (REST approve/reject
endpoints), G68 (CLI), G69 (MCP).

---

## G65 — `internal/source/sync`: per-source scan orchestrator

**Type:** Feature

**WHAT:** New package `internal/source/sync`. `func (o *Orchestrator)
ScanOne(ctx context.Context, src *skillsource.Source) (ScanResult, error)`:
(1) Postgres session advisory lock keyed on `src.ID`
(`pg_try_advisory_lock(hashtext($1))`, released via `defer
pg_advisory_unlock`) — single-owner-per-source (§11.4.119); **this is NEW
machinery, no existing advisory-lock usage was found elsewhere in this
codebase** (verified absent, not merely uncited). (2) cheap HEAD-SHA gate
via `github.Client.GetHeadSHA` — unchanged ⇒ no-op `ScanResult`. (3)
`ListTreeRecursive` filtered by `src.PathGlob`. (4) per changed/new path:
`FetchBlob` → `skillmd.Parse` → `mapper.Map` → `dedup.Classify` →
`skill.Store.ImportSkillModel` (NEW) / `enhance.CreateProposal`
(DUPLICATE) / namespaced-import+advisory-edge (VARIANT). (5)
`skillsource.Store.MarkSyncStatus` with the new HEAD SHA + outcome.

**Affected scope:** `internal/source/sync/orchestrator.go` (new),
`internal/source/sync/orchestrator_test.go` (new, live-Postgres
integration, wiring a fake/httptest-backed `github.Client` per G58's test
pattern so the orchestrator test needs no live network).

**Reproduction/trigger:** N/A (new orchestrator). Verification: end-to-end
in-process run against a fixture `github.Client` returning a small,
deterministic tree (2–3 `SKILL.md` files) — asserts imported-skill count,
`skill_source_mappings` rows, and `last_synced_at`/`last_commit_sha`
updates match expectations.

**Acceptance criteria:** (1) `go test ./internal/source/sync/...` green.
(2) Two concurrent `ScanOne` calls for the SAME source: the second observes
the held advisory lock and returns immediately without importing anything
twice (proves single-owner-per-source, §11.4.119). (3) Two concurrent
`ScanOne` calls for DIFFERENT sources run fully in parallel (no shared
lock contention — proves the lock is genuinely per-source, keyed on
`src.ID`, not a global mutex). (4) An unchanged HEAD SHA short-circuits
to zero DB writes beyond `last_seen_at`-class bookkeeping (idempotent
no-op, §11.4.86 content-hash discipline). (5) Paired §1.1 mutation: skip
the advisory-lock acquisition → the concurrent-same-source test goes RED
(double-import proven possible).

**Composes with:** §11.4.119 (single-resource-owner partitioning),
§11.4.86 (content-hash + commit-SHA change detection), §11.4.201 (the
rate-limit/backoff path from G58 surfaces honestly here, never fakes a
successful scan).

**Depends on:** G57 (source registry), G58+G59 (fetch/clone), G60+G61
(parse/map), G62 (dedup), G63 (import), G64 (enhance). Unblocks G66, G69.
**This is the highest-fan-in item in the DAG** — it is the integration
point, landed only after every upstream package item is independently
green.

---

## G66 — Worker wiring: `JobTypeSourceRescan` + `sourceRescanWorker`

**Type:** Feature

**WHAT:** In `internal/worker/runner.go`: (1) new constant
`JobTypeSourceRescan JobType = "source_rescan"` added to the `JobType`
block (`runner.go:27-34`). (2) new `case JobTypeSourceRescan:` arm in
`executeJob`'s dispatch switch (`runner.go:428-448`) calling a new
`handleSourceRescan(ctx, job)`. (3) new `sourceRescanWorker(ctx
context.Context)` ticker method mirroring `registryReviewWorker` EXACTLY
(`runner.go:568-586`): `interval := time.Duration(cfg.SourceSync
.ScanTickMinutes) * time.Minute; if interval < time.Minute { interval =
time.Minute }`, `time.NewTicker(interval)` loop, `ctx.Done()`/`ticker.C`
select. Each tick iterates `enabled=true` sources via
`skillsource.Store.List`, computes due-ness as `last_synced_at +
poll_interval_minutes <= now()` per-source (a DELTA vs DESIGN.md's literal
"dynamic min-interval ticker" phrasing — `time.Ticker` cannot be
reconfigured per-source without teardown/rebuild; the fixed-tick-and-
evaluate-due-ness pattern is what `registryReviewWorker` already does and
this item follows it exactly rather than inventing a second scheduling
primitive). (4) `go r.supervise(ctx, "source_rescan_worker",
r.sourceRescanWorker)` added to `Start()` alongside the existing
`autoExpandWorker`/`validationWorker`/`registryReviewWorker` launches
(panic-firewalled via the same `supervise`, `runner.go:261`).

**Affected scope:** `internal/worker/runner.go` (edit, 4 non-contiguous
hunks), `internal/worker/sourcerescan_test.go` (new — mirrors
`registryreview_unit_test.go`'s spy-interface pattern, confirmed present
at `internal/worker/registryreview_unit_test.go`, for the unit-level
ticker/dispatch test, plus `registryreview_integration_test.go`'s
live-DB pattern for the due-ness-per-source integration test).

**Reproduction/trigger:** N/A (new job type + ticker). Verification: submit
a `JobTypeSourceRescan` job via `Runner.SubmitJob` (existing method,
`runner.go:345`) directly (bypassing the ticker) and assert
`handleSourceRescan` invokes the G65 orchestrator for the named source(s)
and records a `JobResult`.

**Acceptance criteria:** (1) `go test ./internal/worker/...` green
including new cases. (2) `sourceRescanWorker`'s interval floors at 1 minute
exactly like `registryReviewWorker` (a config value of `0` or negative
does not busy-loop). (3) A due source is scanned; a not-yet-due source
(recent `last_synced_at`) is skipped on the same tick (proves the
per-source due-ness evaluation, not a blanket scan-everything-every-tick).
(4) A panic inside `handleSourceRescan` is caught by the existing
`supervise`/`recoverJob` panic firewall (`runner.go:301,324`) and increments
`Metrics.PanicsRecovered` — reusing the existing panic-safety guarantee,
not a new one. (5) Paired §1.1 mutation: remove the due-ness check (scan
every enabled source every tick regardless of `poll_interval_minutes`) →
a test asserting a long-`poll_interval` source is NOT rescanned on a
short-interval tick goes RED.

**Composes with:** §11.4.89 (background test execution — this worker
itself IS a background job runner, not something the test suite runs
synchronously), §11.4.108 (`handleSourceRescan`'s runtime signature is
"the orchestrator ran for every due source", verified on a clean test DB).

**Depends on:** G55 (config `ScanTickMinutes`), G57 (source list/due-ness),
G65 (the orchestrator this dispatches to). Unblocks G67 (REST sync-trigger
calls `SubmitJob` with this job type).

---

## G67 — REST wiring: `cmd/server/skillsource_routes.go` + `buildRouter`

**Type:** Feature

**WHAT:** New file `cmd/server/skillsource_routes.go` (package `main`),
`func registerSkillSourceRoutes(v1 *gin.RouterGroup, sourceStore
*skillsource.Store, runner *worker.Runner, proposalStore
*enhance.ProposalStore, logger *zap.Logger)`, called from `buildRouter`
(`cmd/server/main.go:187-379`) immediately after the existing `v1.GET
("/missing", ...)` block (~line 344) and before `mcpServer.
RegisterHTTPRoutes(...)` (line 352) — inside the SAME already-fail-closed-
auth-gated `v1` group (`main.go:264-268`), so no new auth code is needed.
Ten endpoints (full table in `WIRING_PLAN.md` §4): `POST/GET/PATCH/DELETE
/api/v1/skill-sources[/:name]`, `POST /api/v1/skill-sources/:name/sync`
(submits `worker.JobTypeSourceRescan` via the EXISTING `runner.SubmitJob`,
`runner.go:345`), `GET /api/v1/skill-sources/:name/mappings`, `GET
/api/v1/skill-enhancement-proposals`, `POST
/api/v1/skill-enhancement-proposals/:id/{approve,reject}`. Responses use
the SAME inline `gin.H{...}` idiom `buildRouter`'s existing handlers
already use — deliberately NOT importing the dead `internal/api` package's
`response.go`/TOON-negotiation helpers (per G01's confirmed dead-code
status).

**Affected scope:** `cmd/server/main.go` (edit — one new call inside
`buildRouter`, plus passing the new `*skillsource.Store`/`*enhance.
ProposalStore` through the existing constructor chain from `main()`),
`cmd/server/skillsource_routes.go` (new),
`cmd/server/skillsource_routes_test.go` (new — mirrors
`cmd/server/g22_router_test.go`'s existing router-assembly test pattern:
build the router in-process, hit each new route with `httptest`, assert
auth-gating (401/503 unauthenticated, 200 with a valid key) and correct
status codes).

**Reproduction/trigger:** N/A (new routes). Verification: build the router
via `buildRouter` in a test, `POST /api/v1/skill-sources` with/without a
valid `X-API-Key`, assert the SAME fail-closed behavior every existing
`/api/v1` route already has.

**Acceptance criteria:** (1) `go test ./cmd/server/...` green including new
route tests. (2) Every new route is reachable ONLY under the same
`authMW` gate as the existing routes — an unauthenticated request to
`POST /api/v1/skill-sources` gets 401 (keys configured) / 503 (unconfigured
+ auth not disabled), never a 200 (mirrors the exact fail-closed contract
`GAPS_AND_RISKS_REGISTER.md` G01's remediation already proved for the
existing routes). (3) `/health` and `/` remain open and unaffected. (4)
Paired §1.1 mutation: register the new routes OUTSIDE the `v1.Use(authMW)`
group → the auth test goes RED.

**Composes with:** §11.4.108 (this file IS the ARTIFACT→RUNTIME wiring
step — a route defined only in a dead package would be exactly the G01
class of defect this project's own register already flags), §11.4.142/
§11.4.194 (every new handler gets the mandatory independent code review
before landing).

**Depends on:** G57, G63 (import path), G65 (orchestrator), G66 (job type
for the sync-trigger endpoint), G64 (proposal approve/reject). Unblocks
G68, G69 (TUI uses these same REST endpoints), G70, G73.

---

## G68 — CLI wiring: `cmd/cli/commands/source.go`

**Type:** Feature

**WHAT:** New file `cmd/cli/commands/source.go`, `func NewSourceCommand()
*cobra.Command` mirroring `NewRegistryCommand()`'s exact shape
(`cmd/cli/commands/registry.go:16-70`): parent `Use: "source"` command with
subcommands `add <repo-url>`, `list`, `show <name>`, `enable <name>`,
`disable <name>`, `remove <name>`, `sync <name>`, `mappings <name>`,
`proposals list [--status]`, `proposals approve <id>`, `proposals reject
<id>` — each hitting the G67 REST endpoints via the SAME
`commands.SetAuthHeader`/HTTP-client plumbing `registry.go`/`common.go`
already use. Registered via `rootCmd.AddCommand(commands.NewSourceCommand())`
added to `cmd/cli/main.go:142-147`'s existing list.

**Affected scope:** `cmd/cli/commands/source.go` (new),
`cmd/cli/commands/source_test.go` (new, mirroring
`cmd/cli/commands/common_test.go`/`skill_test.go`'s existing CLI-command
test pattern — `httptest.Server`-backed, asserting the correct HTTP
method/path/body per subcommand), `cmd/cli/main.go` (edit — one new
`AddCommand` line).

**Reproduction/trigger:** N/A (new CLI surface). Verification: each
subcommand invoked against an `httptest.Server` standing in for the G67
routes, asserting the exact request shape and correct human-readable
output rendering (`--format json|toml`, reusing the existing
`APIClient.Output`/`OutputTOML` methods at `cmd/cli/main.go:87-112`).

**Acceptance criteria:** (1) `go test ./cmd/cli/...` green including new
cases. (2) Every subcommand's `--help` output is non-empty and documents
its flags (matching the existing `registry`/`skill` commands' `Long`/
`Example` field convention). (3) `source sync <name>` prints the submitted
job's ID/status, not just a bare "OK" (parity with how `expand`/`learn`
commands surface job state — confirmed by the `cmd/cli/commands/expand.go`/
`learn.go` files present in this package). (4) A CLI invocation against a
server with no API key configured surfaces the SAME 503 the REST layer
returns, not a generic "connection failed" (proves the CLI propagates the
real HTTP status, per the existing `APIClient.Request`'s error-wrapping at
`cmd/cli/main.go:77-81`).

**Composes with:** §11.4.20 (CLI as one of the three mandated config
surfaces), §11.4.98 (every CLI-driven test here is fully automated,
re-runnable, no human keystroke).

**Depends on:** G67 (REST endpoints to call). Parallelizable with G69, G70
once G67 lands.

---

## G69 — MCP wiring: `internal/mcp/source_tools.go`

**Type:** Feature

**WHAT:** New file `internal/mcp/source_tools.go`, six new tools each
mirroring `registerSkillCreate`'s exact pattern (`internal/mcp/tools.go:257-304`):
`skill_source_register`, `skill_source_list`, `skill_source_sync`,
`skill_source_get_mappings`, `skill_enhancement_proposals_list`,
`skill_enhancement_proposal_approve`. Each a new `(s *MCPServer)
registerSkillSource*()` method calling the injected `*skillsource.Store`/
`*worker.Runner`/`*enhance.ProposalStore` DIRECTLY (the MCP server already
holds `store *skill.Store`/`reg *registry.Registry` by direct reference
per `internal/mcp/server.go:33-49` — the new stores are added the same
way, NOT proxied through the REST layer, mirroring how `registerSkillCreate`
calls `s.skillStore.ImportFromTOML` directly rather than making an HTTP
call to itself). All six calls added to `RegisterTools()`
(`internal/mcp/server.go:134`) alongside the 7 existing registrations.

**Affected scope:** `internal/mcp/source_tools.go` (new),
`internal/mcp/source_tools_test.go` (new, mirroring
`internal/mcp/skill_create_draft_test.go`'s existing MCP-tool test
pattern), `internal/mcp/server.go` (edit — `NewMCPServer`'s parameter list
gains the new store references; `RegisterTools()` gains 6 new call lines).

**Reproduction/trigger:** N/A (new MCP surface). Verification: dispatch
each tool via `s.dispatchTool` (existing method, `server.go:161`) in a test
and assert the correct store/orchestrator/runner method was invoked with
the correct arguments, plus a correctly-shaped `CallToolResult`.

**Acceptance criteria:** (1) `go test ./internal/mcp/...` green including
new cases. (2) `skill_source_register` rejects a malformed request the
SAME way `skill_create` rejects an empty `toml` parameter (`tools.go:272-275`
precedent: required-argument check before any store call). (3) The 6 new
tools appear in the MCP `tools/list` response (an integration-level test
against `RegisterTools()`'s live tool set, not a hand-maintained count).
(4) `skill_enhancement_proposal_approve` never bypasses the G64
`Approve` method's draft-only contract (an MCP-level attempt to force
`active` status is rejected/ignored, proven by a test).

**Composes with:** §11.4.20 (MCP as a first-class client surface for CLI
AI agents — this is the "meta" case: an agent using THIS System's MCP
tools to manage the very sources that feed OTHER agents' skills),
§11.4.98 (fully automated tool-dispatch tests, no human keystroke).

**Depends on:** G57, G64, G65, G66 (the stores/orchestrator/runner these
tools call directly). Parallelizable with G68, G70.

---

## G70 — TUI wiring: `cmd/tui/sources.go` (read-only, lowest priority)

**Type:** Feature

**WHAT:** New file `cmd/tui/sources.go`, a read-only bubbletea view
mirroring `cmd/tui/registry.go`'s existing browse-list pattern (confirmed
present alongside `browse.go`/`search.go`/`tree.go` in the same package):
a list showing `name | source_class | enabled | last_status |
last_synced_at`, drill-down into a source's `skill_source_mappings`, and a
separate read-only browse of pending `skill_enhancement_proposals`
(target skill / novelty score / status). **No mutating actions** — no
register/sync-trigger/approve from the TUI in this landing, matching the
TUI's own already-established scope (DESIGN.md §1.4(4): "read-only
browse/search/tree/registry"). Wired into `cmd/tui/model.go`'s existing
view-switch alongside the current views, reading via the SAME `cmd/tui/
api_client.go` HTTP client the other views already use to call G67's REST
endpoints.

**Affected scope:** `cmd/tui/sources.go` (new),
`cmd/tui/model.go` (edit — one new view-switch case),
`cmd/tui/api_client.go` (edit — new GET-only methods for the G67 list/get/
mappings/proposals endpoints, mirroring `cmd/tui/api_client_test.go`'s
existing test pattern for the other read paths).

**Reproduction/trigger:** N/A (new read-only view). Verification: launch
the TUI against a test server exposing G67's routes, navigate to the new
view, assert rendered content matches the server's fixture data (via the
project's existing TUI-testing approach — `cmd/tui/api_client_test.go`
already demonstrates the pattern for other views).

**Acceptance criteria:** (1) `go test ./cmd/tui/...` green including new
cases. (2) The view renders zero sources gracefully (empty-state message,
not a crash) — a boundary case explicitly tested. (3) No keybinding in
this view triggers any mutating HTTP call (a test asserts the view's
`api_client.go` additions are strictly `GET`-only methods). (4) §11.4.170-
class host-rendered visual proof: a snapshot of the new view's rendered
terminal output (bubbletea's own test-rendering harness) is captured as
evidence, not merely asserted via string content.

**Composes with:** §11.4.170 (rendered-UI visual proof, applied to a TUI
rather than a web/native UI — the applicable analogue), the "all clients"
requirement in the feature brief (this item is what makes the claim true
for the TUI specifically, closing GAP-2 from `WIRING_PLAN.md` §0).

**Depends on:** G67 (REST endpoints to read). Explicitly the LOWEST
priority landable item — additive, non-blocking for the ingestion pipeline
itself, and deliberately scoped read-only per the TUI's existing role.

---

## G71 — e2e/full-automation test: real `anthropics/skills` pipeline run

**Type:** Task

**WHAT:** A full-automation, re-runnable-without-manual-intervention
(§11.4.98) end-to-end test that registers `anthropics/skills` (CATALOG.md
Tier-A pick: small, stable, public, ~43 commits) as a source via the REST
API (G67), triggers a sync (G66), and asserts real imported `skills` rows
+ `skill_source_mappings` rows + captured evidence land under
`qa-results/gh_skills_ingestion_e2e/<run-id>/` (following the exact
`qa-results/<run-id>/` convention already in use — confirmed subdirectories
`g32_f1`, `infra_fix`, `p1t1_remediation`, etc.). A checked-in tiny fixture
(recorded API responses for `anthropics/skills`'s tree/commits/contents at
capture time) provides the deterministic offline path for CI-without-a-
token; the LIVE path is a separate, additional real-network run that
honestly `SKIP`s-with-reason (`credentials_absent`/
`network_unreachable_external`, §11.4.3) when no token/network is available
— never a fake PASS.

**Affected scope:** `internal/source/sync/e2e_anthropics_test.go` (new, or
a top-level `test/e2e/` location if the project's convention prefers one
— **UNCONFIRMED** which the landing agent should pick; no existing
top-level `test/` directory was found in this tree, so co-locating under
`internal/source/sync/` alongside the orchestrator's own tests is the
lower-risk default), plus a small recorded-fixture data file (JSON,
captured from the real repo, cited with its capture date).

**Reproduction/trigger:** Run the test twice consecutively
(`-count=2`, §11.4.98 re-runnability proof) against the recorded fixture
and confirm identical, self-cleaning results both times (no duplicate
imports on the 2nd run — proves the idempotent-unchanged-SHA path, G65
acceptance criterion 4, exercised end-to-end rather than only at the unit
layer).

**Acceptance criteria:** (1) offline (fixture-driven) run is green in
every CI invocation, no network/token dependency. (2) live run either
PASSes with real captured evidence (imported skill count, provenance rows,
API-response hashes) or honestly SKIPs with a named reason — never a
silent no-op reported as PASS. (3) `-count=2` run is byte-identical in
outcome (§11.4.50 deterministic consistency). (4) Evidence path is
recorded in the test's own output, not merely implied.

**Composes with:** §11.4.98 (fully automated, re-runnable), §11.4.83
(docs/qa evidence trail — this item's `qa-results/` output IS that
trail for this feature), §11.4.3 (topology-appropriate SKIP, never
PASS-by-default).

**Depends on:** G67, G68, G69 (at least the REST path must exist to drive
the e2e run through a real client surface, not a direct package call —
the "full-automation" mandate means driving the SAME interface an operator
would use).

---

## G72 — Stress + chaos test suite for the ingestion pipeline

**Type:** Task

**WHAT:** Per §11.4.85: **stress** — import ≥2,000 synthetic `SKILL.md`
fixtures (mega-repo scale, matching CATALOG.md B1/B3's real 1,900–3,700-
skill repos) through the full G65 orchestrator pipeline, N≥10 concurrent
`ScanOne` calls across DISTINCT sources (proving true parallelism, not
just the single-source lock from G65's own test), boundary cases (empty
repo / 0 `SKILL.md` files / one pathologically huge `SKILL.md`). **Chaos**
— GitHub 500/timeout injected mid-scan (categorized recovery, resume on
next tick), rate-limit-exhausted response injected (honest backoff per
G58's `RateLimitStatus`, no crash), SIGKILL of the process mid-`ImportSkillModel`
(transaction rollback verified — zero half-imported skills on restart),
corrupt/malformed YAML frontmatter injected mid-batch (parse error
recorded for that ONE file, scan continues for the rest — no whole-batch
abort on one bad file).

**Affected scope:** `internal/source/sync/stress_test.go` (new),
`internal/source/sync/chaos_test.go` (new). Reuses the project's own
`stress_chaos.sh`-class helpers if/when they exist at the shell-script
layer (§11.4.85 names `ab_stress_run`/`ab_chaos_kill_pid_during`-class
helpers as the constitution's own convention; **UNCONFIRMED** whether this
specific project has already vendored that helper library — if absent,
this item's Go-level tests stand on their own without it, and vendoring
the shared helper is a separate, out-of-scope infra item).

**Reproduction/trigger:** Each chaos scenario is deliberately injected
(fault-injection, not waited-for) — e.g. a fake `github.Client` transport
that returns a 500 on the Nth call, a `context.WithTimeout` shorter than a
deliberately slow fixture response, an `os/exec`-spawned child process
killed via `SIGKILL` mid-transaction in a harness process.

**Acceptance criteria:** (1) stress run's p50/p95/p99 per-file import
latency captured and recorded (not merely "it finished"). (2) N≥10
concurrent different-source scans show no deadlock, no resource leak
(goroutine count returns to baseline post-run). (3) every chaos scenario's
recovery is CATEGORIZED (a `network`/`upstream`/`process`/`data`-class
label on the recorded error), never a bare swallowed panic. (4) the
corrupt-YAML scenario proves per-file isolation: N-1 valid files import
successfully alongside the 1 rejected file in the SAME batch. (5) the
SIGKILL scenario proves zero half-imported skills via a post-restart query
(`skills` row count for the interrupted import is exactly 0 or exactly
"fully committed", never partial).

**Composes with:** §11.4.85 (stress+chaos mandate this item directly
satisfies for this feature), §11.4.5/§11.4.69 (every PASS cites its
captured-evidence artifact — latency JSON, categorized-error log, restart-
state snapshot — under `qa-results/`).

**Depends on:** G65 (the orchestrator under test). Parallelizable with
G71 once G65 is green.

---

## G73 — Vendor Challenges + HelixQA constitution submodules (blocking dependency)

**Type:** Task

**WHAT:** DESIGN.md §2.E already flags this honestly: "these dirs are
constitution submodules not yet vendored in this project tree — the
tracked item carries the wiring dependency." Verified independently: no
`Challenges/` or `HelixQA/` directory (or `.gitmodules` entry pointing at
`vasic-digital/Challenges` / `HelixDevelopment/HelixQA`) was found anywhere
under this project's tree during this session's inspection. Per
§11.4.27(B), this project's Constitution-mandated 100%-test-type-coverage
requires these two submodules incorporated (recursive per CONST-047) BEFORE
a genuine Challenge/HelixQA bank entry for THIS feature (G74) can exist —
adding a bank-entry file with no incorporated runner would be an inert,
unwired artifact (§11.4.124/§11.4.197 class of defect).

**Affected scope:** `.gitmodules` (edit, this project's own top-level, not
`$REL`'s — **UNCONFIRMED exact path**, since this project's overall
`.gitmodules` location was not inspected this session; a landing agent
must locate it first), plus whatever `helix-deps.yaml`-declared recursive
sync mechanism this project already uses (confirmed present:
`$REL/helix-deps.yaml` already declares `llms_verifier`, `helix_llm`,
`helix_agent` as grouped submodule dependencies — this item follows that
SAME manifest-then-sync pattern, adding `Challenges` and `HelixQA` entries
to it).

**Reproduction/trigger:** N/A (infrastructure/vendoring item). Verification:
the submodules resolve at the canonical path the manifest declares, and
each ships its own test suite green (per §11.4.31's own anti-bluff
guarantee — a bootstrapped consuming project running `incorporate-submodule`
then running the submodule's OWN tests against the bootstrapped layout).

**Acceptance criteria:** (1) `Challenges` and `HelixQA` resolve at their
canonical `helix-deps.yaml`-declared paths. (2) Each submodule's own test
suite passes against this project's bootstrapped layout. (3)
`helix-deps.yaml` updated with both new entries following the existing
`{name, ssh_url, ref, why, layout}` schema already in use for
`llms_verifier`/`helix_llm`/`helix_agent`.

**Composes with:** §11.4.27 (100%-test-type-coverage's Challenges/HelixQA
requirement), §11.4.28/§11.4.31 (dependency-manifest mandate), §11.4.197
(a research/feature effort must not stay un-wired — this item exists
precisely so G74 is not a backlog-forever dead file).

**Depends on:** none (can run fully in parallel with every other item in
this list — it is a project-infrastructure item, not a feature-code item).
Unblocks G74 only.

---

## G74 — HelixQA Challenge bank entry for skill-source ingestion

**Type:** Task

**WHAT:** Once G73 lands, add a Challenge bank entry (following whatever
concrete bank-file format `Challenges`/`HelixQA` (now vendored) defines —
**UNCONFIRMED exact schema until G73 lands and the real submodule's own
bank format can be read**) that: registers a real (or fixture-recorded,
per G71's dual-path convention) source, triggers a sync, and scores PASS
only on real imported-skill evidence — never a metadata-only/config-only
PASS (§11.4/§11.4.1). A HelixQA autonomous QA session entry drives this
Challenge without manual intervention.

**Affected scope:** exactly 1–2 new files inside the vendored
`Challenges`/`HelixQA` bank directory structure (path unknown until G73
lands; sized here as "however many files that submodule's own convention
requires for one new bank entry" — typically 1 definition file + 1
optional fixture file per that submodule's existing pattern).

**Reproduction/trigger:** Run the HelixQA autonomous session against this
one bank entry; PASS requires the SAME captured-evidence standard as
G71's e2e test (imported-skill rows, provenance, captured artifact path).

**Acceptance criteria:** (1) the Challenge scores PASS only when real
skills are genuinely imported (a deliberately-broken fixture — e.g. an
unreachable source — must score FAIL/SKIP-with-reason, never PASS,
proving the Challenge itself is not a bluff per §11.4.107(10) applied to
Challenge banks). (2) the bank entry is discoverable and runs via the
project's normal HelixQA invocation path, not a bespoke one-off script.

**Composes with:** §11.4.27(B) (100%-test-type-coverage's Challenges/
HelixQA clause, the specific clause this item satisfies for THIS feature).

**Depends on:** G73 (submodules must exist first), G67/G71 (the real
pipeline + its e2e proof this Challenge exercises).

---

## G75 — Docs: README/API/CLI reference sync for the new surfaces

**Type:** Task

**WHAT:** Update the project's tracked documentation to reflect the new
surfaces: `README.md`'s endpoint list (the existing `buildRouter`
`GET /` handler at `cmd/server/main.go:355-376` already enumerates every
live route as a JSON array — the new skill-source routes from G67 must be
added to THAT SAME list, keeping the self-describing `/` response in sync
with reality, per §11.4.108 — a route missing from that list while being
live is exactly the kind of drift this project's own conventions guard
against elsewhere), any `docs/API.md`-equivalent reference (cited by
`cmd/cli/main.go:187`'s comment "see ... docs/API.md → 'Health & Info →
GET /health'", confirming such a doc exists and is actively cross-
referenced by code comments — **UNCONFIRMED exact current contents**,
not read this session), and the CLI's own `--help` text (already covered
by G68's acceptance criteria, cited here for completeness of the doc-sync
sweep).

**Affected scope:** `cmd/server/main.go` (edit — the `GET /` endpoint-list
literal, ~3 new lines), whatever `docs/API.md`-equivalent file the
`cmd/cli/main.go:187` comment references (path to be confirmed by the
landing agent — not located this session), `README.md` (if this project's
`README.md` carries a route/command inventory — to be confirmed at landing
time against the actual file, not assumed here).

**Reproduction/trigger:** N/A (doc sync). Verification: the `GET /`
response's endpoint list and the actually-registered route set are
compared programmatically (a test iterating `router.Routes()` against the
literal string list) — proving the two never drift, rather than a manual
eyeball check.

**Acceptance criteria:** (1) the `GET /` response's endpoint array includes
every new G67 route. (2) a test asserts `router.Routes()`'s real path set
is a superset of (or exactly matches, modulo the open `/health`/`/`
routes) the literal list in the handler — a paired §1.1 mutation adding an
undocumented route to `buildRouter` without updating the literal list makes
this test go RED. (3) whatever `docs/API.md`-equivalent exists gains an
entry for each new endpoint, cross-referenced the same way the existing
health-route entry already is (per the `cmd/cli/main.go:187` comment's own
citation style).

**Composes with:** §11.4.12/§11.4.60 (documentation always-sync mandate),
§11.4.108 (the self-describing `/` response IS a runtime artifact that can
itself drift from the real route set — this item closes that specific
drift vector for the new routes).

**Depends on:** G67, G68, G69, G70 (documents the surfaces those items
land). Landed last, after every user-facing surface exists.

---

## Dependency graph (summary)

```
G53 ─┬─────────────────────────────► G57 ─┬─► G62 ─► G63 ─┐
G54 ─┼─► G64 ◄───────────────────────────  │              │
G55 ─┼─► G59                          G61 ─┘              │
G56 ─┴─► G63, G64                     G60 ─► G61           │
G58 ─┬─► G59                                               │
     └─► G65                                               │
G59 ──────────────────────────────────────► G65            │
G61 ──────────────────────────────────────► G62, G63       │
G62 ──────────────────────────────────────► G63             │
G63 ────────────────────────────────────────────────────────┤
G64 ──────────────────────────────────────► G65, G67        │
G65 ──────────────────────────────────────► G66, G69        │
G66 ──────────────────────────────────────► G67             │
G67 ──────────────────────────────────────► G68, G69, G70, G71, G73
G73 (independent) ──────────────────────► G74
G71, G72 ◄── G65/G67 (verification-only, no downstream deps)
G68, G69, G70 ──────────────────────────► G75
```

Parallelizable rounds (§11.4.58 PWU pipeline, ≥3-stream default per
§11.4.103): **Round 1** — G53, G54, G55, G56, G58, G60, G73 (7 fully
independent items, ideal for ≥3 concurrent streams). **Round 2** — G57,
G59, G61 (each needs exactly one Round-1 item). **Round 3** — G62, G64.
**Round 4** — G63. **Round 5** — G65. **Round 6** — G66. **Round 7** —
G67. **Round 8** — G68, G69, G70 (parallel). **Round 9** — G71, G72
(parallel). **Round 10** — G74. **Round 11** — G75 (last).

---

## Sources verified 2026-07-16

Every real-file claim in this document is carried forward from, and
grounded in, `WIRING_PLAN.md`'s own "Sources verified" footer (the exact
same `file:line` citations, not re-derived). No new external repo/library
claim is introduced in this file beyond what `CATALOG.md` and
`WIRING_PLAN.md` already verified. Points marked `UNCONFIRMED` above
(exact `docs/API.md`-equivalent path, exact top-level `test/` convention,
exact `.gitmodules` path, exact Challenges/HelixQA bank-file schema) were
not resolved this session and are explicitly left as landing-time
discovery work, never guessed (§11.4.6).
