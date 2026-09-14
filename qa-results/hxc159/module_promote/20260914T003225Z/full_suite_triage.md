# Full-suite triage (T-P3.01, run 20260914T003225Z)

`go test ./...` from the promoted root: 25 packages ok, 2 packages with
failures. Full log: `go_test_all.txt`.

## 1. `internal/skillsource` TestStore_* — ENVIRONMENTAL, resolved

Symptom: `register pgvector types: vector type not found in the database`.
Cause: the throwaway Postgres lacked `CREATE EXTENSION vector` in the shared
`postgres` DB the skillsource helper connects to (it creates only its own
table, unlike the skill package helper which runs full migrations).
Proof: identical failure on the pre-promotion HEAD worktree
(`/tmp/skills_head_check`, old module path); after
`CREATE EXTENSION vector` on the throwaway DB, the package is fully green
both at HEAD (`skillsource_env_note.txt`: ok) and on the promoted tree.
No code change. Follow-up for the conductor: none required (test-env setup
step, documented here).

## 2. `internal/autoexpand` TestDraftSkill_ResourcesPersisted — PRE-EXISTING defect, out of scope

Symptom: `resources_resource_type_check` violation (SQLSTATE 23514).
Root cause: the G20 fixture's LLM-drafted payload uses
`resource_type: "documentation"`, but migration 001 allows only
`('official-doc', 'article', 'code', 'video', 'tutorial')`.
Proof: identical FAIL on the pre-promotion HEAD worktree
(`autoexpand_head_repro.txt`). The pipeline performs no normalization of
LLM-emitted resource types — a real draft-persist defect, but NOT part of
T-P3.01 (promotion + A6). Fixing it means a behavior decision (normalize vs
reject) that belongs to a tracked follow-up, not this promotion.
T-P3.01 acceptance (`go build ./...` green from root, module consumable,
A6 wired with RED→GREEN) is unaffected.

## 3. `scripts/test_guard_forbidden_commands.sh` — ENVIRONMENTAL, pre-existing

Symptom: `FAIL: guard script not found at: .../constitution/scripts/hooks/guard-forbidden-commands.sh (0 cases run)`.
Cause: the `constitution` submodule is not initialized in this clone
(empty directory) — identical failure on the pre-promotion HEAD worktree.
No code change; the suite cannot run until the submodule is initialized
(conductor-side `git submodule update --init constitution`).
