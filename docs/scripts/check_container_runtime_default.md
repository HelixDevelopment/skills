# check_container_runtime_default.sh

**Revision:** 2
**Last modified:** 2026-07-16T00:00:00Z

## Overview

`scripts/check_container_runtime_default.sh` is a deterministic gate that
asserts the project `Makefile`'s `CONTAINER_RUNTIME ?= ...` default is
exactly `podman`, never `docker`.

**Why this exists.** Constitution §11.4.161 (Rootless container runtime
mandate) requires every project to use rootless Podman — or an equivalent
rootless runtime — as the **default** for all containerized workloads.
Rootful Docker, or any use of `sudo` to run a container engine, is
forbidden unless the target platform genuinely has no rootless option, and
that constraint must then be explicitly documented per §11.4.112. This
project's platform (Linux host with rootless Podman available) has no such
constraint, so the plain rule applies: the default MUST be `podman`.

Register item **G39** (HIGH, tracked against §11.4.161 / §11.4.76 /
§11.4.173) flagged that `project/Makefile` shipped
`CONTAINER_RUNTIME ?= docker` — a rootful-docker default — as a defect.
This script is the permanent, mechanical, self-validating gate (per
Constitution §11.4.201 — every guard/gate MUST assert the REAL condition
and print its resolved evidence on every refusal) that prevents that
regression from silently landing again.

## Prerequisites

- `bash` >= 4.
- Standard coreutils: `grep`, `sed`, `mktemp`, `cp`, `rm`.
- **No container engine is required to run this gate.** It only parses the
  Makefile's text; it never invokes `podman`, `docker`, or any compose
  subcommand. (A working rootless Podman install is naturally still
  required to actually *use* the project's container targets — see
  `Makefile` and `deploy/docker-compose.yml` — but is orthogonal to this
  gate.)

## Usage examples

Check the real project Makefile (the common case; run from anywhere, the
script resolves its own directory):

```bash
project/scripts/check_container_runtime_default.sh
```

Expected output on a compliant Makefile:

```
EVIDENCE: /path/to/project/scripts/../Makefile -> CONTAINER_RUNTIME default = 'podman'
PASS: CONTAINER_RUNTIME defaults to 'podman' (rootless-first, Constitution Sec 11.4.161).
```

Exit code `0` on PASS.

Check an arbitrary Makefile (e.g. a scratch copy, or a different project
layout):

```bash
project/scripts/check_container_runtime_default.sh --makefile /path/to/other/Makefile
```

Run the paired §1.1 mutation self-test (proves the gate is genuinely
load-bearing — it PASSes the real Makefile and FAILs a throwaway
docker-mutant copy it creates, checks, and deletes; the real `Makefile` on
disk is never written):

```bash
project/scripts/check_container_runtime_default.sh --selftest
```

Expected output (abridged):

```
== Sec 1.1 paired mutation test: check_container_runtime_default.sh ==

-- assertion 1: gate PASSes on the real project Makefile --
...
assertion 1: PASS (gate correctly passes the fixed Makefile)

-- assertion 2: gate FAILs on a throwaway docker-mutant scratch copy --
...
assertion 2: PASS (gate correctly FAILs the docker-mutant)

OVERALL SELFTEST: PASS (gate is genuinely load-bearing - PASS on real Makefile, FAIL on docker-mutant)
```

Show help:

```bash
project/scripts/check_container_runtime_default.sh -h
```

## Edge cases

- **Makefile missing.** `extract_default` returns failure; the script
  prints `EVIDENCE: no 'CONTAINER_RUNTIME ?=' line found in: <path>` and
  `FAIL: cannot resolve CONTAINER_RUNTIME default ...`, exit code `1`. It
  never guesses a default (Constitution §11.4.6) — an unresolvable
  condition is reported honestly as FAIL, never silently treated as PASS.
- **`CONTAINER_RUNTIME ?=` line absent** (e.g. someone renames the
  variable or removes the `?=` default entirely). Same FAIL-with-evidence
  path as above — the gate does not fall back to inspecting `COMPOSE_CMD`
  or any other variable; it asserts specifically on the declared default
  assignment.
- **Default set to anything other than `podman`** (`docker`, a typo, an
  empty string after `?=`, etc.). Reported as FAIL with the exact resolved
  value quoted in the evidence line, never a vague "wrong value" message.
- **`--selftest` cannot apply its mutation** (e.g. the real Makefile's
  `CONTAINER_RUNTIME` line has drifted to a shape the mutation regex no
  longer matches). The self-test treats this as an inconclusive-therefore-
  FAILED assertion 2 (per §11.4.6 — an untested condition is never silently
  assumed safe) rather than silently reporting a false PASS.
- **Concurrent invocations.** `--selftest` uses a private `mktemp -d`
  scratch directory per invocation and only ever touches its own scratch
  copy, never the real `Makefile`, so concurrent runs do not race each
  other or corrupt shared state.

## Internal behavior

1. `extract_default MAKEFILE_PATH` — greps the file for the first line
   matching `^CONTAINER_RUNTIME[[:space:]]*\?=`, then strips the
   `CONTAINER_RUNTIME ?=` prefix and any trailing whitespace with `sed` to
   yield the bare default token (e.g. `podman`). Returns failure (nothing
   printed) if the file is missing or no such line exists.
2. `check_makefile MAKEFILE_PATH` — calls `extract_default`, always prints
   an `EVIDENCE:` line first (the resolved file + value, or the concrete
   absence), then prints `PASS:`/`FAIL:` and returns the matching exit
   code. This ordering — evidence always before verdict, on every path —
   is the §11.4.201 requirement that a guard's resolved evidence is
   visible on every refusal, not just failures.
3. Plain mode (`mode="check"`, the default) calls `check_makefile` once
   against either the default project Makefile
   (`<script-dir>/../Makefile`) or a path supplied via `--makefile`, and
   the script's own exit code is that call's exit code.
4. `--selftest` mode (`run_selftest`) is the paired Constitution §1.1
   mutation test:
   - Creates a private scratch directory via `mktemp -d`, registered with
     a `trap ... EXIT` cleanup so it is removed on every exit path
     (success, failure, or interrupt) per §11.4.14.
   - **Assertion 1**: calls `check_makefile` against the real project
     Makefile and asserts it PASSes.
   - **Assertion 2**: `cp`'s the real Makefile into the scratch directory,
     then `sed -i` mutates *only that scratch copy's*
     `CONTAINER_RUNTIME ?= podman` line to `CONTAINER_RUNTIME ?= docker`.
     It verifies the mutation actually applied (defensive — if the pattern
     didn't match, the assertion is marked FAILED/inconclusive rather than
     silently skipped), then calls `check_makefile` against the mutant and
     asserts it FAILs.
   - Exits `0` only if both assertions hold.
5. The real `project/Makefile` is opened read-only in every code path;
   `--selftest`'s only write is to its own throwaway scratch file, which is
   removed by the exit trap.

## Related scripts

- `../../Makefile` — the file this gate checks (`CONTAINER_RUNTIME ?=`
  line, and every target built on top of `$(CONTAINER_RUNTIME)` /
  `$(COMPOSE_CMD)`: `dev`, `dev-down`, `db-reset`, `docker-build`,
  `docker-push`, `docker-up`, `docker-down`, `docker-logs`, `docker-ps`,
  `clean-all`).
- `../scripts/_lib.sh` — the shared shell library used by the project's
  other lifecycle scripts (`start.sh`, `stop.sh`, `status.sh`, etc.); it
  independently probes for `docker compose` / `podman compose` /
  `podman-compose` at runtime (engine auto-detection for those scripts,
  distinct from this gate's Makefile-default assertion). `_lib.sh` and its
  callers are out of this script's scope and were not modified as part of
  landing this gate — see the "Known related finding" note below.
- `../deploy/docker-compose.yml` — the single canonical compose stack (G13)
  that `$(COMPOSE_CMD)` (i.e. `$(CONTAINER_RUNTIME) compose`) drives via
  `-f $(COMPOSE_FILE)`; verified CLI-compatible with Podman via
  `podman compose` (which shells out to the installed `podman-compose`
  provider) on the host this gate was authored and run on.

## Known related finding (not fixed by this script — out of scope)

While verifying no Makefile target breaks under the new `podman` default,
it was also observed that `scripts/_lib.sh` (an existing file, out of this
task's create-new-files-only scope for `project/scripts/`) auto-detects the
compose engine in **Docker-first** order (`docker compose`, then
`podman compose`, then standalone `podman-compose`) rather than
Podman-first. This does not contradict `Makefile`'s `CONTAINER_RUNTIME ?=
podman` default (a different file, a different mechanism — `_lib.sh`'s
callers auto-detect rather than reading `CONTAINER_RUNTIME`), but it is a
related §11.4.161 rootless-first-preference gap worth a follow-up register
item for whoever owns `project/scripts/_lib.sh` and its callers
(`start.sh`, `stop.sh`, `restart.sh`, `status.sh`, `logs.sh`, `backup.sh`,
`restore.sh`, `migrate.sh`, `package.sh`).

## Last verified

2026-07-16 — captured evidence: `podman --version` → `podman version
5.7.1`; `podman info --format '{{.Host.Security.Rootless}}'` → `true`; gate
run against the real (fixed) `project/Makefile` → `PASS`; `--selftest` run
→ `OVERALL SELFTEST: PASS` (both paired-mutation assertions held); the real
`Makefile`'s `CONTAINER_RUNTIME ?= podman` line confirmed unchanged after
the self-test completed.
