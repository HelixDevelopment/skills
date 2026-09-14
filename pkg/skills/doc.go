// Package skills is the pure-data skill loader (HXC-159 T-P4.01).
//
// It enumerates Agent-Skills-shaped corpora (exact-case SKILL.md, YAML
// front-matter, name matches directory) into the canonical Skill model and
// registers typed sources into one fail-closed Registry.
//
// Dependency-free by construction (R-25): this package imports ONLY the Go
// standard library. No third-party module may ever appear in its import
// graph — enforced by gates/cm-consumer-version-parity.sh, which fails the
// build if `go list -deps ./pkg/skills` yields anything outside stdlib.
// That structural absence (not a version pin) is what makes the
// replace-outside-main-module problem impossible for both consumers.
//
// Canonical model (spec §8, D-1): Agent Skills open standard core
// {name, description, version, body}; HelixCode's regex trigger model and
// HelixAgent's allowance/metadata model ride as profiled x- namespaces,
// mirroring scripts/skillconv (helix_code checkout, own module — shapes
// reused per §11.4.74, code NOT imported: skillconv lives in another module
// and pkg/skills must stay dependency-free).
//
// §11.4.74 reuse survey (2026-09-14, recorded in qa-results/hxc159/loader):
//   - submodules/skill_registry (dev.helix.agent/skillregistry): REUSE SHAPES
//     ONLY (sentinel-error style, exact-case SKILL.md directory probing).
//     Code not reused: its Loader imports gopkg.in/yaml.v3 (violates R-25
//     dependency-free) and its Skill carries executor/storage/timeout scope
//     far beyond a pure-data loader.
//   - submodules/helix_agent/internal/skills (types.go:12 Skill,
//     loader.go:44 LoadFromDirectory, registry.go:49 NewRegistry): dialect
//     reference for DialectHelixAgent front-matter mapping; code unreachable
//     across modules (internal/ wall, plan §4) so shapes only.
//
// Calibration data consumed as DATA (P2, do not recalibrate):
//   - Active-count ceiling: 20 (provisional, flat curve) — wired in T-P4.04.
//   - Description-similarity threshold: 0.0256 Jaccard, NON-SEPARABLE,
//     fp_risk_accepted — wired in T-P4.04.
//
// Source: helix_code qa-results/hxc159/shadowing_curve/p2_curve1/
// (ceiling.json, threshold.json).
package skills
