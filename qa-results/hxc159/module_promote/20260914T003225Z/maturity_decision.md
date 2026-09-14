# T-P3.01.4 — maturity-contradiction resolution (U-9 carry-over)

Contradiction as framed in tasks.md: path says `research/mvp`, content is
production-shaped (Dockerfile, systemd unit, 12 migrations, deploy compose).

## Decision: `relocate-and-reconcile` (U-9's verified verdict, adopted)

U-9's 2026-07-29 probe (measured in a `/tmp` clean room, never the shared
tree) INVERTED the framing: `research/mvp` is ACCURATE and the outlier is
one stale README line:

- Root `CONTINUATION.md` §1 (Rev 12, 2026-07-18): *"The MVP skill-graph
  system … is in **active development**"*.
- Research-area `CONTINUATION.md`: *"**PHASE:** P0.5 critical remediation
  (security + correctness) before feature phases"*.
- Path and governance docs AGREE. The sole contradicting text was the
  nested README's *"is a production-grade Go application"* (line 11) whose
  build badge pointed at `github.com/helixdevelopment/skill-system` — a URL
  that is not this repository.

So no rewrite, no re-maturation theatre: the module is an MVP in active
development and is treated as such. Reconciliation actions taken here:

1. Structural: module promoted to repo root (T-P3.01.2) — the research/mvp
   path no longer qualifies the shipped code; the research tree keeps only
   research (`research/`, `workable_items.db`, `helix-deps.yaml` until T-P3.02
   lifts it).
2. Textual: `service_readme.md` line 11 corrected to MVP/active-development
   wording; all badge/clone/install URLs repointed to the canonical
   `github.com/HelixDevelopment/skills` (part of T-P3.01.3).

This file + the README correction constitute the upstream-recorded decision
U-9 required (not inferred — measured and written into the repo).
