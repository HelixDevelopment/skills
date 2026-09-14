# multitrack

> **GENERATED FILE — DO NOT HAND-EDIT.** Regenerated from the live skill
> graph by the `skills-catalog` generator. Edit the skill via CLI/REST/MCP
> (see `docs/scripts/` / `docs/API.md`) — this file will be overwritten.

<!-- skills-catalog:section=header -->
## Header

- **Name:** multitrack
- **Title:** Multi-Track Parallel-Development Orchestration
- **Version:** 0.1.0
- **Kind:** atomic
- **Status:** active
- **Domain:** agent-infrastructure
- **Complexity:** advanced
- **Tags:** multitrack, parallel, orchestration, worktree, concurrency

<!-- /skills-catalog:section=header -->
<!-- skills-catalog:section=description -->
## Description

Multi-track parallel-development orchestration (§11.4.176, §11.4.187,
§11.4.192). Wires multitrack config + cwd-hook. Enables multiple agents
to work on independent tracks simultaneously with proper isolation via
git worktrees and flowing-pool claim registry.

<!-- /skills-catalog:section=description -->
<!-- skills-catalog:section=dependencies -->
## Dependencies

### Requires

- `session_orchestrator` — for flowing-pool claim registry

### Optional

- `continuum` — for cross-track state persistence

<!-- /skills-catalog:section=dependencies -->
<!-- skills-catalog:section=resources -->
## Resources

| Resource | Type | Description |
|---|---|---|
| `constitution/skills/multitrack/` | Directory | Skill source |

<!-- /skills-catalog:section=resources -->
<!-- skills-catalog:section=metadata -->
## Metadata

- **Source:** `constitution/skills/multitrack/`
- **Consumed via:** Constitution skill (installed via register.sh)
- **Constitution references:** §11.4.176, §11.4.187, §11.4.192
- **Created:** 2026-07-15
- **Last updated:** 2026-07-17

<!-- /skills-catalog:section=metadata -->

## Usage

### Enabling multi-track mode

Multi-track is activated when the operator directs parallel work on independent tracks. Each track gets its own git worktree and branch, isolated from other tracks.

```
# Operator directive to start multi-track work
BACKGROUND :: set up track A for the media-validator refactor and track B for the session-sync rewrite

# The agent creates isolated worktrees:
#   .claude/worktrees/track-a-media-validator/
#   .claude/worktrees/track-b-session-sync/
```

### Track claim registry

The flowing-pool claim registry (managed by `session_orchestrator`) prevents two agents from working on the same track simultaneously. An agent claims a track, works on it, and releases it when done or blocked.

```bash
# Claim a track (done automatically by the orchestration layer)
# Release a track (done automatically on completion or blockage)
```

### Configuration

Multi-track configuration lives in the project's multitrack config file. Each track declares its name, branch, worktree path, and scope (which files/domains it owns).

## Constitution References

| Reference | Meaning |
|---|---|
| **§11.4.176** | Multi-track work-division. Defines how work is partitioned into independent tracks, each with its own scope, branch, and worktree. Tracks are isolated — changes on one track do not affect others until merge. |
| **§11.4.187** | Multi-track ruler orchestration. The conductor/ ruler manages track lifecycle: creation, claiming, releasing, and merging. Defines the flowing-pool claim registry that prevents double-claiming. |
| **§11.4.192** | Multi-track merge discipline. Defines when and how tracks merge back: merge-after-live-QA, flavor/product non-merge, and trunk-merged-into-every-stream. |
| **§11.4.58** | Parallel-development methodology. The foundational methodology that multitrack implements — multiple agents working simultaneously with proper isolation. |
| **§11.4.94** | Zero-idle priority-first parallel-by-default operating mode. The autonomous loop keeps all tracks busy; idle time on one track means work on another. |
| **§11.4.84** | Working-tree quiescence rule for subagent commits. A subagent MUST NOT commit while another subagent's build is writing to the same tracked artifacts. |

## Cross-links

- **Requires:** `session_orchestrator` (flowing-pool claim registry)
- **Optional:** [`session-sync`](session-sync.md) (cross-track state persistence via continuum)
- **Related skills:** [`session-sync`](session-sync.md) (bridges session state between workstations/tracks)
- **Parent domain:** [`agent-infrastructure`](../by-domain/agent-infrastructure.md)
- **Constitution source:** [`constitution/skills/multitrack/`](../../../constitution/skills/multitrack/)

## Integration

| Surface | How it hooks in |
|---|---|
| **Git worktrees** | Each track gets an isolated worktree under `.claude/worktrees/`. Changes on one track are invisible to others until merge. |
| **Flowing-pool claim registry** | Managed by `session_orchestrator`. Prevents two agents from claiming the same track. Claims are atomic and time-bounded. |
| **CWD hook** | The multitrack cwd-hook ensures the agent's working directory is set to the correct worktree for the claimed track. |
| **Continuum** | Optional cross-track state persistence. Each track's state is content-addressed and can be resumed independently (§11.4.207). |
| **Merge discipline** | Tracks merge back per §11.4.192: merge-after-live-QA, flavor/product branches do not merge with each other, trunk is merged into every active stream. |
