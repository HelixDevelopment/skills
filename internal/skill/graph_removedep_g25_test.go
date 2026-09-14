package skill

// G25 — RemoveDependency's audit trail must record an EXPLICIT lookup-failure
// marker when an endpoint skill name lookup fails, instead of silently
// discarding the Scan error and persisting a bare `"from":""` / `"to":""` an
// operator cannot distinguish from a skill whose name legitimately is empty
// (GAPS_AND_RISKS_REGISTER.md §G25; research/g18_g25_g26_correctness_bundle.md
// §2). Before this fix graph.go's RemoveDependency did
// `_ = tx.QueryRow(...).Scan(&fromName)` (the two error-discarding lines),
// degrading the R11 audit evidence trail.
//
// Coverage split (§11.4.108 source != runtime):
//   - Tests 1-3 exercise the pure buildRemovalAuditDetail helper in isolation
//     (no live DB needed), independently verifying EACH of the two Scan-error
//     branches per §11.4.194's multi-factor mandate plus the success-path
//     regression floor.
//   - Test 4 proves the helper is actually WIRED into the live RemoveDependency
//     path by reading back the PERSISTED audit_log JSONB from a real,
//     freshly-migrated PostgreSQL. It is gated on the same SKILL_SYSTEM_TEST_DB_*
//     contract as this package's other live tests (graph_granularity_test.go /
//     migration_granularity_test.go) and honestly t.Skip()s absent a live DB.

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/google/uuid"
)

// Test 1 — the SOURCE-endpoint lookup failure is recorded as an explicit
// `from_lookup_error` marker, never a silently-empty `"from"`. Verified
// independently of the target branch (§11.4.194 multi-factor: a single combined
// test would not prove this branch in isolation).
func TestBuildRemovalAuditDetail_SourceLookupFailureIsExplicit(t *testing.T) {
	detail := buildRemovalAuditDetail("", errors.New("boom-from"), "target-name", nil)

	if _, ok := detail["from_lookup_error"]; !ok {
		t.Errorf(`expected a "from_lookup_error" key when the source lookup failed; got detail=%v`, detail)
	}
	if got := detail["from_lookup_error"]; got != "boom-from" {
		t.Errorf(`from_lookup_error = %v, want "boom-from"`, got)
	}
	if _, ok := detail["from"]; ok {
		t.Errorf(`expected NO bare "from" key on a source lookup failure (an empty-string name is indistinguishable from success); got detail=%v`, detail)
	}
	// The target succeeded and must still be recorded by its real name.
	if got := detail["to"]; got != "target-name" {
		t.Errorf(`to = %v, want "target-name"`, got)
	}
	if _, ok := detail["to_lookup_error"]; ok {
		t.Errorf(`expected NO "to_lookup_error" when the target lookup succeeded; got detail=%v`, detail)
	}
}

// Test 2 — the TARGET-endpoint lookup failure is recorded as an explicit
// `to_lookup_error` marker, verified independently of the source branch.
func TestBuildRemovalAuditDetail_TargetLookupFailureIsExplicit(t *testing.T) {
	detail := buildRemovalAuditDetail("source-name", nil, "", errors.New("boom-to"))

	if _, ok := detail["to_lookup_error"]; !ok {
		t.Errorf(`expected a "to_lookup_error" key when the target lookup failed; got detail=%v`, detail)
	}
	if got := detail["to_lookup_error"]; got != "boom-to" {
		t.Errorf(`to_lookup_error = %v, want "boom-to"`, got)
	}
	if _, ok := detail["to"]; ok {
		t.Errorf(`expected NO bare "to" key on a target lookup failure; got detail=%v`, detail)
	}
	// The source succeeded and must still be recorded by its real name.
	if got := detail["from"]; got != "source-name" {
		t.Errorf(`from = %v, want "source-name"`, got)
	}
	if _, ok := detail["from_lookup_error"]; ok {
		t.Errorf(`expected NO "from_lookup_error" when the source lookup succeeded; got detail=%v`, detail)
	}
}

// Test 3 — regression floor: when BOTH lookups succeed the detail map is
// byte-identical to the pre-refactor {"from":..,"to":..} shape, proving the
// helper does not regress the common success path.
func TestBuildRemovalAuditDetail_BothLookupsSucceed_UnchangedShape(t *testing.T) {
	detail := buildRemovalAuditDetail("skill-a", nil, "skill-b", nil)

	want := map[string]interface{}{"from": "skill-a", "to": "skill-b"}
	if !reflect.DeepEqual(detail, want) {
		t.Errorf("detail = %v, want %v (the success path must stay byte-identical to the pre-refactor {from,to} shape)", detail, want)
	}
}

// Test 4 — live-DB wiring proof (§11.4.108 source != runtime; §11.4.115
// RED-first): removing a dependency edge whose TARGET skill row is ABSENT must
// persist an audit_log detail carrying the explicit `to_lookup_error` marker,
// NOT a silently-empty `"to":""`. This reproduces the register's "a skill is
// already gone" precondition and reads back the REAL persisted JSONB as
// captured evidence.
//
// RED on the pre-fix `_ = ...Scan(&toName)` discard code (the paired §1.1
// mutation reverting the call site reproduces it): the persisted detail is
// `{"from":<src>,"to":""}` with no `to_lookup_error` key, so this test FAILs.
func TestRemoveDependency_AuditRecordsExplicitLookupFailure_RequiresLiveDatabase(t *testing.T) {
	admin, ok := skillSkipIfNoTestDB(t)
	if !ok {
		return
	}
	ctx := context.Background()

	dbCfg, cleanup := skillCreateThrowawayDB(t, admin)
	defer cleanup()

	pool, err := db.New(dbCfg)
	if err != nil {
		t.Fatalf("db.New(dbCfg): %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool, realMigrationsDirFromSkillPkg); err != nil {
		t.Fatalf("db.Migrate (full real migrations dir): %v", err)
	}

	store := NewStore(pool)

	// The SOURCE skill must exist: audit_log.skill_id references skills(id)
	// with NO cascade (migrations/001_initial.up.sql:84), and the from-name
	// lookup must succeed so the failing endpoint is unambiguously the target.
	src := &models.Skill{Name: "g25.remove.src", Title: "Source", Content: "content", Status: models.SkillStatusDraft, Kind: models.SkillKindAtomic}
	if err := store.Create(ctx, src); err != nil {
		t.Fatalf("create source skill: %v", err)
	}

	// Construct the defect precondition: a dependency edge whose TARGET skill
	// row is ABSENT. The production schema's ON DELETE CASCADE on
	// skill_dependencies.depends_on (migrations/001_initial.up.sql:27) would
	// otherwise cascade-delete the edge the instant its target skill vanished,
	// so we drop that ONE FK in this throwaway DB to hold the edge orphaned --
	// exactly the "a skill is already gone" state §G25 describes. The
	// production RemoveDependency path under test runs UNMODIFIED against the
	// real migrated schema; only this fixture state is arranged. If the
	// constraint name ever diverged, the INSERT below fails loudly (a real
	// FK-violation error), never a false pass.
	if _, err := pool.Exec(ctx, `ALTER TABLE skill_dependencies DROP CONSTRAINT IF EXISTS skill_dependencies_depends_on_fkey`); err != nil {
		t.Fatalf("drop depends_on FK to orphan the edge: %v", err)
	}
	absentTarget := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO skill_dependencies (skill_id, depends_on, relation_type) VALUES ($1, $2, 'requires')`, src.ID, absentTarget); err != nil {
		t.Fatalf("insert orphaned edge (src -> absent target): %v", err)
	}

	if err := store.RemoveDependency(ctx, src.ID, absentTarget); err != nil {
		t.Fatalf("RemoveDependency(src, absentTarget): unexpected error: %v", err)
	}

	// Read back the PERSISTED audit detail -- real captured runtime evidence.
	var rawDetail []byte
	if err := pool.QueryRow(ctx,
		`SELECT details FROM audit_log WHERE event = 'dependency.removed' AND skill_id = $1 ORDER BY ts DESC LIMIT 1`,
		src.ID).Scan(&rawDetail); err != nil {
		t.Fatalf("read back dependency.removed audit detail: %v", err)
	}
	var detail map[string]interface{}
	if err := json.Unmarshal(rawDetail, &detail); err != nil {
		t.Fatalf("unmarshal persisted audit detail JSONB %q: %v", rawDetail, err)
	}

	// GREEN (post-fix): the absent target endpoint is recorded explicitly.
	if _, ok := detail["to_lookup_error"]; !ok {
		t.Errorf(`persisted audit detail must carry a "to_lookup_error" for the absent target endpoint; got %v (RED on the pre-fix discard-the-Scan-error code, which records a bare "to":"")`, detail)
	}
	// The silently-empty "to":"" the G25 defect produced must be gone.
	if v, ok := detail["to"]; ok && v == "" {
		t.Errorf(`persisted audit detail still carries a silently-empty "to":"" (the G25 defect); got %v`, detail)
	}
	// The present source endpoint must still record its real name.
	if got, _ := detail["from"].(string); got != src.Name {
		t.Errorf(`persisted "from" = %q, want the real source name %q`, got, src.Name)
	}
}
