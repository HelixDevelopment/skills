# DESIGN — GitHub skill-source ingestion for the HelixKnowledge Skill Graph System

**Revision:** 2
**Last modified:** 2026-07-16T00:00:00Z
**Status:** DESIGN ONLY (Phase 1 research/design — NO implementation landed). Feeds the SDD implementation waves in `PLAN.md`.
**Scope:** read GitHub repositories as registered *skill sources*; fetch + parse their `SKILL.md` skills; analyze, dedup, import missing ones, and extract "game-changer" enhancement deltas for skills we already have; re-scan sources regularly; expose source management over CLI + REST + MCP (+ TUI); cover with the full §11.4.27 test matrix + anti-bluff captured evidence.
**Honest boundary (§11.4.6):** this is a design. Every "the System does X" below about the *current* system is a verified FACT from the codebase map; every "we WILL add Y" is a proposal. Nothing here is built.

---

## §1. Current model (Task 2 — verified facts)

Module `github.com/HelixDevelopment/skills` (Go, Postgres + pgvector), base dir: repo root (promoted from `.../Agent_AI_Skill_Tree_Development/project` under HXC-159 T-P3.01).

### 1.1 Skill representation (`internal/models/skill.go`)
- `Skill{ ID uuid, Name string (UNIQUE identity key), Version, Title, Description, Content (markdown body), Metadata JSONB (Tags[]/Domain/Complexity), Status (draft|validated|active|deprecated), Kind (atomic|composite|umbrella), CreatedAt/UpdatedAt }`. Runtime-only: `Dependencies[]`, `Resources[]`, `Embedding vector(768)`, `TreeDepth`.
- **Identity = `Name`** (DB `UNIQUE`), used verbatim as the resolution key everywhere. **No normalization/slug/case-fold function exists.** `ID` is re-minted per import → NOT a cross-instance identity.
- **Granularity axis = `Kind`** (aggregation): atomic = 0 outgoing `composes` edges; composite/umbrella = ≥1. Orthogonal to `Metadata.Complexity` (difficulty).
- **It is a directed graph (DAG on the hard-closure subset).** Edge = `SkillDependency{ SkillID, DependsOn, RelationType, Optional, SortOrder }`. 6 `RelationType`s:
  - Hard-closure (acyclicity-enforced, transitively pulled): `requires`, `extends`, `composes` (whole→part).
  - Advisory (may cycle, not auto-pulled): `recommends`, `related_to` (sym), `alternative_to` (sym).
  - `part_of` is explicitly UNSUPPORTED (`ErrPartOfUnsupported`).

### 1.2 DB schema (`migrations/00{1,2,3}_*.sql`, embedded, applied fail-closed at server start)
- `skills` (name UNIQUE, JSONB metadata, vector(768) embedding, kind CHECK, pg_trgm name/title indexes).
- `skill_dependencies` (PK `(skill_id, depends_on, relation_type)`, FK CASCADE, 6-value CHECK).
- `resources` (`skill_id` FK, `url`, `title`, `resource_type` CHECK, **`fetched_hash`**, `content_cached`, `last_validated`). ← the only existing "external ref + content hash" primitive.
- `evidences`, `skill_registry` (health: missing_deps[], stale, coverage, auto_expand), `audit_log` (ts/event/skill_id/details JSONB — the lightweight job/event log).
- **NO provenance/source columns anywhere; NO `skill_sources` table** (confirmed). This is entirely net-new.

### 1.3 Create/import paths
- **Canonical live create = MCP `skill_create` → `Store.ImportFromTOML`** (`internal/skill/import_export.go`): strict TOML decode → resolve all 6 edge types (+ `depends_on`/`prerequisite` aliases, `[[skill.components]]`→`composes`) in one name→ID batch → cycle-check hard edges → single transaction (skill + registry + deps + resources + recalc coverage + audit `skill.imported`). Always `Status=draft`.
- `Store.Create` (upsert `ON CONFLICT(name)`), `Store.ExportToTOML` (byte-stable inverse), `AddDependency`/`RemoveDependency` (cycle-checked).
- **Native skill format on disk = TOML** (`seed/skills/*.toml`, one skill/file; no automated seeder — manual `skill import`/MCP only). `seed/CORPUS.yaml` is the closed-world DAG spec.
- `internal/registry` = the *health* registry (coverage/stale/review), **NOT** a source registry.

### 1.4 Surfaces (where source management must wire)
1. **REST (live):** `cmd/server/main.go buildRouter`, base `/api/v1`, X-API-Key auth, CORS allowlist. Live routes are **read-only** skill routes. `internal/api.Server` defines full CRUD (`POST /skills`, `/skills/import`, …) but is **unwired dead code** (G01) — do NOT rely on it as the live surface.
2. **MCP:** `internal/mcp/server.go RegisterTools` — 7 tools (`skill_search/get/tree/create`, `learn_from_project`, `missing_skills`, `get_coverage`); stdio/http(`/mcp/v1`)/ACP.
3. **CLI:** `cmd/cli` (cobra, binary `skill-system`) — groups `skill/search/registry/expand/learn/config`, all HTTP to the API.
4. **TUI:** `cmd/tui` (bubbletea) — read-only browse/search/tree/registry.
5. **Worker + scheduler:** `cmd/worker` → `internal/worker.Runner`. **In-process `time.Ticker` loops + in-memory `jobChan` (cap 100); NO OS cron, NO durable jobs table** (jobs persisted only as `audit_log` events). JobTypes `autoexpand|validate|codeanalysis|registry_review`, retry w/ backoff. A separate `registry.ReviewScheduler` (robfig/cron/v3) exists but is unwired. **← the exact seam for a periodic re-scan job.**
6. `internal/codeanalysis` — reads a LOCAL project dir (tree-sitter/regex), maps code patterns→existing skills, path-guarded against `CodeAnalysis.AllowedRoot`. **Closest precedent to "read an external repo"** (but local-only, no SKILL.md, unwired for create — G03).
7. `internal/autoexpand` — LLM-powered programmatic skill creation; **unwired dead code** (G03/G20). Reusable as the enhancement-merge LLM path.

### 1.5 Config (`internal/config`)
File (TOML) + `${VAR}`/`${VAR:-def}` interpolation + `HELIX_*` env overrides + `validate()` fail-closed on residual `${`. **No DB-backed config.** Precedents to copy: `autoexpand.llm_api_key = "${ANTHROPIC_API_KEY}"` (secret by NAME only, §11.4.10) and `codeanalysis.allowed_root` (env override, fail-closed empty).

### 1.6 Tests / docs / tracker
- Co-located `*_test.go`; live-Postgres integration via `testdb_helper_test.go` (§11.4.27 no-mocks-beyond-unit); inline paired `§1.1` mutation tests; evidence under `project/qa-results/<run-id>/`. No Challenges/HelixQA dirs vendored in this tree yet; no `ab_pass_with_evidence` helper present (hand-rolled assertions).
- Register: `docs/research/mvp/Agent_AI_Skill_Tree_Development/GAPS_AND_RISKS_REGISTER.md` (one level ABOVE `project/`). IDs `G<NN>` grouped by severity; each maps to a design doc `research/g<NN>_<slug>_design.md`. Highest seen: **G47** (a pending §11.4.66 operator decision on `G0x/R0x/P0x` vs `ATM-NNN`). No workable-items DB yet (G40 design-only).

### 1.7 The mapping question (SKILL.md → this Skill model)
| SKILL.md (source) | this Skill model | mapping decision |
|---|---|---|
| frontmatter `name` (kebab) | `Name` | **namespace it** → `<source_slug>.<name>` (fits the dotted convention, preserves `UNIQUE(name)`, avoids cross-repo collision). Base name kept in provenance. |
| frontmatter `description` | `Description` | direct. `Title` = frontmatter name titleized OR first `# H1` of body. |
| markdown body | `Content` | direct. |
| `license` | provenance + `Metadata` | drives the import license-gate (§4.4). |
| `allowed-tools`, `disable-model-invocation`, `arguments`, unknown fields | `Metadata` JSON (lossless) | **no first-class home** — stored, not modeled (honest boundary: we do not model Claude-Code invocation semantics). |
| `scripts/`, `references/`, `assets/`, body URLs | `Resources[]` | `resource_type` code/article; the SKILL.md permalink → an `official-doc` resource with `fetched_hash` = content hash. |
| — (SKILL.md has **no dependency schema**) | `Dependencies[]` | **none** — imported skills are **atomic leaves; we do NOT invent edges (§11.4.6).** (Bundle/marketplace manifests → `composes`/`umbrella` is a flagged Phase-2 enhancement, not claimed now.) |
| — | `Kind` | `atomic` (default). |
| — | `Status` | `draft` (matches `ImportFromTOML`; then flows through existing validation/registry review). |

---

## §2. Feature architecture (Task 3)

New package family (all decoupled, project-agnostic-friendly): `internal/source/` with sub-packages `github` (fetch), `skillmd` (parse), `mapper` (SKILL.md→Skill + license gate), `dedup` (classify), `enhance` (delta proposals), `sync` (orchestrator). Import write goes through a new `internal/skill` method (§4.3). Scheduler seam in `internal/worker`. Surfaces in `cmd/server` + `internal/mcp` + `cmd/cli` + `cmd/tui`.

```
[config seed / CLI / REST / MCP]  → register source
        │
        ▼
  skill_sources (DB, runtime SoT)  ──cheap SHA gate──► GitHub commit ref
        │ (poll interval elapsed / manual sync)
        ▼
 source/sync orchestrator (single-owner-per-source lock §11.4.119)
   1 fetch  → source/github  (Trees recursive list + blob fetch; ETag+SHA; rate-limit aware)
   2 classify → source class (vendored | marketplace | link_index)   [L → resolve links to child sources]
   3 parse  → source/skillmd (YAML frontmatter + body + scripts/refs/assets) → ParsedSkill (+content hash)
   4 map    → source/mapper  (namespaced Name, Metadata, Resources, license gate) → models.Skill + Provenance
   5 dedup  → source/dedup   (NEW | DUPLICATE-of-existing | VARIANT)
        ├─ NEW        → skill.ImportSkillModel (single tx: skill+resources+provenance+registry+audit)
        ├─ DUPLICATE  → write provenance mapping to existing skill; run enhance (step 6)
        └─ VARIANT    → import (namespaced) + advisory related_to/alternative_to edge; flag for review
   6 enhance→ source/enhance (delta extract; NEVER blind-overwrite §11.4.122 → enhancement PROPOSAL row)
        ▼
  audit_log events + skill_sources.last_* status  (honest: no durable per-file jobs table — see §5 limitation)
```

### 2.A Skill-source config & registry
**DB (runtime source of truth) — migration `004_skill_sources`:**
- `skill_sources`: `id UUID PK`, `name TEXT UNIQUE` (human handle, e.g. `anthropic-official`), `repo_url TEXT NOT NULL`, `host TEXT DEFAULT 'github'`, `owner TEXT`, `repo TEXT`, `ref TEXT DEFAULT 'main'`, `path_glob TEXT DEFAULT '**/SKILL.md'`, `source_class TEXT DEFAULT 'unknown' CHECK(IN 'vendored','marketplace','link_index','unknown')`, `auth_ref TEXT` (**env-var NAME holding the token — NEVER the token, §11.4.10**), `enabled BOOLEAN DEFAULT TRUE`, `poll_interval_minutes INT DEFAULT 1440`, `license_default TEXT`, `last_synced_at TIMESTAMPTZ`, `last_commit_sha TEXT`, `last_status TEXT`, `last_error TEXT`, `created_at/updated_at`.
- `skill_source_mappings`: `id UUID PK`, `source_id FK→skill_sources ON DELETE CASCADE`, `source_path TEXT NOT NULL` (path of the SKILL.md in the repo), `skill_id UUID NULL FK→skills ON DELETE SET NULL` (the skill this file produced, or the existing skill it deduped to), `upstream_name TEXT`, `upstream_content_hash TEXT`, `upstream_license TEXT`, `last_action TEXT CHECK(IN 'imported','enhanced','deduped','skipped_license','unchanged','error')`, `first_imported_at`, `last_seen_at`, **`UNIQUE(source_id, source_path)`**. This row is the stable per-upstream-file identity across re-scans (independent of the re-minted skill `ID`).
- `skills.origin TEXT NOT NULL DEFAULT 'native' CHECK(IN 'native','imported')` — cheap filter/segregation of imported vs authored skills.
- **Config seeds, DB is authoritative:** `[[skill_sources]]` in `config.toml` is a declarative bootstrap upserted (by `name`) into `skill_sources` at startup; runtime add/remove via API/CLI/MCP mutates the DB. (Mirrors the constitution's config-seeds/DB-SoT pattern; a source added at runtime survives; a config entry re-asserts on restart.)
- `[source_sync]` config: `default_poll_interval_minutes`, `api_base` (default `https://api.github.com`), `clone_fallback_enabled`, `license_allowlist` (e.g. `["Apache-2.0","MIT","BSD-3-Clause","CC-BY-4.0"]`), `max_skills_per_scan`. Token: `github_token = "${GITHUB_TOKEN}"` (name-only interpolation) + `HELIX_GITHUB_TOKEN` override, following the `llm_api_key` precedent exactly.

### 2.B Fetch / parse
- **Access strategy — GitHub REST API first, shallow-clone fallback.**
  - List: `GET /repos/{owner}/{repo}/git/trees/{ref}?recursive=1` → one call enumerates the whole tree; filter by `path_glob` to find every `SKILL.md`. (Handles the 1,900–3,700-skill mega-repos in ~1 list call.)
  - Fetch each: `GET /repos/{owner}/{repo}/contents/{path}` (or blob API) with `If-None-Match` (ETag) — unchanged blobs return 304 (cheap).
  - Change gate: `GET /repos/{owner}/{repo}/commits/{ref}` → HEAD SHA; if `== last_commit_sha`, the whole scan is a no-op (1 call). On change, `GET /repos/{owner}/{repo}/compare/{last_sha}...{ref}` → only changed `SKILL.md` paths re-fetched.
  - **Fallback:** shallow `git clone --depth 1` into a temp dir under a configured `AllowedRoot` (path-guarded exactly like `codeanalysis.ValidateProjectPath`, rootless §11.4.161) when the tree is huge, the API is rate-limited, or for a Tier-C link-index whose linked repos are cloned. Cleanup in `trap`/defer (§11.4.14).
  - **Auth (§11.4.10):** token read from the env var NAMED in `skill_sources.auth_ref` (or the global `github_token`); the VALUE is never stored/logged. Public repos work token-less (60 req/h). If an operator supplies a token to store, run the §11.4.10.A pre-store leak audit first.
  - **Rate limits:** read `X-RateLimit-Remaining/Reset`; on exhaustion → set `last_status='rate_limited'`, back off until `Reset`, **honest SKIP-with-reason, never fake success** (§11.4.201 — the backoff guard asserts the REAL header, not a proxy).
- **Discovery + classification:** auto-detect `source_class` — presence of `SKILL.md` files ⇒ `vendored`; a root `.claude-plugin/marketplace.json` ⇒ `marketplace` (resolve each plugin `source` → register/scan as child sources); an `awesome-*`/README-of-links with no SKILL.md ⇒ `link_index` (extract GitHub links → propose child sources, operator-gated, never auto-register arbitrary repos §11.4.122).
- **Parser (`internal/source/skillmd`):** split YAML frontmatter (`---`…`---`) from the markdown body; tolerate the 2-field minimum, CRLF, unicode, no-frontmatter (→ derive name from dir), and **unknown fields (store lossless)**. Detect sibling `scripts/`, `references/`, `assets/`. Output `ParsedSkill{ Name, Description, License, RawFrontmatter map[string]any, Body, Scripts[], References[], Assets[], SourcePath, ContentHash }`. Self-validated with golden-good + golden-bad fixtures (§11.4.107(10)).

### 2.C Analyze / import / enhance
- **Analyze:** compute a normalized `ContentHash` (sha256 of the full raw frontmatter+body file text after BOM-strip/CRLF normalization — round-3 N1/N3 remediation of an earlier name+body-only formula that missed Description/Version and was forgeable via an embedded NUL byte); extract domain/complexity heuristics into `Metadata`; compute a "richness" signal (description length, body length, has scripts/references); optionally embed (reuse the existing 768-dim embedding pipeline) for semantic dedup.
- **Import (write path):** new `Store.ImportSkillModel(ctx, skill, provenance)` — a sibling to `ImportFromTOML` reusing the SAME single-transaction body (skill upsert + resources + registry + audit) but taking a `models.Skill` directly (no TOML round-trip; SKILL.md is YAML, not TOML). Writes the `skill_source_mappings` row in the same tx. `origin='imported'`, `Status='draft'`. Imported atomic skills carry **no dependency edges** (§1.7).
- **Dedup policy (never blind-overwrite §11.4.122) — 3 outcomes:**
  1. **Exact upstream identity** `(source_id, source_path)` already mapped + `upstream_content_hash` unchanged ⇒ **UNCHANGED** (skip; `last_seen_at` bumped).
  2. **Cross-source/native match** by normalized base-name AND content similarity (trigram/name + optional embedding cosine, threshold **calibrated on our own fixtures, not hardcoded from literature** §11.4.6):
     - no match ⇒ **NEW** (import).
     - strong match to a skill we already have ⇒ **DUPLICATE** (do NOT create a 2nd copy; write provenance mapping to the existing skill; run enhancement).
     - same base-name, materially different content ⇒ **VARIANT** (import namespaced + add an **advisory** `related_to`/`alternative_to` edge to the existing skill — advisory edges are safe to auto-add; hard edges are never auto-invented; flag for review).
  - **Conservative default (§11.4.6/§11.4.122):** when the DUPLICATE-vs-VARIANT boundary is uncertain, treat as VARIANT + flag — never silently merge or overwrite.
- **Enhance ("game-changer delta" for skills we already have):**
  - Structured diff of upstream body vs our version → a `DELTA` (sections/steps/techniques present upstream & absent in ours; new `allowed-tools`/scripts). Optionally LLM-scored for novelty via the existing `autoexpand` LLM provider.
  - **NEVER blind-overwrite (§11.4.122).** The delta lands as a `skill_enhancement_proposals` row (migration `005`) referencing target skill + upstream source + the delta + upstream license; `status='proposed'`. Merge is **gated** — operator/QA approval (§11.4.185) or an approved LLM-assisted merge that still lands as a **draft** for review, appends/augments `Content` + new `Resources`, bumps `Version`, audits `skill.enhanced_from_source`.
  - Honest boundary (§11.4.6): "game-changer" is a heuristic + LLM judgment, imperfect — proposals are SURFACED for human decision, never asserted-as-correct or auto-applied.
- **License gate (§4.4):** upstream license ∈ `source_sync.license_allowlist` ⇒ import body. Else (source-available / unknown) ⇒ import **metadata + a resource link only**, `last_action='skipped_license'`, surfaced for operator decision. Never silently redistribute source-available content (e.g. anthropics `docx/pdf/pptx/xlsx`).

### 2.D Regular re-scan
- **New `JobType='source_rescan'` + `sourceRescanWorker` ticker** in `internal/worker.Runner` (interval = `min(enabled sources' poll_interval)` clamped to a floor, or a global default). Per due source: cheap SHA gate → if unchanged, no-op; else compare API → only changed/new `SKILL.md` re-parsed → dedup → import NEW / propose ENHANCEMENT for edited. **Idempotent:** `(source_id, source_path, content_hash)` determines the action; unchanged files skipped. Change detection is **content-hash (§11.4.86)** with the commit SHA as the cheap outer gate. **This key covers file-content changes ONLY — see §3 D9:** a change to a source's `sourceSlug`, `licenseAllowlist`, or permalink configuration is a SEPARATE axis `content_hash` cannot detect; the re-scan orchestrator MUST additionally force a full remap on a config edit, never rely on `content_hash` alone to decide "nothing changed."
- **Single-owner-per-source lock (§11.4.119):** a source is scanned by exactly one worker at a time (advisory lock keyed on `source_id`) so concurrent scans of *different* sources run parallel but the same source never double-imports.
- **Auto-discovery (optional, flagged):** periodically crawl `github.com/topics/claude-code-skills` + resolve link-index repos → **propose** new sources (operator-gated add, never auto-register §11.4.122).
- Alternative considered: reuse the unwired `registry.ReviewScheduler` (robfig/cron/v3) instead of a raw ticker — either works; the ticker matches the live worker pattern and is the lower-risk default.

### 2.E Test coverage (§11.4.27 full matrix + anti-bluff §11.4.5/§11.4.69)
- **unit** (+ paired §1.1 mutation each): skillmd parser (missing description, unknown fields, no frontmatter, CRLF, unicode, giant body — golden-good/golden-bad §11.4.107(10)); mapper (namespacing, license gate, lossless unknown-field capture); dedup classifier truth-table (NEW/DUP/VARIANT); change-detection (content-hash + SHA gate); rate-limit backoff calc; github client request-building (mockable HTTP transport — unit only).
- **integration** (live Postgres, `testdb_helper`): register→import lands skill+provenance+resources rows; re-scan unchanged ⇒ no-op (row counts identical); re-scan edited ⇒ enhancement proposal, target skill unchanged; dedup-to-existing ⇒ no 2nd copy; license-restricted ⇒ metadata+link only.
- **e2e / full-automation (§11.4.98 re-runnable, §11.4.3 topology dispatch):** a real GitHub repo (`anthropics/skills` — small, stable, public) driven through the WHOLE pipeline via the API, asserting real imported skill rows + provenance + captured evidence under `qa-results/<run>/`. A **checked-in tiny fixture repo** (or recorded API responses) provides the deterministic offline path (runs in CI with no token); the live path is the additional real-evidence run and **SKIPs-with-reason (`network_unreachable_external`/`credentials_absent`) honestly** when unauthenticated rate limits or no token block it — never a fake PASS.
- **stress (§11.4.85):** import ≥2,000 synthetic SKILL.md (mega-repo scale); N concurrent source scans (single-owner lock holds); boundaries (empty repo, 0 SKILL.md, one huge SKILL.md). p50/p95/p99 captured.
- **chaos (§11.4.85):** GitHub 500/timeout mid-scan → categorized recovery + resume next tick; rate-limit injection → honest backoff, no crash; SIGKILL mid-import → tx rollback, zero half-imported skills; corrupt SKILL.md (bad YAML) → parse error recorded, scan continues.
- **Challenges + HelixQA banks:** a Challenge registers a source, syncs, scores PASS only on real imported-skill evidence; a HelixQA bank entry drives it autonomously. (Honest dependency: these dirs are constitution submodules not yet vendored in this project tree — the tracked item carries the wiring dependency.)
- **Anti-bluff:** every PASS via captured evidence (imported row counts, provenance rows, upstream content-hash, API-response hashes) under `qa-results/<run>/`; the parser + dedup analyzers are self-validated golden-good/golden-bad (§11.4.107(10)); a metadata-only/config-only/grep PASS is forbidden.

---

## §3. Design decisions (summary)
- **D1 Namespaced imported names** (`<source_slug>.<name>`) to preserve `UNIQUE(name)` + avoid cross-repo collision; base name in provenance for dedup. Rejected: overwrite native skills on name clash (violates §11.4.122); global UUID identity (breaks the name-keyed model).
- **D2 New `skill_sources` + `skill_source_mappings` tables + `skills.origin`** (nothing to extend — no provenance today). Rejected: overloading `resources.url` (per-resource, not per-source; no re-scan identity).
- **D3 GitHub REST API first, shallow-clone fallback** (Trees API = 1 list call for mega-repos; conditional requests for cheap re-scan). Rejected: clone-always (slow, disk-heavy for 3,700-skill repos).
- **D4 New `Store.ImportSkillModel` reusing the `ImportFromTOML` transaction** (SKILL.md is YAML not TOML; forcing a TOML round-trip is lossy/fragile).
- **D5 Enhancement = gated proposal, never auto-overwrite** (§11.4.122 + §11.4.185). LLM merge lands as draft for review.
- **D6 Re-scan = new `source_rescan` JobType on the existing worker ticker**, SHA-gated + content-hash idempotent (§11.4.86), single-owner-per-source (§11.4.119).
- **D7 Config seeds, DB is runtime SoT** for sources; token by env-var NAME only (§11.4.10).
- **D8 License gate on import** — allowlist ⇒ body; else metadata+link only.
- **D9 Re-scan key MUST include a config-axis rule, not `content_hash` alone (F2 remediation, round 4):** `mapper.Map` takes THREE caller-supplied config inputs that its `parsed.ContentHash` input (a hash of the upstream file's bytes only, computed in `skillmd.Parse`) never covers and can never reflect a change in — `sourceSlug` (the `Name` prefix), `licenseAllowlist` (the ALLOW/DENY verdict deciding real body vs. license-gated stub, and whether a "see upstream" `Resource` is attached), and `sourcePermalink` (the stub's `Resource.URL`). A re-scan gated on `(source_id, source_path, content_hash)` ALONE (as §2.D currently describes) would silently keep serving a STALE mapped output — a stale Name prefix, a stale ALLOW/DENY verdict, or a stale permalink — after an operator edits a source's configured slug, the global/per-source license allowlist, or the permalink template, even though the upstream file itself never changed. **Rule (binding on the not-yet-implemented re-scan orchestrator, §2.D):** a config change (slug, license allowlist, or permalink) forces a full remap of every affected `skill_source_mappings` row, independent of whether `content_hash` changed — the content-hash skip is a FILE-CONTENT-only optimization and MUST NEVER be read as "nothing about this mapping could have changed." The two options for implementing this (either is acceptable; NEITHER is built yet): (a) fold a hash of the config inputs (`sourceSlug` + a sorted `licenseAllowlist` join + `sourcePermalink`) into a second `config_fingerprint` column alongside `upstream_content_hash`, gating the re-scan skip on BOTH hashes matching; or (b) skip re-scanning ContentHash-unchanged files by default, but ALWAYS force a full remap of every row belonging to a source whenever that source's `license_default`/allowlist, slug, or permalink configuration is edited (a config-write-triggered remap sweep, independent of the regular polling cadence). Rejected: gating solely on `content_hash` (proven stale-serving per the scenario above) or re-deriving the config inputs implicitly from `parsed.ContentHash` (impossible — the hash mathematically cannot encode inputs it never received). See `internal/source/skillmd/parse.go`'s `ContentHash` field doc ("Scope" paragraph) for the equivalent honest-boundary statement at the code layer.

## §4. Risks, honest boundaries, and open items
- **R1 Format gap:** SKILL.md is YAML-frontmatter markdown; the native skill format is TOML. Mapping layer is net-new; Claude-Code-specific fields (`allowed-tools`, `arguments`, `disable-model-invocation`) have **no first-class home** → stored in `Metadata`, not modeled. Honest: we ingest skills-as-content, not Claude-Code invocation semantics.
- **R2 No DAG enrichment from import:** SKILL.md has no dependency schema, so imports are **atomic leaves; we do NOT invent edges (§11.4.6).** The System's DAG value grows only via native authoring or a later bundle/marketplace-manifest→`composes` mapping (flagged Phase-2, not claimed).
- **R3 Dedup ambiguity:** exact hash catches exact dups; semantic near-dups need embeddings + a threshold **calibrated on our own fixtures** (§11.4.6). Conservative default = VARIANT + flag (never silent merge).
- **R4 Licensing:** source-available (anthropics doc skills) must NOT be redistributed as ours; unknown license ⇒ link-only. **Operator decision needed (§11.4.66)** on the license allowlist.
- **R5 Rate limits:** 60/h unauth, 5,000/h authenticated; mega-repos need Trees + conditional requests + honest `rate_limited` status (§11.4.201), never fake.
- **R6 No durable per-file jobs table today:** scans are `audit_log` events + `last_*` status columns; a crash mid-scan is recovered by the idempotent next re-scan, but in-flight per-file progress is not persisted (consistent with the current worker design). A `source_scan_runs` table is an optional later hardening (flagged, not required).
- **R7 Quality of mega-aggregated repos** (sickn33/jeremylongshore) varies — import gate + dedup + draft-status + review pipeline mitigate; nothing auto-activates (all land `draft`).
- **Operator decisions (§11.4.66):** (1) license allowlist policy; (2) whether enhancement auto-merge is ever permitted or always operator/QA-gated; (3) the tracked-item ID scheme (G-scheme vs the pending **G47** ATM decision); (4) the default seed source list (proposal: `anthropics/skills` + `obra/superpowers` + register `anthropics/claude-plugins-official` as a marketplace to fan out to vendor repos).

## Sources verified 2026-07-16
Codebase facts: verified by read-only analysis of `github.com/HelixDevelopment/skills` at the current HEAD (`internal/models/skill.go`, `internal/skill/*`, `migrations/00{1,2,3}_*.sql`, `internal/api`, `internal/mcp`, `cmd/*`, `internal/worker`, `internal/config`, `internal/codeanalysis`, `internal/autoexpand`, `GAPS_AND_RISKS_REGISTER.md`). External skill-format facts: see `CATALOG.md` "Sources verified" footer (anthropics/skills, code.claude.com/docs/en/skills, agentskills.io, et al.).
