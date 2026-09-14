package autoexpand

// G03 remediation (the autoexpand+worker half) -- live-database (no live LLM
// required) proof that draftPersistAndCrossReference (pipeline.go) honours the
// G20 anti-bluff contract: with no LLM client configured, DraftSkill refuses
// to create a placeholder by returning an error, and draftPersistAndCrossReference
// propagates that error back to the caller -- no skill row is ever persisted
// for an unverified draft.
//
// This test deliberately does NOT configure an LLM client (p.llm stays
// nil). Before G20 the no-LLM path fell through to createMinimalDraft and
// persisted a placeholder; now it returns an error instead. The
// LLM-backed drafting half is covered separately, with a real Anthropic call,
// by pipeline_crossreference_integration_test.go (env-gated on ANTHROPIC_API_KEY,
// `integration` build tag).
//
// Why draftPersistAndCrossReference is called directly with a manually
// constructed Gap, rather than through Pipeline.Run's own gap scan: see
// pipeline_crossreference_integration_test.go's header comment for the
// confirmed, cited FACT (not a guess, §11.4.6) that Run's gap-detection
// cannot fire against any graph the store API constructs (a hand-inserted
// zero-UUID skills row via raw SQL is schema-valid and does make the gate
// reachable -- see G137, GAPS_AND_RISKS_REGISTER.md).
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

func TestDraftPersistAndCrossReference_PersistsAndCrossReferences_RequiresLiveDatabase(t *testing.T) {
	admin, ok := aeSkipIfNoTestDB(t)
	if !ok {
		return
	}
	ctx := context.Background()

	dbCfg, cleanup := aeCreateThrowawayDB(t, admin)
	defer cleanup()

	pool, err := db.New(dbCfg)
	if err != nil {
		t.Fatalf("db.New(dbCfg): %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool, aeRealMigrationsDir); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	store := skill.NewStore(pool)

	parent := &models.Skill{
		Name:    "g03ae.crossref.nollm.parent-skill",
		Title:   "G03 autoexpand cross-reference parent (no-LLM path)",
		Content: "content for the G03 autoexpand draftPersistAndCrossReference no-LLM test",
		Status:  models.SkillStatusActive,
		Kind:    models.SkillKindAtomic,
	}
	if err := store.Create(ctx, parent); err != nil {
		t.Fatalf("create parent skill: %v", err)
	}

	// No WithLLMClient option: p.llm stays nil; no network access anywhere in
	// this test. DraftSkill now returns an error (G20 anti-bluff) instead of
	// falling through to createMinimalDraft and persisting a placeholder.
	p := NewPipeline(store, nil, config.AutoExpandConfig{MaxDepth: 2, MaxNewSkillsPerRun: 5}, zap.NewNop())

	childName := "g03ae-crossref-nollm-child-skill"
	gap := Gap{
		SkillName:      parent.Name,
		MissingDepName: childName,
		SuggestedTitle: "No-LLM Child Skill",
		Reason:         "test-seeded gap for the no-LLM draftPersistAndCrossReference path",
	}

	result := &ExpansionResult{}
	_, err = p.draftPersistAndCrossReference(ctx, gap, result)
	// G20 flip: nil LLM now produces an error instead of persisting a placeholder.
	if err == nil {
		t.Fatal("G20 anti-bluff: expected an error when DraftSkill is called without an LLM client, " +
			"but draftPersistAndCrossReference succeeded (would have persisted a placeholder)")
	}

	// Anti-bluff: verify no skill was persisted for the gap.
	_, err = store.GetByName(ctx, childName)
	if err == nil {
		t.Fatal("G20 anti-bluff: no skill should have been created when DraftSkill fails, " +
			"but GetByName found a persisted row -- a placeholder was wrongly committed")
	}
}
