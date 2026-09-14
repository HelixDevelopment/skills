//go:build integration

package autoexpand

// G03 remediation (the autoexpand+worker half) -- live proof, with a REAL
// Anthropic Messages API call AND a REAL PostgreSQL database, that
// draftPersistAndCrossReference (pipeline.go) genuinely: (1) expands via
// the configured LLM provider (not the no-LLM minimal-fallback draft), (2)
// persists the resulting sub-skill, and (3) cross-references it into the
// tree by adding a `requires` edge from the gap's parent skill -- resolved
// via the EXACT Store.GetByName lookup (§G29/§G60), never the fuzzy
// Store.Search -- to the newly created skill.
//
// Why this test constructs its Gap manually rather than driving it through
// Pipeline.Run's own top-level gap scan: confirmed FACT (§11.4.6, by direct
// inspection of store.go + migrations/001_initial.up.sql, and independently
// reproduced against a live throwaway database before writing this file,
// §11.4.199), Pipeline.Run's gap-detection (DetectGapsForSkill ->
// collectGapsFromTree / detectGapsForSingleSkill, both keyed on
// `dep.DependsOn == uuid.Nil`) can never fire for any graph the store API
// constructs: skill_dependencies.depends_on is a NOT-nullable foreign key
// (REFERENCES skills(id) ON DELETE CASCADE) and Store.GetByName's
// dependency-loading query INNER JOINs skills on it, so every populated
// models.SkillDependency.DependsOn is, by construction, a real, existing
// skill id -- Postgres itself refuses an INSERT that would make it
// otherwise. That is a real, separate, out-of-scope defect in the
// gap-DETECTION half of this package (reported alongside this change, not
// fixed here); it does not change what draftPersistAndCrossReference itself
// -- the "expand via the LLM provider -> persist -> cross-reference"
// machinery this ticket adds -- is supposed to do once a Gap IS found. This
// test exercises that machinery directly and honestly, with real network +
// real database evidence, rather than asserting something the unmodified
// detection code cannot produce (worker package's own
// autoexpand_integration_test.go documents the identical finding for the
// worker-dispatch half).
//
// Scoping the LLM boundary (§11.4.3, following llm_anthropic_integration_
// test.go's EXACT existing convention for this package): SKIPs with a
// reason -- never a fake PASS -- when ANTHROPIC_API_KEY is unset. Live LLM
// proof is operator-scheduled. Also SKIPs when SKILL_SYSTEM_TEST_DB_HOST is
// unset (this package's sibling packages, e.g. internal/worker and
// internal/db, establish that same DB-gating convention; this package had
// no prior DB-backed test, so this file also carries the minimal
// package-local throwaway-DB helper those packages each already duplicate,
// per testdb_helper_test.go's own documented rationale for why it is not
// shared/exported).
//
// This test's very assertion that draft.Content is NOT the createMinimalDraft
// fallback template ("This skill was auto-generated to fill a gap ...") is
// itself a citation of the fallback's CURRENT behavior: that no-LLM
// minimal-draft fallback is slated for removal per G20
// (never-persist-a-placeholder, GAPS_AND_RISKS_REGISTER.md) -- this ticket
// lands only G20's *OpenAILLM type-assertion half (see generateSkillDraft,
// llm.go), not the fallback-removal half. Once G20's fallback removal
// lands, this negative assertion (content != the fallback template) is
// expected to remain true for a different reason (the fallback template
// text no longer exists at all), so it should not need to change; the
// sibling no-LLM test (pipeline_crossreference_test.go) is the one whose
// assertions are expected to flip when that lands.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/HelixDevelopment/skills/internal/skill"
	"go.uber.org/zap"
)

// TestIntegration_DraftPersistAndCrossReference_LiveLLMAndDatabase drives
// the real create+cross-reference machinery end-to-end: a real Anthropic
// Generate call drafts the sub-skill, Store.Create persists it, and
// Store.GetByName + Store.AddDependency cross-reference it into the parent
// skill's dependency edge.
func TestIntegration_DraftPersistAndCrossReference_LiveLLMAndDatabase(t *testing.T) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("ANTHROPIC_API_KEY not set; skipping live Anthropic-backed autoexpand test (§11.4.3 SKIP-with-reason)")
	}
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
		Name:    "g03ae.crossref.parent-skill",
		Title:   "G03 autoexpand cross-reference parent",
		Content: "content for the G03 autoexpand draftPersistAndCrossReference integration test",
		Status:  models.SkillStatusActive,
		Kind:    models.SkillKindAtomic,
	}
	if err := store.Create(ctx, parent); err != nil {
		t.Fatalf("create parent skill: %v", err)
	}

	logger := zap.NewNop()
	llmClient := NewAnthropicLLM(key, "", logger)
	p := NewPipeline(store, nil, config.AutoExpandConfig{MaxDepth: 2, MaxNewSkillsPerRun: 5}, logger, WithLLMClient(llmClient))

	childName := "g03ae-crossref-child-skill"
	gap := Gap{
		SkillName:      parent.Name,
		MissingDepName: childName,
		SuggestedTitle: childName,
		Reason:         fmt.Sprintf("Skill %q depends on %q which does not exist", parent.Name, childName),
	}

	result := &ExpansionResult{}
	draft, err := p.draftPersistAndCrossReference(ctx, gap, result)
	if err != nil {
		t.Fatalf("draftPersistAndCrossReference: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("draftPersistAndCrossReference reported errors: %v", result.Errors)
	}

	// (1) EXPAND VIA THE LLM PROVIDER: the created skill's content must be
	// genuine LLM-drafted prose, not the createMinimalDraft fallback
	// template ("This skill was auto-generated to fill a gap in the
	// knowledge graph."). A live LLM call producing real, non-trivial
	// content is the positive evidence the LLM branch -- not the fallback --
	// actually executed.
	if strings.Contains(draft.Content, "This skill was auto-generated to fill a gap in the knowledge graph") {
		t.Fatalf("draft.Content matches the no-LLM createMinimalDraft fallback template; "+
			"the LLM-backed drafting branch did not run (content=%q)", draft.Content)
	}
	if len(draft.Content) < 100 {
		t.Errorf("draft.Content is only %d bytes, too short for genuine LLM-generated skill content: %q",
			len(draft.Content), draft.Content)
	}
	if draft.Title == "" {
		t.Error("draft.Title is empty; expected a real LLM-generated title")
	}
	t.Logf("live LLM-drafted skill: name=%q title=%q content_len=%d", draft.Name, draft.Title, len(draft.Content))

	// (2) PERSIST: the sub-skill must be a real row, independently
	// re-readable from the database (not just an in-memory struct).
	reread, err := store.GetByName(ctx, childName)
	if err != nil {
		t.Fatalf("GetByName(%q) after draftPersistAndCrossReference: %v", childName, err)
	}
	if reread.ID != draft.ID {
		t.Fatalf("re-read skill id = %s, want %s (the exact skill draftPersistAndCrossReference created)", reread.ID, draft.ID)
	}

	// (3) CROSS-REFERENCE INTO THE TREE: the parent, re-resolved via the
	// EXACT Store.GetByName lookup (never Store.Search), must now list the
	// new skill as a `requires` dependency.
	parentReread, err := store.GetByName(ctx, parent.Name)
	if err != nil {
		t.Fatalf("GetByName(%q) (parent) after cross-reference: %v", parent.Name, err)
	}
	found := false
	for _, dep := range parentReread.Dependencies {
		if dep.DependsOn == draft.ID && dep.DependsOnName == childName {
			found = true
			if dep.RelationType != models.DepTypeRequires {
				t.Errorf("cross-referenced edge relation_type = %q, want %q", dep.RelationType, models.DepTypeRequires)
			}
			break
		}
	}
	if !found {
		names := make([]string, 0, len(parentReread.Dependencies))
		for _, d := range parentReread.Dependencies {
			names = append(names, d.DependsOnName)
		}
		t.Fatalf("parent skill %q has no dependency edge to the newly created %q after "+
			"draftPersistAndCrossReference; got dependencies=%v -- the new sub-skill was persisted "+
			"but never cross-referenced into the tree", parent.Name, childName, names)
	}
}
