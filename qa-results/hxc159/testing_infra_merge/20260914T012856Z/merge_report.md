# T-P3.04 — Merge `origin/feature/testing-infra` into `main` (HXC-159)

- run-id: `20260914T012856Z`
- date (UTC): 2026-09-14
- operator task: Spec 001-helixskills-incorporation, T-P3.04
- clone: `/tmp/skills_upstream` (UPSTREAM clone; main pre-merge at `22e1cac`, pushed)
- policy: merge only, NEVER rebase; no push performed in this task.

## Ahead-count verification (vs new main `22e1cac`)

- `git rev-list --count main..origin/feature/testing-infra` → **6** (matches task brief).
- `git rev-list --count origin/feature/testing-infra..main` → **25** (main moved on:
  T-P3.01 Go-module-to-root promotion, T-P3.02 helix-deps.yaml/CONSTITUTION.md,
  T-P3.03 reconciliations).
- merge-base: `dcbe504` (SECURITY: harden MVP project configs).

## Merge

- Command: `git merge --no-ff --no-commit origin/feature/testing-infra`
- Result: 2 content conflicts + 4 file-location conflicts + 2 clean auto-merges.

### Conflict summary (all resolved preserving both sides)

1. `CONTINUATION.md` (content, both-modified) — kept HEAD Rev 12 header + HEAD
   session list; appended feature's T3-RESTART entries as one provenance line
   (they document exactly the testing-infra work being merged). No marker residue.
2. `docs/research/mvp/Agent_AI_Skill_Tree_Development/GAPS_AND_RISKS_REGISTER.md`
   (content, both-modified) — kept HEAD Rev 12 table (157 items, superset);
   feature's Rev 10 table (136 items) fully superseded, dropped. No marker residue.
3. 4× file-location conflicts — git detected the T-P3.01 root promotion rename:
   `.../project/internal/<pkg>/stress_chaos_fuzz_test.go` (feature) →
   `internal/<pkg>/stress_chaos_fuzz_test.go` (HEAD layout). Accepted at the new
   root paths (1 361 lines total: 490 + 336 + 296 + 239).
4. Import-path adaptation (part of the same resolution, not a rebase): the suites
   imported `github.com/helixdevelopment/skill-system/internal/...` (old nested
   module); rewritten to `github.com/HelixDevelopment/skills/internal/...`
   (files: `internal/codegraph`, `internal/source/dedup`). Verified against repo
   convention (`internal/worker/*_unit_test.go`) + `gofmt`.
5. `test/helixqa/skill_system.yaml` — clean auto-merge, 91 → **119 entries (+28)**.
6. `.../project/test/challenges/CHALLENGE_README.md` — auto-added at stale nested
   path; relocated via `git mv` to `test/challenges/CHALLENGE_README.md`
   (new root layout; `test/challenges/` did not exist on main).

## Suite results (runtime signature: 4 suites run + report from the merge commit)

`go test -count=1` per package, `go1.26.0 linux/amd64`, fresh (no cache):

| Suite (file) | Lines | Result |
|---|---|---|
| `internal/codegraph` (`stress_chaos_fuzz_test.go`) | 490 | **PASS** — exit 0, 42 top-level PASS, 0 FAIL |
| `internal/models` (`stress_chaos_fuzz_test.go`) | 336 | **PASS** — exit 0 (final log; see fuzz note), 16 top-level PASS, 0 FAIL |
| `internal/skillsource` (`stress_chaos_fuzz_test.go`) | 296 | **PASS** — exit 0, 35 top-level PASS, 0 FAIL |
| `internal/source/dedup` (`stress_chaos_fuzz_test.go`) | 239 | **PASS** — exit 0, 17 top-level PASS, 0 FAIL |

`go build` + `go vet` on all four packages: clean. `gofmt -l internal/`: clean.
Full logs: `suite_<pkg>.log` (first merge-tree run), `suite_<pkg>_final.log`
(post-fix re-run). Per-suite summary: `suite_summary.txt`.

### Out-of-scope honest SKIP (pre-existing, DB-backed, NOT part of the 4 suites)

- 7× `TestStore_*` in `internal/skillsource/store_test.go` SKIP with reason
  `SKILL_SYSTEM_TEST_DB_HOST not set` (live-pgvector-gated, §11.4.3-style).
  Postgres probe: TCP `127.0.0.1:5544` **reachable**, `vector` extension present,
  but database `hxc159test` does **not** exist and TCP password auth is required
  (password unknown; changing it would mutate shared container state, out of
  scope). No results faked — recorded as environmental SKIP for DB-backed
  tests only. The 4 T-P3.04 suites are DB-free (zero `sql/postgres` references).

## Fuzz validation (bounded, 15–20 s per target, 7 targets)

| Target | Result |
|---|---|
| FuzzEvidenceCache (codegraph) | PASS, exit 0 |
| FuzzIndexProjectPath (codegraph) | PASS, exit 0 |
| FuzzSymbolToEvidence (codegraph) | PASS, exit 0 |
| FuzzSkillKindNormalize (models) | PASS, exit 0 |
| FuzzSkillJSON (models) | **FAIL → root-caused → FIXED → PASS** (see below) |
| FuzzValidate (skillsource) | PASS, exit 0 |
| FuzzClassify (dedup) | PASS, exit 0 |

**FuzzSkillJSON finding (real, not flaked):** fuzzer produced `Name:"\xff"`;
`json.Marshal` substitutes U+FFFD for invalid UTF-8 (documented Go stdlib
behavior), so unmarshal yields `"�" != "\xff"` and the exact-equality oracle
failed. The oracle's domain (JSON round-trip) is valid-UTF-8 strings only.
Fix (in-merge, minimal): skip non-UTF-8 fuzz inputs with `unicode/utf8.ValidString`
+ comment citing stdlib behavior; assertions for valid inputs untouched (no
weakening). Re-run: 20 s, ~900K execs, **PASS**, exit 0. Regression corpus
`internal/models/testdata/fuzz/FuzzSkillJSON/789c0e11c842946e` retained
(now exercises the Skip path). Logs: `fuzz_FuzzSkillJSON.log` (FAIL),
`fuzz_FuzzSkillJSON_rerun.log` (PASS).

## HelixQA bank registration

- `test/helixqa/skill_system.yaml`: 91 → **119 entries (+28)** via merge.
- New IDs (full list: `helixqa_new_ids.txt`): CME-CODEGRAPH-001…008,
  CME-DEDUP-001…005, CME-SKILLSOURCE-001…004, CME-MODELS-001…004,
  CME-FUZZ-EVIDENCE, CME-FUZZ-VALIDATE, CME-FUZZ-SKILLKIND, CME-FUZZ-SKILLJSON,
  CME-FUZZ-CLASSIFY, CME-STRESS-FULL, CME-CHAOS-FULL (28 total, `dispatches_to`
  entries present, 119 total in file).
- Challenges README registered at `test/challenges/CHALLENGE_README.md`.

## Files changed by this merge (beyond auto-merge)

- `internal/codegraph/stress_chaos_fuzz_test.go` (new, import-path fix + gofmt)
- `internal/models/stress_chaos_fuzz_test.go` (new, UTF-8 oracle-scope fix)
- `internal/skillsource/stress_chaos_fuzz_test.go` (new)
- `internal/source/dedup/stress_chaos_fuzz_test.go` (new, gofmt)
- `internal/models/testdata/fuzz/FuzzSkillJSON/789c0e11c842946e` (fuzz corpus)
- `test/challenges/CHALLENGE_README.md` (relocated)
- `test/helixqa/skill_system.yaml` (+28)
- `CONTINUATION.md`, `GAPS_AND_RISKS_REGISTER.md` (conflict resolutions)
- `qa-results/hxc159/testing_infra_merge/20260914T012856Z/` (this evidence)
