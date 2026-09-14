# Challenges — HelixKnowledge Skill Graph System

Challenges are adversarial verification tests that prove each system component
behaves correctly under stress, chaos, and edge-case conditions. They compose
with HelixQA test banks (§11.4.27) and produce captured evidence per §11.4.69.

## Challenge bank inventory

| ID | Name | Package | Category |
|---|---|---|---|
| CME-CODEGRAPH-001 | Concurrent evidence dedup | codegraph | stress |
| CME-CODEGRAPH-002 | Unavailable server degradation | codegraph | chaos |
| CME-DEDUP-001 | Classifier consistency under load | dedup | stress |
| CME-DEDUP-002 | Edge-case name normalization | dedup | chaos |
| CME-SKILLSOURCE-001 | Source validation integrity | skillsource | chaos |
| CME-MODELS-001 | Skill struct safety | models | chaos |
| CME-STRESS-FULL | Full stress sweep | all | stress |
| CME-CHAOS-FULL | Full chaos sweep | all | chaos |

## Running challenges

```bash
# Individual challenge
go test ./internal/codegraph/ -run TestStress_ConcurrentSymbolsToEvidence -v

# Full stress sweep
go test ./... -run "TestStress" -v

# Full chaos sweep  
go test ./... -run "TestChaos" -v

# Fuzz (run for at least 30s each)
go test ./internal/codegraph/ -fuzz=FuzzEvidenceCache -fuzztime=30s
go test ./internal/source/dedup/ -fuzz=FuzzClassify -fuzztime=30s
go test ./internal/skillsource/ -fuzz=FuzzValidate -fuzztime=30s
go test ./internal/models/ -fuzz=FuzzSkillKindNormalize -fuzztime=30s
go test ./internal/models/ -fuzz=FuzzSkillJSON -fuzztime=30s
```

## Evidence contract

Each challenge produces captured evidence under `qa-results/`:
- `test_exit_zero` — the test binary exits 0
- `captured_output_contains "PASS"` — every sub-test emits PASS
- For fuzz: `no_panics` — the fuzz target never panics or crashes

Last verified: 2026-07-18
