package skill

// W2 remediation tests (Fable code-review NO-GO round, P1.T1): AddDependency's
// pair-scoped duplicate-edge exists-check and hasCycle's cycle-detection walk
// both needed updating for the R16 typed-edge model
// (research/skill_granularity_and_composition.md §4.1) --
//
//   - W2(a): the exists-check was scoped to (skill_id, depends_on) only, so a
//     SECOND typed edge on an already-related pair (e.g. adding `recommends`
//     once `requires` already exists for the same pair) was wrongly rejected
//     as a duplicate, even though migrations/002_granularity.up.sql widened
//     the PK to (skill_id, depends_on, relation_type) specifically to allow
//     this.
//   - W2(b): hasCycle's reachability walk was relation-type-agnostic, so a
//     symmetric advisory edge (related_to/alternative_to) back-edge was
//     wrongly rejected as a structural cycle (ErrCycleDetected), while a
//     genuine hard-closure cycle (e.g. composes forming a loop) must still be
//     rejected.
//
// AddDependency executes entirely inside s.pool.WithTx against real SQL (see
// graph_test.go:58-82's documented boundary: no in-memory graph structure
// exists to unit test this against in its place). Gated on the same
// SKILL_SYSTEM_TEST_DB_* live-database contract as
// migration_granularity_test.go in this package -- absent a configured live
// PostgreSQL, every case honestly t.Skip()s (§11.4.3/§11.4.27).

import (
	"context"
	"errors"
	"testing"

	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/models"
)

// TestP1T1W2_SecondTypedEdgePerPairAccepted proves W2(a): once a `requires`
// edge exists for a (skill_id, depends_on) pair, a SECOND edge on the SAME
// pair with a DIFFERENT relation_type (`recommends`) is accepted, not
// rejected as an "already exists" duplicate.
func TestP1T1W2_SecondTypedEdgePerPairAccepted(t *testing.T) {
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

	a := &models.Skill{Name: "p1t1.w2a.a", Title: "A", Content: "content A", Status: models.SkillStatusDraft, Kind: models.SkillKindAtomic}
	if err := store.Create(ctx, a); err != nil {
		t.Fatalf("create skill A: %v", err)
	}
	b := &models.Skill{Name: "p1t1.w2a.b", Title: "B", Content: "content B", Status: models.SkillStatusDraft, Kind: models.SkillKindAtomic}
	if err := store.Create(ctx, b); err != nil {
		t.Fatalf("create skill B: %v", err)
	}

	if err := store.AddDependency(ctx, a.ID, b.ID, models.DepTypeRequires); err != nil {
		t.Fatalf("AddDependency(A, B, requires): unexpected error: %v", err)
	}

	// W2(a): the pre-fix (skill_id, depends_on)-only exists-check would
	// find the requires row above and reject THIS call with "dependency
	// already exists", even though relation_type differs and the widened
	// PK explicitly allows it.
	if err := store.AddDependency(ctx, a.ID, b.ID, models.DepTypeRecommends); err != nil {
		t.Fatalf("AddDependency(A, B, recommends) on a pair that already has a requires edge: expected success (W2(a): a second typed edge per pair must be accepted), got error: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM skill_dependencies WHERE skill_id = $1 AND depends_on = $2`, a.ID, b.ID).Scan(&count); err != nil {
		t.Fatalf("count edges for (A,B): %v", err)
	}
	if count != 2 {
		t.Errorf("edge count for (A,B) = %d, want 2 (requires + recommends coexisting)", count)
	}
}

// TestP1T1W2_RelatedToBackEdgeIsNotACycle proves W2(b): a `related_to` edge
// A->B followed by its reciprocal back-edge B->A (the same symmetric
// relation recorded from both sides -- research/skill_granularity_and_composition.md
// §4.1) is NOT a structural cycle and must be accepted.
func TestP1T1W2_RelatedToBackEdgeIsNotACycle(t *testing.T) {
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

	a := &models.Skill{Name: "p1t1.w2b.a", Title: "A", Content: "content A", Status: models.SkillStatusDraft, Kind: models.SkillKindAtomic}
	if err := store.Create(ctx, a); err != nil {
		t.Fatalf("create skill A: %v", err)
	}
	b := &models.Skill{Name: "p1t1.w2b.b", Title: "B", Content: "content B", Status: models.SkillStatusDraft, Kind: models.SkillKindAtomic}
	if err := store.Create(ctx, b); err != nil {
		t.Fatalf("create skill B: %v", err)
	}

	if err := store.AddDependency(ctx, a.ID, b.ID, models.DepTypeRelatedTo); err != nil {
		t.Fatalf("AddDependency(A, B, related_to): unexpected error: %v", err)
	}

	// W2(b): the pre-fix relation-type-agnostic hasCycle walk would find
	// A can reach B (via the related_to edge just inserted, since the walk
	// followed every skill_dependencies row regardless of type), so adding
	// B->A would be wrongly flagged as creating a cycle (ErrCycleDetected).
	err = store.AddDependency(ctx, b.ID, a.ID, models.DepTypeRelatedTo)
	if err != nil {
		if errors.Is(err, ErrCycleDetected) {
			t.Fatalf("AddDependency(B, A, related_to) reciprocal back-edge: wrongly rejected as a cycle (W2(b) bug) -- related_to is a symmetric advisory relation, not a structural one: %v", err)
		}
		t.Fatalf("AddDependency(B, A, related_to): unexpected error: %v", err)
	}

	var countAB, countBA int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM skill_dependencies WHERE skill_id = $1 AND depends_on = $2 AND relation_type = 'related_to'`, a.ID, b.ID).Scan(&countAB); err != nil {
		t.Fatalf("count A->B related_to: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM skill_dependencies WHERE skill_id = $1 AND depends_on = $2 AND relation_type = 'related_to'`, b.ID, a.ID).Scan(&countBA); err != nil {
		t.Fatalf("count B->A related_to: %v", err)
	}
	if countAB != 1 {
		t.Errorf("A->B related_to edge count = %d, want 1", countAB)
	}
	if countBA != 1 {
		t.Errorf("B->A related_to edge count = %d, want 1", countBA)
	}
}

// TestP1T1W2_ComposesCycleIsRejected is the non-regression counterpart to
// the two tests above: scoping hasCycle's walk to models.HardClosureTypes
// must NOT disable detection of a genuine hard-closure cycle. This assertion
// holds on BOTH the pre-fix and post-fix code (composes was always part of
// the walk) -- it exists to prove the W2(b) fix does not accidentally widen
// the acyclicity hole beyond the intended advisory-relation carve-out.
func TestP1T1W2_ComposesCycleIsRejected(t *testing.T) {
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

	a := &models.Skill{Name: "p1t1.w2c.a", Title: "A", Content: "content A", Status: models.SkillStatusDraft, Kind: models.SkillKindComposite}
	if err := store.Create(ctx, a); err != nil {
		t.Fatalf("create skill A: %v", err)
	}
	b := &models.Skill{Name: "p1t1.w2c.b", Title: "B", Content: "content B", Status: models.SkillStatusDraft, Kind: models.SkillKindComposite}
	if err := store.Create(ctx, b); err != nil {
		t.Fatalf("create skill B: %v", err)
	}

	if err := store.AddDependency(ctx, a.ID, b.ID, models.DepTypeComposes); err != nil {
		t.Fatalf("AddDependency(A, B, composes): unexpected error: %v", err)
	}

	err = store.AddDependency(ctx, b.ID, a.ID, models.DepTypeComposes)
	if err == nil {
		t.Fatal("AddDependency(B, A, composes) closing a composes loop with A: expected ErrCycleDetected, got nil")
	}
	if !errors.Is(err, ErrCycleDetected) {
		t.Errorf("AddDependency(B, A, composes) error = %v, want it to wrap ErrCycleDetected", err)
	}
}

// TestP1T1NEW1_AdvisoryOverHardIsNotACycle is the RED->GREEN driver for NEW-1
// (Fable code-review round-2): AddDependency called hasCycle(...)
// UNCONDITIONALLY for every relation type, even though the hasCycle WALK
// itself was already correctly scoped to models.HardClosureTypes (W2(b)
// above). That mismatch meant an ADVISORY candidate edge (recommends /
// related_to / alternative_to) whose REVERSE path is a HARD edge was falsely
// rejected as a cycle -- advisory relations are exempt from acyclicity by
// design (research/skill_granularity_and_composition.md §4.1 "exempt (may
// cycle by nature)"). This is the advisory-candidate/hard-reverse-path cell
// of the 2x2 that TestP1T1W2_RelatedToBackEdgeIsNotACycle (advisory/advisory)
// and TestP1T1W2_ComposesCycleIsRejected (hard/hard) do not cover.
func TestP1T1NEW1_AdvisoryOverHardIsNotACycle(t *testing.T) {
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

	a := &models.Skill{Name: "p1t1.new1.a", Title: "A (parent)", Content: "content A", Status: models.SkillStatusDraft, Kind: models.SkillKindComposite}
	if err := store.Create(ctx, a); err != nil {
		t.Fatalf("create skill A: %v", err)
	}
	b := &models.Skill{Name: "p1t1.new1.b", Title: "B (child)", Content: "content B", Status: models.SkillStatusDraft, Kind: models.SkillKindAtomic}
	if err := store.Create(ctx, b); err != nil {
		t.Fatalf("create skill B: %v", err)
	}

	// Hard edge: A composes B (whole -> part).
	if err := store.AddDependency(ctx, a.ID, b.ID, models.DepTypeComposes); err != nil {
		t.Fatalf("AddDependency(A, B, composes): unexpected error: %v", err)
	}

	// Advisory candidate edge B->A, with a HARD reverse path (A->B composes)
	// already present. Pre-fix: hasCycle(ctx, tx, B, A) is called
	// unconditionally, finds A reachable from B via the composes edge, and
	// wrongly returns ErrCycleDetected. Post-fix: the call is gated on
	// models.IsHardClosure(DepTypeRelatedTo) == false, so hasCycle is never
	// invoked for this candidate and the edge is accepted.
	err = store.AddDependency(ctx, b.ID, a.ID, models.DepTypeRelatedTo)
	if err != nil {
		if errors.Is(err, ErrCycleDetected) {
			t.Fatalf("NEW-1: AddDependency(B, A, related_to) advisory candidate over a hard (composes) reverse path: wrongly rejected as a cycle: %v", err)
		}
		t.Fatalf("AddDependency(B, A, related_to): unexpected error: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM skill_dependencies WHERE skill_id = $1 AND depends_on = $2 AND relation_type = 'related_to'`, b.ID, a.ID).Scan(&count); err != nil {
		t.Fatalf("count B->A related_to edge: %v", err)
	}
	if count != 1 {
		t.Errorf("B->A related_to edge count = %d, want 1", count)
	}
}

// TestP1T1NEW1_HardOverAdvisoryAccepted completes the 2x2 candidate-class x
// reverse-path-class matrix: a HARD candidate edge whose reverse path is
// ADVISORY must be accepted, because the hasCycle walk (scoped to
// HardClosureTypes) never follows the advisory reverse edge in the first
// place. This holds on both pre-fix and post-fix code (the walk's own scoping
// is W2(b), already fixed) -- it exists to prove NEW-1's CALL-side gating
// does not accidentally start rejecting this cell too.
func TestP1T1NEW1_HardOverAdvisoryAccepted(t *testing.T) {
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

	a := &models.Skill{Name: "p1t1.new1b.a", Title: "A", Content: "content A", Status: models.SkillStatusDraft, Kind: models.SkillKindComposite}
	if err := store.Create(ctx, a); err != nil {
		t.Fatalf("create skill A: %v", err)
	}
	b := &models.Skill{Name: "p1t1.new1b.b", Title: "B", Content: "content B", Status: models.SkillStatusDraft, Kind: models.SkillKindAtomic}
	if err := store.Create(ctx, b); err != nil {
		t.Fatalf("create skill B: %v", err)
	}

	// Advisory edge: A related_to B.
	if err := store.AddDependency(ctx, a.ID, b.ID, models.DepTypeRelatedTo); err != nil {
		t.Fatalf("AddDependency(A, B, related_to): unexpected error: %v", err)
	}

	// Hard candidate edge B->A, with an ADVISORY reverse path (A->B
	// related_to). The hard-scoped walk never follows the related_to edge,
	// so B cannot reach A through it, and the composes edge is accepted.
	err = store.AddDependency(ctx, b.ID, a.ID, models.DepTypeComposes)
	if err != nil {
		t.Fatalf("AddDependency(B, A, composes) hard candidate over an advisory (related_to) reverse path: expected success, got error: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM skill_dependencies WHERE skill_id = $1 AND depends_on = $2 AND relation_type = 'composes'`, b.ID, a.ID).Scan(&count); err != nil {
		t.Fatalf("count B->A composes edge: %v", err)
	}
	if count != 1 {
		t.Errorf("B->A composes edge count = %d, want 1", count)
	}
}
