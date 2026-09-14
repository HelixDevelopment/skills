# T-P4.03 record — namespacing <source>.<name> + coexistence proof

Run: 20260914T021701Z · HXC-159 T-P4.03 (.01–.03 all addressed)

## Rule (T-P4.03.1)

`QualifiedName(source, name) = source + "." + name`. No escape character
by construction: source names are dot-free (`Source.Validate`), bare names
cannot contain `/`, `ParseQualified` splits on the FIRST dot — dots in bare
names round-trip verbatim (tested). `Skill.Qualified()` derives the identity
(never stored, never stale).

## Coexistence proof (T-P4.03.2)

- Fixture: `src-a/same` + `src-b/same` → union 2, identities
  `src-a.same` / `src-b.same`, union-count equality holds per source
  (`TestCrossSourceCoexistence`, committed).
- Real-FS probe (temporary, output in terminal transcript): two on-disk
  corpora with same-named skill → `corpus-a.same` + `corpus-b.same`,
  union=2, err=nil.
- Fail-closed evolution: `TestCollisionFailsClosed` (T-P4.02) migrated to
  duplicate QUALIFIED identity (two dirs, one front-matter name, one
  source) — the intended behavior change, with RED_MODE polarity intact
  on both sides (RED_MODE=1 fails pre/post fix by design).

## Name-matches-directory (T-P4.03.3)

`AssertNameMatchesDir([]Skill) []Skill` returns violators; empty = rule
holds. Committed test covers conforming (empty) + legacy (exactly the
violator). Consumer gates run this over conforming sources.

## Real-corpus shadowing — R-04/R-19 live in the wild

- 20+ bare names duplicated across the HelixAgent tree (measured;
  `real_bare_duplicates.txt`): `skill-creator` ×4 distinct contents/hashes
  (`MCP/submodules/sentry-mcp/…`, `skills/misc/codex-core/…`,
  `skills/misc/vtcode/…`, `skills/codex/…`), `code-review`,
  `skill-adapter`, `create-plan`, …
- Whole-subtree registration REFUSES on real data:
  `duplicate skill "create-plan" (qualified helixagent-skills.create-plan):
  codex/create-plan collides with forge/create-plan — refusing to start`.
  Collision errors cite root-relative paths (Skill.RelPath, added here) so
  same-basename directories are distinguishable.
- Operational consequence (recorded, not solved here): raw whole-tree
  registration of corpus 2 is correctly refused; incorporation proceeds via
  scoped sources / explicit allowlist in T-P4.04. Loader leniency would be
  the bluff — the refusal IS the runtime signature.
