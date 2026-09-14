# Pin-gate redesign note (post-T-P3.01)

The gate as committed under T-P3.03 asserted strict PIN.sha == main tip.
That contradicted the task's own runtime signature ("the recorded gitlink
SHA is an ANCESTOR of the upstream main tip, asserted by a live
`git merge-base --is-ancestor` check") and made every follow-up commit a
gate failure by construction.

Redesigned `gates/cm-submodule-pin-on-main.sh` to the spec: ancestor check
+ branch==main + sha-resolves. New evidence in this run dir:
`gate_pass_ancestor.txt` (PASS), `gate_mutation_helix_skills.txt`
(repoint to helix_skills → FAIL on the branch check), restore PASS.
The earlier `gate_mutation_fail.txt` / `gate_restored_pass.txt` belong to
the superseded equality semantics and are retained as history only.
PIN.md prose updated to the ancestor rule; the recorded SHA (1506c91,
T-P3.03 post-merge tip) is unchanged and still verifies.
