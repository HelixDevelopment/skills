package registry

// G32 remediation (research/p05_high_defect_fix_designs.md §4.3 step 2):
// RunReviewOnce and performReview previously implemented two SEPARATE,
// slightly-divergent review passes -- performReview's inline 5-step body
// had a low-coverage stale-mark (markLowCoverageSkillsStale) that
// RunReviewOnce's own 4-step body lacked. RunReviewOnce is now extended to
// perform all 5 checks, and performReview delegates to it instead of
// duplicating the steps inline, closing that asymmetry.
//
// These tests seed a skill whose skill_registry row has last_review set to
// "just now" (so the age-based stale-mark does NOT fire) and coverage at
// its Store.Create default of 0.0 (below the 0.25 threshold, so ONLY the
// low-coverage check can be responsible for a stale=true outcome) --
// isolating the specific capability this consolidation added/preserved.
//
// Gated on SKILL_SYSTEM_TEST_DB_HOST (§11.4.3): absent a configured live
// database this honestly t.Skip()s, never a fake PASS (§11.4.27).

import (
	"context"
	"testing"

	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/HelixDevelopment/skills/internal/skill"
)

// seedFreshLowCoverageActiveSkill creates an active skill via skill.Store
// (which inserts a skill_registry row with stale=false, coverage=0.0,
// missing_deps='{}' by default), then backdates last_review to "just now"
// so the age-based stale-mark cannot fire, isolating the low-coverage
// check.
func seedFreshLowCoverageActiveSkill(t *testing.T, ctx context.Context, pool *db.Pool, name string) *models.Skill {
	t.Helper()

	store := skill.NewStore(pool)
	sk := &models.Skill{
		Name:    name,
		Title:   "G32 low-coverage candidate",
		Content: "content for the G32 RunReviewOnce/performReview parity test",
		Status:  models.SkillStatusActive,
		Kind:    models.SkillKindAtomic,
	}
	if err := store.Create(ctx, sk); err != nil {
		t.Fatalf("create skill: %v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE skill_registry SET last_review = NOW() WHERE skill_id = $1`, sk.ID); err != nil {
		t.Fatalf("set last_review to now: %v", err)
	}

	return sk
}

func queryStale(t *testing.T, ctx context.Context, pool *db.Pool, skillID interface{ String() string }) bool {
	t.Helper()
	var stale bool
	if err := pool.QueryRow(ctx, `SELECT stale FROM skill_registry WHERE skill_id = $1`, skillID).Scan(&stale); err != nil {
		t.Fatalf("query stale flag: %v", err)
	}
	return stale
}

// TestRunReviewOnce_MarksLowCoverageSkillStale_RequiresLiveDatabase proves
// RunReviewOnce now performs the low-coverage stale-mark it previously
// lacked (the exact asymmetry this consolidation closes).
func TestRunReviewOnce_MarksLowCoverageSkillStale_RequiresLiveDatabase(t *testing.T) {
	admin, ok := registrySkipIfNoTestDB(t)
	if !ok {
		return
	}
	ctx := context.Background()

	dbCfg, cleanup := registryCreateThrowawayDB(t, admin)
	defer cleanup()

	pool, err := db.New(dbCfg)
	if err != nil {
		t.Fatalf("db.New(dbCfg): %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool, registryRealMigrationsDir); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	sk := seedFreshLowCoverageActiveSkill(t, ctx, pool, "g32.runreviewonce.low-coverage")

	if queryStale(t, ctx, pool, sk.ID) {
		t.Fatal("precondition failed: seeded skill_registry row must start with stale=false")
	}

	reg := NewRegistry(pool)
	if err := reg.RunReviewOnce(ctx); err != nil {
		t.Fatalf("RunReviewOnce: %v", err)
	}

	if !queryStale(t, ctx, pool, sk.ID) {
		t.Fatal("skill_registry.stale = false after RunReviewOnce; want true -- a skill with " +
			"coverage 0.0 (< 0.25) and a fresh (non-stale-triggering) last_review must be marked " +
			"stale by the low-coverage check RunReviewOnce previously lacked")
	}
}

// TestPerformReview_StillMarksLowCoverageSkillStale_RequiresLiveDatabase
// proves the consolidation did not regress performReview's pre-existing
// low-coverage stale-mark capability now that performReview delegates to
// RunReviewOnce instead of duplicating the 5 steps inline.
func TestPerformReview_StillMarksLowCoverageSkillStale_RequiresLiveDatabase(t *testing.T) {
	admin, ok := registrySkipIfNoTestDB(t)
	if !ok {
		return
	}
	ctx := context.Background()

	dbCfg, cleanup := registryCreateThrowawayDB(t, admin)
	defer cleanup()

	pool, err := db.New(dbCfg)
	if err != nil {
		t.Fatalf("db.New(dbCfg): %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool, registryRealMigrationsDir); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	sk := seedFreshLowCoverageActiveSkill(t, ctx, pool, "g32.performreview.low-coverage")

	if queryStale(t, ctx, pool, sk.ID) {
		t.Fatal("precondition failed: seeded skill_registry row must start with stale=false")
	}

	reg := NewRegistry(pool)
	rs := &ReviewScheduler{registry: reg}
	rs.performReview(ctx)

	if !queryStale(t, ctx, pool, sk.ID) {
		t.Fatal("skill_registry.stale = false after performReview; want true -- performReview " +
			"delegating to RunReviewOnce (research/p05_high_defect_fix_designs.md §4.3 step 2) " +
			"must not lose the low-coverage stale-mark it already performed before consolidation")
	}
}

// ---------------------------------------------------------------------------
// G32 F1 remediation (research/p05_high_defect_fix_designs.md §4.3/§4.4/§4.6)
// ---------------------------------------------------------------------------
//
// The G32 consolidation left RunReviewOnce running UpdateCoverage FIRST.
// UpdateCoverage (registry.go) sets last_review = NOW() for every
// validated/active registry row, so the "not reviewed in 30+ days" age
// stale-mark two statements later -- which requires last_review < NOW() -
// INTERVAL '30 days' on that SAME row-set -- could never fire within one
// cycle: an unsatisfiable conjunction that made the age check provably
// DEAD, a real semantic-capability loss on a deliberately-retained review
// check (the pre-consolidation performReview ran the age mark BEFORE the
// recalcs, so it genuinely could fire).
//
// The F1 fix reorders ONLY the age stale-mark to run before UpdateCoverage
// (restoring its pre-consolidation position), while the missing-dep and
// low-coverage marks stay AFTER their recalcs so they keep reading
// freshly-recomputed missing_deps / coverage columns (moving THOSE before
// the recalcs would read stale, previous-cycle values -- a new bug the fix
// deliberately avoids). The tests below isolate the age check and the
// missing-dependency check so all three stale-marks are independently
// covered (§4.6), each in a NON-overdetermined scenario (§11.4.194) where
// exactly one mark's precondition is met.

// seedAgeOnlyActiveSkill creates an active skill with ONE validated
// evidence -- so UpdateCoverage computes coverage = 1/1 = 1.0, above the
// 0.25 low-coverage threshold -- and NO dependencies (missing_deps stays
// '{}'), then backdates last_review to 31 days ago. The 30-day age check is
// thus the ONLY stale-mark whose precondition is met, so a stale=true
// outcome can be attributed to it and it alone.
func seedAgeOnlyActiveSkill(t *testing.T, ctx context.Context, pool *db.Pool, name string) *models.Skill {
	t.Helper()

	store := skill.NewStore(pool)
	sk := &models.Skill{
		Name:    name,
		Title:   "G32 F1 age-only candidate",
		Content: "content for the G32 F1 age-mark isolation test",
		Status:  models.SkillStatusActive,
		Kind:    models.SkillKindAtomic,
	}
	if err := store.Create(ctx, sk); err != nil {
		t.Fatalf("create skill: %v", err)
	}

	// One validated evidence => UpdateCoverage computes coverage = 1.0, so
	// the low-coverage mark cannot fire and cannot mask the age mark.
	if _, err := pool.Exec(ctx, `INSERT INTO evidences (skill_id, source_project, validated) VALUES ($1, 'g32-f1', true)`, sk.ID); err != nil {
		t.Fatalf("insert validated evidence: %v", err)
	}

	// Backdate last_review 31 days: strictly past the 30-day window. BEFORE
	// the fix, UpdateCoverage refreshes this to NOW() before the age mark
	// runs, so the age mark cannot fire (the defect under test).
	if _, err := pool.Exec(ctx, `UPDATE skill_registry SET last_review = NOW() - INTERVAL '31 days' WHERE skill_id = $1`, sk.ID); err != nil {
		t.Fatalf("backdate last_review: %v", err)
	}

	return sk
}

// seedMissingDepFreshActiveSkill creates an active skill with ONE validated
// evidence (coverage = 1.0, so the low-coverage mark cannot fire), a fresh
// last_review (so the age mark cannot fire), and a dependency on a
// NON-active (draft) skill (so CalculateMissingDeps records a non-empty
// missing_deps). The missing-dependency mark is thus the ONLY stale-mark
// whose precondition is met.
func seedMissingDepFreshActiveSkill(t *testing.T, ctx context.Context, pool *db.Pool, name string) *models.Skill {
	t.Helper()

	store := skill.NewStore(pool)

	dep := &models.Skill{
		Name:    name + ".unresolved-dep",
		Title:   "G32 F1 unresolved dependency (draft)",
		Content: "a draft dependency that is neither validated nor active",
		Status:  models.SkillStatusDraft,
		Kind:    models.SkillKindAtomic,
	}
	if err := store.Create(ctx, dep); err != nil {
		t.Fatalf("create dependency skill: %v", err)
	}

	sk := &models.Skill{
		Name:    name,
		Title:   "G32 F1 missing-dep candidate",
		Content: "content for the G32 F1 missing-dependency isolation test",
		Status:  models.SkillStatusActive,
		Kind:    models.SkillKindAtomic,
	}
	if err := store.Create(ctx, sk); err != nil {
		t.Fatalf("create skill: %v", err)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO evidences (skill_id, source_project, validated) VALUES ($1, 'g32-f1', true)`, sk.ID); err != nil {
		t.Fatalf("insert validated evidence: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO skill_dependencies (skill_id, depends_on) VALUES ($1, $2)`, sk.ID, dep.ID); err != nil {
		t.Fatalf("insert dependency edge: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE skill_registry SET last_review = NOW() WHERE skill_id = $1`, sk.ID); err != nil {
		t.Fatalf("set last_review to now: %v", err)
	}

	return sk
}

// TestRunReviewOnce_MarksAgeStaleSkillStale_RequiresLiveDatabase is the
// primary G32 F1 regression guard. It proves the 30-day age stale-mark
// genuinely fires in an age-ONLY scenario: RED on the pre-fix artifact
// (UpdateCoverage refreshed last_review before the age mark, so stale
// stayed false), GREEN after the reorder.
func TestRunReviewOnce_MarksAgeStaleSkillStale_RequiresLiveDatabase(t *testing.T) {
	admin, ok := registrySkipIfNoTestDB(t)
	if !ok {
		return
	}
	ctx := context.Background()

	dbCfg, cleanup := registryCreateThrowawayDB(t, admin)
	defer cleanup()

	pool, err := db.New(dbCfg)
	if err != nil {
		t.Fatalf("db.New(dbCfg): %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool, registryRealMigrationsDir); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	sk := seedAgeOnlyActiveSkill(t, ctx, pool, "g32f1.runreviewonce.age-only")

	if queryStale(t, ctx, pool, sk.ID) {
		t.Fatal("precondition failed: seeded skill_registry row must start with stale=false")
	}

	reg := NewRegistry(pool)
	if err := reg.RunReviewOnce(ctx); err != nil {
		t.Fatalf("RunReviewOnce: %v", err)
	}

	if !queryStale(t, ctx, pool, sk.ID) {
		t.Fatal("skill_registry.stale = false after RunReviewOnce; want true -- a skill with " +
			"coverage 1.0 (>= 0.25), NO missing deps, and last_review 31 days old must be marked " +
			"stale by the 30-day age check ALONE. Pre-fix this check ran after UpdateCoverage's " +
			"last_review = NOW() refresh and could never fire (research/p05_high_defect_fix_designs.md §4).")
	}
}

// TestRunReviewOnce_MarksMissingDepSkillStale_RequiresLiveDatabase isolates
// the missing-dependency stale-mark (age and low-coverage preconditions
// deliberately unmet), so the three stale-marks are each independently
// covered.
func TestRunReviewOnce_MarksMissingDepSkillStale_RequiresLiveDatabase(t *testing.T) {
	admin, ok := registrySkipIfNoTestDB(t)
	if !ok {
		return
	}
	ctx := context.Background()

	dbCfg, cleanup := registryCreateThrowawayDB(t, admin)
	defer cleanup()

	pool, err := db.New(dbCfg)
	if err != nil {
		t.Fatalf("db.New(dbCfg): %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool, registryRealMigrationsDir); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	sk := seedMissingDepFreshActiveSkill(t, ctx, pool, "g32f1.runreviewonce.missing-dep")

	if queryStale(t, ctx, pool, sk.ID) {
		t.Fatal("precondition failed: seeded skill_registry row must start with stale=false")
	}

	reg := NewRegistry(pool)
	if err := reg.RunReviewOnce(ctx); err != nil {
		t.Fatalf("RunReviewOnce: %v", err)
	}

	if !queryStale(t, ctx, pool, sk.ID) {
		t.Fatal("skill_registry.stale = false after RunReviewOnce; want true -- a skill with " +
			"coverage 1.0 (>= 0.25), a fresh last_review, and a dependency on a draft skill must " +
			"be marked stale by the missing-dependency check ALONE.")
	}
}
