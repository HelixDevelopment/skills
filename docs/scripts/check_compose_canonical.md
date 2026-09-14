# check_compose_canonical.sh

**Revision:** 1
**Last modified:** 2026-07-16T00:00:00Z

## Overview

`scripts/check_compose_canonical.sh` is a deterministic gate that asserts the
G13 **canonical-compose** runtime signature: there is exactly ONE compose file
in the project (`deploy/docker-compose.yml`), and no ops script, systemd unit,
or Makefile target still references the retired rival root compose.

**Why this exists.** Register item **G13** (HIGH) flagged that the project
shipped two rival compose files — a root `project/docker-compose.yml` and
`project/deploy/docker-compose.yml` — with divergent scope (service names,
subnet, container names, the presence of the app/worker/monitoring services)
and divergent script references. Rival compose copies drift and scripts pin the
wrong one — the exact divergence Constitution §11.4.186 (anti-divergence)
forbids, and `IMPLEMENTATION_PLAN.md` P12.T4 requires exactly one canonical
compose. The design decision (`research/ops_hardening_design.md` §G13) is to
canonicalize on `deploy/docker-compose.yml`, folding the app/worker/monitoring
services forward as opt-in compose **profiles** (preserved, never silently
dropped — §11.4.122 / §11.4.124), and to point every script + the systemd unit
+ the Makefile at that single file with its canonical `postgres` datastore
service.

This gate is the permanent, mechanical, self-validating guard (per §11.4.201 —
every guard/gate MUST assert the REAL condition and print its resolved evidence)
that keeps the single-canonical-compose invariant from regressing. It is the
G13 §11.4.115 RED→GREEN guard and the §11.4.135 standing regression test.

## Prerequisites

- `bash` >= 4.
- Standard coreutils: `find`, `grep`, `sed`, `mktemp`, `cp`, `rm`.
- A rootless compose engine (`podman compose` / `podman-compose` /
  `docker compose`) is used **only** for Check C (parse-validate the canonical
  file). Its absence SKIPs Check C with an honest reason (§11.4.3) — it never
  fails the gate and is never faked into a PASS.

## Usage examples

Run the full gate (the common case; run from anywhere — the script resolves its
own directory):

```bash
project/scripts/check_compose_canonical.sh
```

Expected output on the canonical tree ends with:

```
OVERALL: PASS (G13 canonical-compose runtime signature satisfied).
```

Run the paired §1.1 mutation self-test (proves the gate is load-bearing):

```bash
project/scripts/check_compose_canonical.sh --selftest
```

Check an alternate tree (e.g. an unpacked release):

```bash
project/scripts/check_compose_canonical.sh --project-root /path/to/tree
```

Exit code `0` on PASS; non-zero on any real FAIL.

## What it checks

- **Check A — single canonical compose.** Exactly one `docker-compose*.yml`
  file is present in the working tree under the project root (excluding
  `dist/`, `build/`, `.git/`, `node_modules/`), and it is
  `deploy/docker-compose.yml`. This is working-tree based (the real checked-out
  state, §11.4.108), so a root file removed from the checkout counts as gone
  regardless of git index staging.
- **Check B — no retired reference.** No scanned file references the retired
  root compose. Two sub-checks:
  - **B1** an explicit non-canonical compose FILE path — a
    `$(PROJECT_DIR|INSTALL_DIR)/docker-compose.yml` without a `/deploy/`
    segment, or a `-f <path>docker-compose.yml` whose path is not `deploy/`
    (e.g. the bare Makefile `-f docker-compose.yml`). The canonical
    `deploy/docker-compose.yml` and the `$COMPOSE_FILE` / `$(COMPOSE_FILE)`
    variables that resolve to it are allowed.
  - **B2** a compose invocation (`$COMPOSE_CMD` / `$(COMPOSE_CMD)`) targeting a
    RETIRED service name `db`/`api` — the hidden-reference class (§11.4.124):
    scripts that reached the root file via cwd-discovery + a `db`/`api` service
    name never spelled the filename. The canonical file's datastore service is
    `postgres` and its app service is `app`.
  - Scanned files: `scripts/*.sh` (minus the `check_*`/`test_*` gates, which
    carry the pattern strings by construction), `deploy/systemd/*.service`, and
    the project `Makefile`.
- **Check C — canonical parses.** The canonical compose (all profiles) parses
  under a rootless compose engine; SKIP-with-reason when no engine is present.

**Honest boundary (§11.4.6).** B1+B2 catch the concrete retired-reference
classes (explicit non-canonical path + retired service name). A brand-new bare
`compose up` with NO `-f` relying on cwd-discovery is out of this gate's scope —
every current invocation carries an explicit `-f`, and re-introducing a bare
non-canonical `-f`/service name is what B1/B2 catch.

## Edge cases

- **No compose engine on PATH** → Check C prints `SKIP:` with the reason and
  the gate still passes on A+B. A missing engine is never a fake PASS.
- **A root compose re-appears on disk** (regression) → Check A FAILs, naming
  both files.
- **A dangling reference re-introduced** into a script/Makefile → Check B FAILs,
  naming the exact `file: Bn LINE: text`.
- The `--selftest` mode operates entirely on a throwaway `mktemp -d` scratch
  copy; it READS but NEVER WRITES any real tracked file, and trap-cleans the
  scratch dir on every exit path (§11.4.14).

## Internal behavior

Plain mode runs Checks A, B, C in order and prints `EVIDENCE:` +
`PASS:`/`FAIL:`/`SKIP:` lines for each before the final `OVERALL:` verdict.
`--selftest` asserts (1) the reference check PASSes on the real tree and (2) it
FAILs on a scratch copy with a re-introduced dangling
`cp "$PROJECT_DIR/docker-compose.yml" ...` reference — so the gate is proven
load-bearing, not a bluff gate that would pass regardless of tree content.

## Related scripts

- `../deploy/docker-compose.yml` — the canonical file this gate asserts on.
- `../deploy/systemd/helix-skills.service` — the systemd unit (already
  canonical via `_lib.sh`).
- `scripts/_lib.sh` — resolves `HX_COMPOSE_FILE=${HX_DEPLOY_DIR}/docker-compose.yml`
  (canonical) and `exec -T postgres`; the shared helper for
  start/stop/restart/status/logs.
- `scripts/migrate.sh`, `backup.sh`, `restore.sh`, `package.sh` — the non-`_lib`
  ops scripts pointed at the canonical file + `postgres`/`app` services by G13.
- `../Makefile` — its compose targets route through `-f $(COMPOSE_FILE)`.
- `scripts/check_container_runtime_default.sh` — sibling ops gate (rootless
  runtime default, G39).

## References

- `research/ops_hardening_design.md` §G13 (design + runtime signature).
- `GAPS_AND_RISKS_REGISTER.md` G13.
- Constitution §11.4.108 (runtime signature), §11.4.122 (no silent removal),
  §11.4.124 (hidden references), §11.4.161 (rootless runtime), §11.4.186
  (anti-divergence), §11.4.201 (guard asserts real condition + prints evidence).

Last verified: 2026-07-16
