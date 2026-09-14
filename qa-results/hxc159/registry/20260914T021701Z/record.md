# T-P4.02 record — typed registry, real-corpus verdicts, one reconciliation

Run: 20260914T021701Z · HXC-159 T-P4.02 (.01–.05 all addressed)

## Runtime signature

Collision fixture → typed `*DuplicateSkillError` naming skill + BOTH
sides; union count == sum of per-source counts (fixture 2+1=3, standing
test `TestUnionCountEquality`); corpus registered by path reference with
the source tree provably unmutated (`TestLoadWritesNothingProvesInPlace`).

## Real-corpus verdicts (in place, read-only, 2026-09-14)

| source | files | ok | name-mismatch (recorded) | bad (refused) | elapsed |
|---|---|---|---|---|---|
| constitution (first-party, agent) | 8 | 5 | 4 | 3, all "no YAML front-matter block" (media-validator, scheduled-work-queue, session-sync) | 1ms |
| helixagent (vendored, helixagent) | 1315 | 1312 | 20 | 3, all "front-matter carries no name", all under third-party `external/cognee/…` | 402ms |
| upstream graph | 0 | 0 | 0 | 0 | — |

Full per-file table: `real_corpus_verdicts.txt` (30 BAD/MISMATCH rows).
Task shape "3+1174+0" vs measured loadable "5+1312+0": recorded as drift
(§11.4.6), not forced. The 3+3 refusals are NAMED corpus-hygiene gaps for
follow-on work (fail-closed is correct per RS-04/T-P6.04 direction); they
do not block registration-by-reference.

## §11.4.120 reconciliation — Name==Dir refused → recorded

The T-P4.01 strict refusal was refuted by the probes above: BOTH
production corpora systematically violate Name==Dir while NEITHER
production loader enforces it (`helix_agent/internal/skills/loader.go:44`
`LoadFromDirectory`: recursive walk, `EqualFold` manifest match, registers
by front-matter `skill.Name`, zero directory comparison). Refusing at load
rejected corpora 1+2 wholesale — a CONST-035 usability defect. New
mechanism: `Skill.NameMismatch` recorded at load + `AssertNameMatchesDir`
report in T-P4.03.3 over conforming sources; load never refuses a legacy
name. Test `TestLoaderRejectsNameDirectoryMismatch` rewritten to
`TestLoaderRecordsNameDirectoryMismatch` with this citation (reconciled,
not weakened: gate+mutation still a valid §1.1 pair).

## §11.4.74 note

Registry/manager/loader/validator of `skill_registry` re-examined for the
registry shape: its registry is a CLI-agent map + its manager an
executor-lifecycle owner — neither is a multi-source enumeration with
fail-closed collision. Shapes reused (sentinel errors); no code reused
(yaml.v3 + executor scope). `LoadOne` added as the per-file verdict seam
the T-P6.04 surface will need (justified by the probe above, not speculative).
