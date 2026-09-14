# T-P3.03.4 — `feature/deep-research` reconciliation note

Branch: `origin/feature/deep-research` @ `8c78272c` (1 commit ahead of main).

Commit: "feat(enterprise): extract source routes, add sync config +
enhancement proposals migration (G69/G72/G81/G84)".

- Extracts inline source route handlers from
  `.../project/cmd/server/main.go` into `.../project/cmd/server/source_routes.go`
  (named handlers, G84; DELETE now 204).
- Wires the source sync orchestrator (`NewOrchestrator`) into `buildRouter`
  — sync endpoint drives the real fetch→parse→map→dedup→import pipeline.
- Adds `SourceSyncConfig` (`Enabled`, `IntervalMinutes`, `MaxConcurrentSyncs`,
  `LicenseAllowlist`, `GitHubTokenEnv`) with defaults (G69/G72).
- Adds migration `007_enhancement_proposals` (`skill_enhancement_proposals`
  table, pending→accepted/rejected→applied lifecycle, G81).

No deletions, no gitlink changes (`git diff --stat` shows 5 files, all under
the nested project tree). Commit message claims all 24 packages pass
build+vet+test — re-verified post-merge in T-P3.01 (`go build ./...` from
root after promotion; pre-promotion spot check below in merge log).

Merge: plain `git merge --no-ff` (never rebase), preserving both sides.
`feature/testing-infra` (6 ahead) is OUT OF SCOPE — T-P3.04, explicitly
not started by this work order.
