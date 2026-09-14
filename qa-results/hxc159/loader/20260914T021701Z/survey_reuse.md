# §11.4.74 reuse survey — T-P4.01 `pkg/skills` (HXC-159)

Date: 2026-09-14 · Run: 20260914T021701Z

## skill_registry (`submodules/skill_registry`, module `dev.helix.agent/skillregistry`)

Read (read-only, helix_code checkout): `types.go` (sentinel errors,
`Skill{ID,Name,Description,Version,…}` + executor scope), `registry.go`
(CLI-agent registry: OpenCode/Crush/HelixCode entries — unrelated surface),
`loader.go` (`Loader.LoadSkillFromFile`, exact-case `SKILL.md` directory
probing, imports `gopkg.in/yaml.v3`).

Verdict: **reuse shapes, not code**.
- Reused: sentinel-error style, exact-case `SKILL.md` probing, `Skill`
  core field names.
- Not reused: the `Loader` (yaml.v3 import violates R-25
  dependency-free), the `Skill` executor/storage/timeout scope
  (beyond a pure-data loader), the CLI-agent registry (different surface).
- Name reconciliation (U-12: four disagreeing module/repo names) untouched —
  out of scope for the loader; no dependency edge created in either direction.

## HelixAgent `internal/skills` (consumer framework, in place)

Read (read-only): `types.go:12` (`type Skill struct`),
`loader.go:29` (`NewSkillLoader`), `loader.go:44` (`LoadFromDirectory`),
`registry.go:49` (`NewRegistry`), `registry.go:125` (`RegisterSkill`).

Verdict: **dialect reference only**. `DialectHelixAgent` front-matter mapping
(`name`, `allowed-tools`, `version`, `license`, `author`, `category`, `tags`)
mirrors these shapes. Code unreachable across modules (`internal/` wall,
plan §4) — nothing imported, tree never copied (D-6, §11.4.122).

## skillconv (`scripts/skillconv/canonical.go`, own module)

Verdict: **shapes reused** (`CanonicalSkill` core + `x-helixcode` /
`x-helixagent` profiled namespaces; HelixCode regex triggers opaque).
Code not imported — pkg/skills must stay dependency-free (stdlib only),
and skillconv is a separate module.

## P2 calibration consumed as DATA (not recalibrated)

- Ceiling 20 (provisional, flat curve): `qa-results/hxc159/shadowing_curve/p2_curve1/ceiling.json`
- Similarity threshold 0.0256 Jaccard, NON-SEPARABLE, fp_risk_accepted:
  `qa-results/hxc159/shadowing_curve/p2_curve1/threshold.json`
- Recorded in `pkg/skills/doc.go`; wired in T-P4.04, not here.

## Measured union shape today (read-only probes, 2026-09-14)

- constitution/skills: **8** exact-case SKILL.md (spec §8 says 7 — drift noted, not forced)
- HelixAgent tree: **1315** SKILL.md (includes nested third-party MCP trees; plan-time figure was 1174)
- Upstream root: **0** SKILL.md
- Standing tests assert union-count equality generically over fixtures;
  these counts are evidence, not test constants.
