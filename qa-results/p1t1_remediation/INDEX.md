# P1.T1 Fable-remediation — RED→GREEN captured evidence (§11.4.5 / §11.4.115 / §11.4.116)

Each finding fixed in this remediation carries a **RED-baseline** run proving the
new test/gate FAILs on the *pre-fix* defect, plus the **GREEN** run proving it
PASSes after the fix. RED runs were produced in an isolated scratch copy of the
tree with ONLY the specific defect re-introduced (§11.4.115 RED-on-broken-artifact,
surgical — the rest of the change-set intact); GREEN runs were produced on the real
fixed tree. Live DB: pgvector container `skillsys_p1t1_test_pg` at `127.0.0.1:55433`
(admin DB `postgres`).

| Finding | Defect re-introduced for RED | RED log | RED result | GREEN |
|---|---|---|---|---|
| **B1** nested-table TOML decode broken (dotted tags) | decode android.toml through original dotted-tag wrapper vs nested-struct | `B1_red_dotted_tag_decode.log` | dotted → requires=0 resources=0; nested → requires=2 resources=2 | `GREEN_full_p1t1_suite.log` (M6, M10 PASS) |
| **W1** down-migration silent data loss | W1 DO-block guards stripped from `002_granularity.down.sql` | `W1_red_down_migration_dataloss.log` | M9b/M9c/M9d: "expected a fail-closed error (W1), got nil — pre-fix this down silently dropped the … column" | GREEN suite M9b/M9c/M9d PASS |
| **W2(a)** exists-check pair-scoped (rejects 2nd typed edge) | revert triple `(skill_id,depends_on,relation_type)` → pair | `W2_red_edge_and_cycle_scope.log` | `SecondTypedEdgePerPairAccepted`: "expected success …, got error: dependency already exists" | GREEN W2 PASS |
| **W2(b)** cycle scan unscoped (soft edge = false cycle) | revert `hasCycle` `relation_type = ANY($3)` → all relations | `W2_red_edge_and_cycle_scope.log` | `RelatedToBackEdgeIsNotACycle`: "wrongly rejected as a cycle (W2(b) bug)"; control `ComposesCycleIsRejected` still PASS | GREEN W2 PASS |
| **W3** ExportToTOML omits Kind | comment out `Kind: string(skill.Kind)` in `import_export.go` | `W3_red_export_omits_kind.log` | M6: `exported skill.kind = "", want "umbrella"` | GREEN M6 PASS |
| **W4** validator ALL_RELATIONS omits related_to/alternative_to | strip both from `ALL_RELATIONS` in `validate_dag.py` | `W4_N1_red_validate_dag.log` | pre-fix exit 0 (misses `leaf.b --related_to--> does.not.exist`); fixed exit 1 "UNRESOLVED EDGES" | fixed catches; real CORPUS.yaml still `DAG OK (43 nodes, 56 edges)` |
| **N1** validator missing closed-set kind check | neuter `check_kind_values` | `W4_N1_red_validate_dag.log` | pre-fix exit 0; fixed exit 1 "INVALID KIND VALUES: kind='boguskind'" | fixed catches |

## Mμ1–Mμ3 (paired-mutation scenarios, §1.1)

`internal/db/migrations_granularity_test.go`'s header cites the design-doc case
table `M1-M10 + Mμ1-Mμ3`. The Mμ rows are the **paired-mutation** requirement of
§1.1 — a mutation that removes/inverts an invariant MUST make its guarding
test/gate FAIL. Each RED run in this directory IS exactly such a paired mutation
(the fix mutated out; the guarding test asserted to FAIL), so the Mμ obligation is
discharged by the concrete runs above:

- **Mμ1** (schema/migration invariant) → the W1 mutation (guards stripped) → M9b/M9c/M9d FAIL. `W1_red_down_migration_dataloss.log`.
- **Mμ2** (edge/relation-model invariant) → the W2(a)+W2(b) mutations → `SecondTypedEdgePerPairAccepted` + `RelatedToBackEdgeIsNotACycle` FAIL. `W2_red_edge_and_cycle_scope.log`.
- **Mμ3** (TOML decode / round-trip invariant) → the B1 dotted-tag mutation + the W3 Kind-omission mutation → deps/resources decode empty + M6 kind="" FAIL. `B1_red_dotted_tag_decode.log`, `W3_red_export_omits_kind.log`.

No literal `TestP1T1_Mμ*` functions exist in the change-set; the Mμ scenarios are
realized as the mutation-driven RED runs above rather than as separately-named
test functions (stated honestly per §11.4.6 — the evidence is the runs, not a name).

## GREEN suite

`GREEN_full_p1t1_suite.log` — full `TestP1T1` sweep on the fixed tree, all PASS:
M1–M10, M9b/M9c/M9d, W2×3, N6 (Search honestly SKIPs with the captured
`similarity(text, unknown) does not exist` SQLSTATE 42883 pg_trgm-missing reason;
VectorSearch PASS), plus `validate_dag.py seed/CORPUS.yaml` → `DAG OK (43 nodes, 56 edges)`.

## Pre-existing out-of-scope gaps (captured, not masked)

1. **pg_trgm extension never created** → `Store.Search` cannot run on a clean
   deployment (SQLSTATE 42883). N6 Search SKIPs with the captured error. Needs a
   `CREATE EXTENSION pg_trgm` in a migration — out of B1/W1–W4/N1–N6 scope.
2. **HNSW index excludes NULL and zero-magnitude embeddings** → `VectorSearch`
   returns 0 rows until a non-zero embedding is written (N6 writes one to exercise
   the kind-aware CTE). `store.Create` never sets an embedding — out of scope.
3. **3 seed TOMLs require unauthored skills** (`c.language`, `python.language`) →
   after the B1 fix, cpp/make/cmake/android_aosp resolve real requires edges and
   correctly block with `ErrDependencyNotFound`; M10 asserts exactly 4 importable +
   4 blocked. Authoring the 3 missing seed files is out of scope.
