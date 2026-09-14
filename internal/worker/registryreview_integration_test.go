package worker

// G32 F1 remediation (research/p05_high_defect_fix_designs.md §4.4/§4.6):
// integration test against a REAL PostgreSQL proving the registry-review
// ticker cycle (Runner.runRegistryReview, the function
// registryReviewWorker's `case <-ticker.C:` arm invokes on every tick)
// genuinely marks a 30+-day-old skill stale via the AGE stale-mark -- not
// merely that some interface method got called (that is the unit test,
// registryreview_unit_test.go).
//
// Scenario (§11.4.194 NON-overdetermined isolation): seed an `active` skill
// with ONE validated evidence -- so RunReviewOnce's UpdateCoverage computes
// coverage = 1.0, above the 0.25 low-coverage threshold -- NO dependencies
// (missing_deps stays '{}'), and `last_review` 31 days old. The 30-day age
// mark is thus the ONLY stale-mark whose precondition is met, so a
// `stale=true` outcome can be attributed to it and it ALONE (an earlier
// version of this test seeded coverage=0.0, which meant the low-coverage
// mark -- not the age mark -- flipped stale, making its "the 30+-day mark
// fires" claim false; that overdetermination is now removed).
//
// RED (pre-F1-fix): the assertion FAILs. Before the fix, RunReviewOnce ran
// UpdateCoverage FIRST, refreshing last_review = NOW() for every
// validated/active row, so the age mark two statements later could never
// fire; with coverage 1.0 and no missing deps, nothing marks this skill
// stale and it stays false. Captured in
// qa-results/g32_f1/05_red_integration.log.
//
// GREEN (post-F1-fix): RunReviewOnce runs the age mark BEFORE UpdateCoverage,
// so the 31-day-old last_review is read before it is refreshed and the skill
// is marked stale. Captured in qa-results/g32_f1/06_green_integration.log --
// a §11.4.108 runtime-signature: a real skill_registry row transition on a
// clean, freshly-migrated database, driven through the exact tick-invoked
// function, not a log line.
//
// Gated on SKILL_SYSTEM_TEST_DB_HOST (§11.4.3): absent a configured live
// database this honestly t.Skip()s, never a fake PASS (§11.4.27).

import (
	"context"
	"testing"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/HelixDevelopment/skills/internal/skill"
	"go.uber.org/zap"
)

func TestRunRegistryReview_MarksStaleSkillsOnRealDB_RequiresLiveDatabase(t *testing.T) {
	admin, ok := workerSkipIfNoTestDB(t)
	if !ok {
		return
	}
	ctx := context.Background()

	dbCfg, cleanup := workerCreateThrowawayDB(t, admin)
	defer cleanup()

	pool, err := db.New(dbCfg)
	if err != nil {
		t.Fatalf("db.New(dbCfg): %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool, workerRealMigrationsDir); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	store := skill.NewStore(pool)

	sk := &models.Skill{
		Name:    "g32f1.registryreview.age-stale-candidate",
		Title:   "G32 F1 age-stale candidate",
		Content: "content for the G32 F1 registry-review integration test",
		Status:  models.SkillStatusActive,
		Kind:    models.SkillKindAtomic,
	}
	if err := store.Create(ctx, sk); err != nil {
		t.Fatalf("create skill: %v", err)
	}

	// One validated evidence => UpdateCoverage computes coverage = 1.0, so
	// the low-coverage mark cannot fire. Together with no dependencies
	// (missing_deps stays '{}'), the age mark is the ONLY stale-mark whose
	// precondition is met -- isolating the specific G32 F1 capability.
	if _, err := pool.Exec(ctx, `INSERT INTO evidences (skill_id, source_project, validated) VALUES ($1, 'g32-f1', true)`, sk.ID); err != nil {
		t.Fatalf("insert validated evidence: %v", err)
	}

	// Backdate last_review to 31 days ago -- exactly the design doc's §4.4
	// seed scenario -- so the skill is eligible for the "not reviewed in
	// 30+ days" stale-mark check.
	if _, err := pool.Exec(ctx, `UPDATE skill_registry SET last_review = NOW() - INTERVAL '31 days' WHERE skill_id = $1`, sk.ID); err != nil {
		t.Fatalf("backdate last_review: %v", err)
	}

	var staleBefore bool
	if err := pool.QueryRow(ctx, `SELECT stale FROM skill_registry WHERE skill_id = $1`, sk.ID).Scan(&staleBefore); err != nil {
		t.Fatalf("query stale flag (precondition): %v", err)
	}
	if staleBefore {
		t.Fatal("precondition failed: seeded skill_registry row must start with stale=false " +
			"(Store.Create inserts stale=false, coverage=0.0 by default)")
	}

	cfg := config.Config{
		Registry: config.RegistryConfig{ReviewIntervalHours: 1},
	}
	runner := NewRunner(pool, store, cfg, zap.NewNop())

	// Invoke the EXACT function registryReviewWorker's `case <-ticker.C:`
	// arm calls on every tick -- the real tick-invoked cycle, not a
	// reimplementation of it (§11.4.199 exact-reproduction-sequence).
	runner.runRegistryReview(ctx)

	var staleAfter bool
	if err := pool.QueryRow(ctx, `SELECT stale FROM skill_registry WHERE skill_id = $1`, sk.ID).Scan(&staleAfter); err != nil {
		t.Fatalf("query stale flag (after review cycle): %v", err)
	}

	if !staleAfter {
		t.Fatalf("skill_registry.stale = false after runRegistryReview; want true -- the " +
			"30+-day-old, active skill seeded above (coverage 1.0, no missing deps) must be marked " +
			"stale by the AGE stale-mark inside the registry-review ticker cycle. Pre-F1-fix " +
			"RunReviewOnce ran UpdateCoverage first, refreshing last_review = NOW() so the age mark " +
			"could never fire; post-fix the age mark runs before UpdateCoverage " +
			"(research/p05_high_defect_fix_designs.md §4).")
	}
}
