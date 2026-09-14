# T-P4.07 Coverage ledger — pkg/skills loader (§11.4.169, HXC-159)

Run: `20260914T033000Z` · Evidence root: `qa-results/hxc159/loader_matrix/20260914T033000Z/`
Host: linux/amd64, go1.26.0, user `milosvasic` (non-root). No blank cells:
every row resolves to `covered` with an evidence path, or an honest
`SKIP-with-reason` (§11.4.3). NFR budget: **cold enumeration of a
1200-skill tree ≤ 500ms** (measured mean ~96ms, ~5x margin; the 402ms
whole-tree baseline was input, not the budget).

| # | Test type (§11.4.169) | Test / gate | Verdict | Evidence path |
|---|---|---|---|---|
| 1 | unit | `go test ./pkg/skills/` (loader, registry, namespace, activation, capability, similarity) | covered | `loader_matrix/20260914T033000Z/suite.log` |
| 2 | integration | manifest→register→load→activate→use incl. union-count equality (`TestConsumerEndToEnd`, `TestUnionCountEquality`) | covered | `loader_matrix/20260914T033000Z/suite.log` + `rerun3.log` |
| 3 | e2e | external consumer package `skills_test` drives the full seam on real on-disk corpora | covered | `loader_matrix/20260914T033000Z/suite.log` (`TestConsumerEndToEnd` PASS) |
| 4 | full-automation | self-driving, re-runnable; determinism proven at `-count=3` | covered | `loader_matrix/20260914T033000Z/rerun3.log` (exit 0) |
| 5 | Challenges | 5× `CME-SKILLS-LOADER-*` rows registered; each dispatches to a passing loader test | covered | `test/challenges/CHALLENGE_README.md` + `loader_matrix/20260914T033000Z/suite.log` |
| 6 | HelixQA | 5× `CME-SKILLS-LOADER-*` bank entries, all 5 dispatches executed green | covered | `test/helixqa/skill_system.yaml` + `loader_matrix/20260914T033000Z/suite.log` |
| 7 | DDoS | `TestSustainedToolCallLoad`: 16 workers × 100 iters MCP tool-call-shaped read storm, zero errors, throughput logged | covered | `loader_matrix/20260914T033000Z/suite.log` (ops/s line) |
| 8 | security | undeclared shell/network refused at the call boundary + audited (`CM-SKILL-CAPABILITY-DECLARED` incl. always-allow mutation) | covered | `capabilities/20260914T030500Z/gate.log` |
| 9 | stress+chaos | deleted root fails closed; mid-flight deletion terminates with structural integrity; RLIMIT_FSIZE save refuses loudly + recovers; SIGKILL mid-write leaves absent-or-complete audit file | covered | `loader_matrix/20260914T033000Z/suite.log` (`TestChaos*` PASS) |
| 10 | concurrency/atomicity | 8 reader + 4 audit-writer goroutines × 50 iters over one registry; union count stable; audit trail lossless (800/800) | covered | `loader_matrix/20260914T033000Z/race_full.log` |
| 11 | race/deadlock | full suite under `-race` green; lock-neutralized mutation produces `WARNING: DATA RACE` (discrimination proof); flat single-RWMutex locking, 60s watchdog (no hang) | covered | `loader_matrix/20260914T033000Z/race_full.log` + `race_red.log` |
| 12 | memory | 1200-skill cold-load heap delta vs 8MiB ceiling; VmHWM vs 256MiB ceiling; 10-reload leak census within ratio bound | covered | `loader_matrix/20260914T033000Z/suite.log` (`TestMemoryCeilingAndLeakCensus` PASS + delta lines) |
| 13 | benchmarking | `BenchmarkColdEnumeration` mean ~96ms over 1200 skills; `TestColdEnumerationBudget` enforces 500ms (fails on breach) | covered | `loader_matrix/20260914T033000Z/bench.log` |

Related (own evidence dirs, referenced not duplicated): activation
(`activation/20260914T025032Z/`: RED compile-fail, metric Go==Python 14/14,
ceiling+1 refuse-and-name), capabilities (`capabilities/20260914T030500Z/`),
headless (`headless/20260914T031500Z/`: 99-dep closure, zero GUI).

Honest notes (§11.4.6): (a) this host HAS X11/GL headers — RS-16's weight
rests on the dependency-closure proof, not header absence; (b) the
similarity gate is NON-SEPARABLE with accepted fp risk (threshold.json) —
near-duplicate generated fixtures trip it by design (observed live in
`TestSustainedToolCallLoad` fixture design); (c) disk-full is proven via
RLIMIT_FSIZE error propagation + `/dev/full` ENOSPC environment proof,
not a filled disk.
