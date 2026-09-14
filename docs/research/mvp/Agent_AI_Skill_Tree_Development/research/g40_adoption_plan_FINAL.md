# G40 — Workable-Items SQLite DB Adoption Plan (FINALIZED)

**Revision:** 2
**Last modified:** 2026-07-18T01:34:00Z
**Status:** FINALIZED — design complete, implementation gated per §11.4.197
**Authority:** Constitution §11.4.93 (SQLite SSoT) / §11.4.95 (DB tracked in
git) / §11.4.74 (extend-don't-reimplement) / §11.4.28 (decoupling) /
§11.4.108 (four-layer fix-verification) / §11.4.6 (no-guessing)
**Design doc:** `research/g40_workable_items_db_adoption_design.md`
**Finding this plan closes:** `GAPS_AND_RISKS_REGISTER.md` G40 (HIGH,
§11.4.93/§11.4.95) + coupled G45 (closed-vocabulary not applied) + resolves
G47 tension (§11.4.54 id scheme)

---

## 1. Decision Summary

### Recommended path: Option (a) — literal Gxx/Rxx as atm_id

The design recommends **Option (a)**: store the literal `Gxx`/`Rxx` strings
(e.g. `"G40"`, `"R09"`) directly as the `items.atm_id` column value. This
requires **zero schema change** — `schema_embed.sql` v6 declares `atm_id` as
`TEXT NOT NULL` with no format `CHECK` constraint, and the engine's `add`
subcommand accepts an explicit `--id <id>` flag that does not force an
`ATM-NNN` shape.

**Why Option (a) over Option (b):**

| Criterion | Option (a): literal Gxx/Rxx | Option (b): mint ATM-NNN + alias |
|---|---|---|
| Schema change | None | Requires new `legacy_id` column (Gap D) |
| Cross-reference integrity | Preserved verbatim — the literal string used in 20+ docs IS the DB primary key | Requires parallel id maintenance or a mapping layer |
| Engine changes | None | Column addition + migration logic |
| §11.4.54 compliance | Yes — "stable, unique id" (the prefix is `G`/`R` rather than `ATM`, but the id is stable and unique) | Yes — fully ATM-conformant |
| Risk | Zero — additive import only | Low but non-zero — alias divergence risk |

**Final choice remains the operator's per G47 (§11.4.66).** This plan proceeds
on Option (a) as the default; if the operator picks Option (b), Phase 1 is
paused until Gap D (the `legacy_id` column) is resolved upstream.

---

## 2. Implementation Phases

### Phase 0 — Build + verify the Go workable-items binary

**Goal:** Confirm the constitution engine compiles and passes its own test suite
before any migration work begins.

**Steps:**
1. `cd constitution/scripts/workable-items && go build -o /tmp/workable-items ./cmd/workable-items/`
2. `go vet ./...` — must be clean
3. `go test ./...` — must be green (engine's own unit + integration + stress + chaos suites)
4. Document the binary version + schema version (`schema_embed.sql` v6) as
   captured evidence in the migration commit

**Acceptance criteria (§11.4.108 runtime-signature):**
- Binary exits 0 on `--help`
- `go test` reports PASS across all engine packages
- `schema_embed.sql` version confirmed as `'6'` (the runtime schema this
  design targets)

**Estimated effort:** ~30 minutes (one-shot build + verify)

**Sequencing:** Can run immediately — no dependency on in-flight work.

**Phase 0 Completion Record (2026-07-18T01:34Z, T4/feature/catalog-docs):**
- Binary built: `/tmp/workable-items` — `--help` exits 0 ✓
- `go vet ./...` — clean (zero warnings) ✓
- `go test ./...` — PASS (`ok  github.com/.../cmd/workable-items 0.595s`) ✓
- Schema version: `4` (NOT `6` — the plan's v6 assumption is incorrect;
  `schema.sql` line `('schema_version', '4')` is the live version.
  This is a GAP finding: the plan referenced v6 from `schema_embed.sql`
  but the actual schema file declares v4. Phase 1 import MUST target
  the live v4 schema, not the planned v6.)
- **Verdict: Phase 0 COMPLETED with one plan-vs-reality gap documented.**
  Phases 1–5 are UNBLOCKED — proceed using live schema v4.

---

### Phase 1 — Create the DB schema + migrate G01-G49 findings

**Goal:** Populate `docs/workable_items.db` with all 49 G-findings from the
markdown register, using the field-mapping table from the design doc §3.2.

**Steps:**
1. Run `workable-items add <type> <severity> --db docs/workable_items.db --id
   <Gxx> --title <T> --description <Evidence+Why-it-matters>
   --created-by Claude --assigned-to ''` once per G-item, using the
   classification table (see §4 below).
2. For every item, follow with `workable-items update --id <id> --status
   <classified-status> --closure-criteria <DECISION+test-coverage-text>`.
3. For items with genuine attempt/redo cycles (G01), add corresponding
   `item_history` rows — reconstructing dates from the register's own dated
   STATUS labels, never inventing dates (§11.4.6).
4. Run `workable-items validate --db docs/workable_items.db` — MUST be clean
   before proceeding.
5. **The `.md` register is NOT touched in this phase.** The DB is populated as
   a parallel representation while the hand-edited register remains the
   operative document.

**Acceptance criteria:**
- `docs/workable_items.db` exists and is non-empty (49 G-item rows)
- `workable-items validate` exits 0 (closed-set + §11.4.91 clarity invariants)
- `workable-items report --by-status --db docs/workable_items.db` shows
  status distribution matching the register's summary counts
- Classification table committed as a reviewable artifact (not silently inline)

**Estimated effort:** ~2-3 hours (classification judgment per item + mechanical
import + validation)

**Sequencing:** SAFE during active Go work per §11.4.96 — touches only a new
`docs/workable_items.db` file, never `project/` Go source. Can run as a
background design/tooling stream concurrently with P0.5.

---

### Phase 2 — Migrate R01-R24 requirements

**Goal:** Import the 24 requirement clusters from `REQUIREMENTS.md` as
`Type=Feature` rows (with per-R overrides for procedural items as `Type=Task`).

**Steps:**
1. Classify each R-item (see §4 below for the per-R type mapping).
2. Run `workable-items add` for each R-item with the classified type.
3. Run `workable-items validate` — must remain clean.
4. Run `workable-items diff --db docs/workable_items.db --issues
   <scratch-path>` to confirm no DB-vs-markdown divergence for the G-items
   (the R-items have no markdown register counterpart to diff against).

**Acceptance criteria:**
- 73 total rows in the DB (49 G + 24 R)
- `workable-items validate` exits 0
- Classification table for R-items committed as a reviewable artifact

**Estimated effort:** ~1-1.5 hours

**Sequencing:** Can run immediately after Phase 1. Also SAFE during active Go
work.

---

### Phase 3 — Wire the bidirectional sync (md→db, db→md)

**Goal:** Prove the DB can regenerate the markdown register byte-identically
(round-trip proof), then switch the register's write path to the DB.

**Steps:**
1. Run `workable-items sync db-to-md --db docs/workable_items.db
   --out-issues <scratch-path>` and diff against the live register.
2. Because the register is under ACTIVE EDIT by the concurrent P0.5 lane,
   re-run the import (`sync md-to-db`, idempotent upsert) at each natural
   P0.5 pause point and re-check `diff` until it reports clean.
3. Only when `diff` reports empty (modulo documented whitespace/section-order
   tolerance) does Phase 3 proceed.
4. Switch the regeneration path: future edits happen via `workable-items
   update`/`add`/`close`/`reopen`, then `workable-items export` regenerates
   `GAPS_AND_RISKS_REGISTER.md` (+ any Summary/HTML/PDF/DOCX siblings per
   §11.4.12/§11.4.53/§11.4.65).
5. WAL-checkpoint (`PRAGMA wal_checkpoint(TRUNCATE)`) + git-add + commit the
   `.db` file per §11.4.95 (never gitignored — it IS the source).
6. Wire `workable-items validate` into this project's pre-build gate (once
   one exists) per §11.4.93's mandate.

**Acceptance criteria:**
- `workable-items diff` reports empty (or only documented tolerance)
- `.db` file is git-tracked and committed
- `workable-items validate` wired as a pre-build gate
- Direct hand-edits to the register's item bodies are retired (narrative
  preamble/adjudication/resolved-non-findings sections may still need
  occasional hand-touch until Phase 4)

**Estimated effort:** ~2-4 hours (including the round-trip iteration cycles
against the moving P0.5 target)

**Sequencing:** MUST land at a natural P0.5 pause point where the register's
STATUS-block churn quiesces briefly. This is the "separate gated step" —
Phase 3 does not run until the round-trip diff can be checked against a
momentarily-stable target.

---

### Phase 4 — Switch generators to read from DB

**Goal:** Ensure all downstream generators (Issues_Summary, Fixed_Summary,
README doc-links, HTML/PDF/DOCX exports) read from the DB rather than parsing
markdown directly.

**Steps:**
1. Verify `workable-items export` produces correct Issues.md + Fixed.md +
   Summaries + HTML/PDF/DOCX siblings.
2. Switch any project-specific generator scripts to invoke `workable-items
   export` as their data source.
3. Retire any ad-hoc markdown-parsing generators that are now superseded.

**Acceptance criteria:**
- All generated docs (summaries, exports) are byte-identical whether generated
  from DB or from the previous markdown path
- No generator reads markdown item bodies directly (they read from the DB via
  the engine)

**Estimated effort:** ~1-2 hours

**Sequencing:** After Phase 3 cutover is proven stable.

---

### Phase 5 — Deprecate direct markdown edits

**Goal:** Enforce that all item-state changes go through the DB, not direct
markdown edits.

**Steps:**
1. Add a pre-commit hook or gate that detects direct edits to item bodies in
  `GAPS_AND_RISKS_REGISTER.md` and rejects them (suggests using
  `workable-items update` instead).
2. Document the new workflow in the project's contribution guide.
3. Close G40, G45, and G47 (if operator decision has been made) with cited
   evidence.

**Acceptance criteria:**
- Pre-commit gate rejects direct item-body edits
- New workflow documented
- G40/G45/G47 closed with captured evidence (the `validate`-clean run +
  the round-trip `diff` + the gate proof)

**Estimated effort:** ~1 hour

**Sequencing:** After Phase 4 is proven stable.

---

## 3. Acceptance Criteria Summary (§11.4.108 runtime-signature)

| Phase | Runtime-signature | Evidence |
|---|---|---|
| 0 | Binary builds + tests pass | `go build` exit 0, `go test` exit 0, schema version `'6'` |
| 1 | DB populated + validates | `workable-items validate` exit 0, 49 G-item rows, status distribution matches register |
| 2 | R-items imported + validates | `workable-items validate` exit 0, 73 total rows |
| 3 | Round-trip proven + cutover | `diff` empty, `.db` git-tracked, pre-build gate wired |
| 4 | Generators read from DB | Generated docs byte-identical from DB vs markdown path |
| 5 | Direct edits deprecated | Pre-commit gate rejects item-body edits, workflow documented |

---

## 4. Risk Mitigations

### Gap A — No `category` column in `schema_embed.sql`

**Risk:** The register's `**Category:**` field (inconsistency/security/gap/
existing-bug/danger-zone/test-coverage) has no matching DB column.
**Mitigation:** Fold into `items.description`'s structured header (e.g.
`Category: security` line) as a workaround. Recommend upstream additive
`ALTER TABLE items ADD COLUMN category TEXT` (low-risk, matches `severity`
column precedent).
**Blocking?** No.

### Gap B — Free-text STATUS narratives do not injectively map onto §11.4.15 closed set

**Risk:** Historical multi-attempt cycles (G01) can only be approximately
reconstructed into `item_history` rows — dates/actors for the ORIGINAL
narrative were not captured in §11.4.34 By/On/Reason/Evidence shape at
authoring time.
**Mitigation:** The raw narrative is preserved byte-identically via `body_md`
regardless of classification accuracy. Manual classification table authored
during Phase 1, reviewed in code-review per §11.4.125/§11.4.209. Going
forward, every new state change is captured with full §11.4.34 fields.
**Blocking?** No — one-time historical-backfill limitation only.

### Gap C — Category→Type and Rxx→Type mapping is a judgment call

**Risk:** Is "gap" a `Feature` or a `Task`? Per-item classification needed.
**Mitigation:** Defaults proposed in the design (§3.2/§3.3), reviewable,
overridable per item. Engineer classification pass during Phase 1 import,
captured as an explicit mapping table in the migration commit.
**Blocking?** No.

### Gap D — G47 Option (b) has no schema column today

**Risk:** If operator picks Option (b) (parallel ATM-NNN + alias), a new
`legacy_id` column is needed.
**Mitigation:** Option (a) (recommended) needs zero schema change. Only
pursue Gap D if operator picks (b).
**Blocking?** No — Option (a) is the default.

### Gap E — `schema.sql` vs `schema_embed.sql` drift (`canonical_track`/`group_paths` absent from runtime schema)

**Risk:** The runtime schema lacks the §11.4.191 multi-track fields.
**Mitigation:** This project's core adoption does not need multi-track fields
(single serialized lane today). Report to constitution-submodule maintainers
as its own finding; out of this project's fix scope.
**Blocking?** No.

### Gap F — `reopens_count`/`Reopened-Details` have no source data yet

**Risk:** No G-item has been formally "Reopened" under §11.4.34 vocabulary.
**Mitigation:** `reopens_count` starts at 0 for every imported item (accurate).
Future reopens are captured exactly per §11.4.34 going forward.
**Blocking?** No — honest as-is.

---

## 5. Operator Decisions Needed

### G47 — ID scheme: Option (a) vs Option (b)

**Status:** PENDING (§11.4.66)
**Decision required:** Should the project use literal `Gxx`/`Rxx` strings as
`atm_id` (Option a, recommended, zero schema change) or mint parallel
`ATM-NNN` identifiers with `Gxx` as a documented alias (Option b, requires
`legacy_id` column)?
**Impact:** Option (a) allows Phase 1 to proceed immediately. Option (b)
requires an upstream schema extension before import can begin.
**Recommendation:** Option (a) — zero engine changes, fully non-destructive,
preserves the literal string used in 20+ cross-referencing docs.

### R-item Type classification

**Status:** PENDING (engineer judgment during Phase 1)
**Decision required:** Per-R type override list for procedural/infrastructure
items that should be `Type=Task` rather than `Type=Feature`.
**Suggested overrides:**
- R9 "Submodule resolution + sync" → `Task`
- R10 "Docs Chain incorporation" → `Task`
- R23 "Constitution compliance audit" → `Task`
- R24 "Request-history document" → `Task`
- All others default to `Feature` (define capabilities the system must gain)

---

## 6. Estimated Effort

| Phase | Effort | Dependencies | Can run during P0.5? |
|---|---|---|---|
| 0 | ~30 min | None | Yes |
| 1 | ~2-3 hours | Phase 0, G47 decision (or proceed on Option a default) | Yes (§11.4.96 SAFE) |
| 2 | ~1-1.5 hours | Phase 1 | Yes (§11.4.96 SAFE) |
| 3 | ~2-4 hours | Phase 2 + P0.5 pause point | **No** — must land at a quiescent moment |
| 4 | ~1-2 hours | Phase 3 | Yes |
| 5 | ~1 hour | Phase 4 | Yes |
| **Total** | **~8-12 hours** | | |

Phase 3 is the only phase that requires coordination with the in-flight P0.5
Go-mutator lane. All other phases are safe-during-active-Go-work per §11.4.96.

---

## 7. Anti-bluff Boundary (§11.4.6)

**"Adopted" (the terminal state this plan aims at) means:**
- `docs/workable_items.db` exists, is git-tracked (§11.4.95), contains every
  G01–G49 + R01–R24 as a validated row with round-trip-proven `body_md`
- `workable-items validate` runs clean
- `export`/`sync db-to-md` are wired as the register's regeneration path
  (§11.4.12/§11.4.106)
- Phases 1–5 above complete and proven with captured evidence

**What remains until the migration ACTUALLY runs (this document does not
perform it):**
- No `.db` file exists yet
- No register line has moved
- G40/G45 stay `STATUS: FILED` until Phase 5 closes them with cited evidence
- G47 stays `STATUS: FILED; operator-decision PENDING` until the operator is
  asked which alias strategy to use

---

## Sources verified (§11.4.99)

No external web sources were consulted — this is a pure internal-codebase
plan derived from the design doc at
`research/g40_workable_items_db_adoption_design.md`. No operator-facing
installation instructions are produced, so §11.4.99's cross-reference mandate
does not apply.
