package autoexpand

// G20 auto-expand improvements — anti-bluff and resource-persistence tests.
//
// TestDraftSkill_NilLLM_ReturnsError proves the nil-LLM guard fires before
// buildContext touches the store, so this case does NOT require a live
// database (§11.4.27: pure unit test, zero network or Postgres).
//
// TestDraftSkill_ResourcesPersisted proves that resources returned by the LLM
// backend are persisted via BulkAddResources. This case requires a live
// database (§11.4.3 DB-gated) but no LLM credential: the LLM leg is faked
// via a transport-level stub (the same roundTripFunc/stubResponse pattern
// llm_anthropic_test.go already sanctions for this package).

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/HelixDevelopment/skills/internal/skill"
	"go.uber.org/zap"
)

// TestDraftSkill_NilLLM_ReturnsError proves that DraftSkill refuses to create
// a placeholder when no LLM client is configured. Because the nil-LLM check
// fires before buildContext touches the store, a nil store is sufficient — no
// database is needed.
func TestDraftSkill_NilLLM_ReturnsError(t *testing.T) {
	// Pipeline with nil store and nil llm — the nil-LLM guard in DraftSkill
	// fires before any store access, so no DB is needed.
	p := NewPipeline(nil, nil, config.AutoExpandConfig{}, zap.NewNop())

	gap := Gap{
		SkillName:      "some-parent",
		MissingDepName: "missing-child",
		SuggestedTitle: "Missing Child",
		Reason:         "test-only gap for the nil-LLM anti-bluff guard",
	}

	draft, resources, err := p.DraftSkill(context.Background(), gap)
	if err == nil {
		t.Fatal("G20 anti-bluff: expected an error when DraftSkill is called without an LLM client, " +
			"but got nil error (would have created a placeholder)")
	}
	if draft != nil {
		t.Errorf("G20 anti-bluff: draft should be nil on error, got %+v", draft)
	}
	if resources != nil {
		t.Errorf("G20 anti-bluff: resources should be nil on error, got %+v", resources)
	}
}

// TestDraftSkill_ResourcesPersisted proves that resources returned by the LLM
// backend are persisted via BulkAddResources. Requires a live PostgreSQL
// database (SKILL_SYSTEM_TEST_DB_HOST) but no LLM credential — the LLM leg is
// faked via a transport-level stub.
func TestDraftSkill_ResourcesPersisted(t *testing.T) {
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

	// Create a parent skill so buildContext succeeds.
	parent := &models.Skill{
		Name:    "g20ae.resources.parent-skill",
		Title:   "G20 autoexpand resources parent",
		Content: "content for the G20 resource-persistence test",
		Status:  models.SkillStatusActive,
		Kind:    models.SkillKindAtomic,
	}
	if err := store.Create(ctx, parent); err != nil {
		t.Fatalf("create parent skill: %v", err)
	}

	// Canned draft with two resources, matching the JSON shape
	// parseSkillDraft (llm.go) expects.
	const childName = "g20ae-resources-child-skill"
	const wantResourceURL1 = "https://example.com/g20-resource-1"
	const wantResourceURL2 = "https://example.com/g20-resource-2"
	draftPayload, err := json.Marshal(map[string]interface{}{
		"name":        childName,
		"version":     "0.1.0",
		"title":       "G20 Resources Child Skill",
		"description": "A skill drafted to test BulkAddResources persistence.",
		"content":     "# G20 Resources Child Skill\n\nContent for the resource-persistence test.",
		"metadata": map[string]interface{}{
			"tags":       []string{"g20", "resources"},
			"domain":     "software-engineering",
			"complexity": "intermediate",
		},
		"resources": []interface{}{
			map[string]interface{}{
				"url":           wantResourceURL1,
				"title":         "G20 Resource One",
				"resource_type": "documentation",
			},
			map[string]interface{}{
				"url":           wantResourceURL2,
				"title":         "G20 Resource Two",
				"resource_type": "tutorial",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal canned draft payload: %v", err)
	}

	// Build a fake Anthropic transport that returns the canned draft.
	anthropicResp, err := json.Marshal(anthropicMessageResponse{
		Type:       "message",
		Role:       "assistant",
		StopReason: "end_turn",
		Content: []anthropicContentBlock{
			{Type: "text", Text: string(draftPayload)},
		},
	})
	if err != nil {
		t.Fatalf("marshal fake anthropic response: %v", err)
	}

	llmClient := NewAnthropicLLM("test-key", "claude-opus-4-8", zap.NewNop())
	llmClient.SetHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return stubResponse(http.StatusOK, string(anthropicResp)), nil
	})})

	p := NewPipeline(store, nil,
		config.AutoExpandConfig{MaxDepth: 2, MaxNewSkillsPerRun: 5},
		zap.NewNop(),
		WithLLMClient(llmClient),
	)

	gap := Gap{
		SkillName:      parent.Name,
		MissingDepName: childName,
		SuggestedTitle: childName,
		Reason:         "test-seeded gap for the G20 resource-persistence path",
	}

	result := &ExpansionResult{}
	draft, err := p.draftPersistAndCrossReference(ctx, gap, result)
	if err != nil {
		t.Fatalf("draftPersistAndCrossReference: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("draftPersistAndCrossReference reported errors: %v", result.Errors)
	}

	// Verify the skill was persisted.
	reread, err := store.GetByName(ctx, childName)
	if err != nil {
		t.Fatalf("GetByName(%q) after draftPersistAndCrossReference: %v", childName, err)
	}
	if reread.ID != draft.ID {
		t.Fatalf("re-read skill id = %s, want %s", reread.ID, draft.ID)
	}

	// Verify resources were persisted via BulkAddResources.
	resources, err := store.GetResources(ctx, draft.ID)
	if err != nil {
		t.Fatalf("GetResources(%s): %v", draft.ID, err)
	}
	if len(resources) != 2 {
		t.Fatalf("expected 2 persisted resources, got %d: %+v", len(resources), resources)
	}

	// Check that both expected resource URLs are present.
	foundURLs := make(map[string]bool)
	for _, r := range resources {
		foundURLs[r.URL] = true
		if r.SkillID != draft.ID {
			t.Errorf("resource %q has SkillID = %s, want %s", r.URL, r.SkillID, draft.ID)
		}
	}
	if !foundURLs[wantResourceURL1] {
		t.Errorf("expected resource URL %q not found among persisted resources", wantResourceURL1)
	}
	if !foundURLs[wantResourceURL2] {
		t.Errorf("expected resource URL %q not found among persisted resources", wantResourceURL2)
	}
}
