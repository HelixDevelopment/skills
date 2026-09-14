package autoexpand

// F1 (G03 fix-round-2 -- lands G20's type-assertion half, "Auto-expand ...
// couples to concrete `*OpenAILLM`", GAPS_AND_RISKS_REGISTER.md): DraftSkill
// previously type-asserted p.llm.(*OpenAILLM) directly and returned
// ("unsupported LLM client type") for every OTHER LLMClient implementation
// -- so a validly-configured "anthropic" (or any future non-OpenAI)
// provider could NEVER draft a skill through the LLM branch at all, even
// though NewLLMClientFromConfig already constructs an *AnthropicLLM
// correctly for that provider string (llm.go). This test proves the fix:
// DraftSkill now routes through the LLMClient interface's Generate method
// (generateSkillDraft, llm.go) and succeeds for a NON-OpenAI client
// (*AnthropicLLM, backed by a fake http.RoundTripper -- §11.4.27 sanctioned
// unit-level transport fake, zero network access), with no
// "unsupported LLM client type" error anywhere in the path.
//
// §1.1 paired mutation: restoring the p.llm.(*OpenAILLM) type assertion in
// pipeline.go's DraftSkill makes this test RED (DraftSkill would return
// "unsupported LLM client type" for the *AnthropicLLM client below,
// instead of the fake-transport-drafted skill); reverting the mutation
// restores GREEN byte-identically.
//
// Gated on SKILL_SYSTEM_TEST_DB_HOST (§11.4.3): DraftSkill's buildContext
// step resolves the gap's parent skill via the real *skill.Store (a
// concrete Postgres-backed type with no fake-able seam), so this case needs
// a real throwaway database -- exactly the same DB-gating convention
// pipeline_crossreference_test.go already established in this package. It
// does NOT need ANTHROPIC_API_KEY or any live network access: the LLM leg
// is entirely faked via SetHTTPClient, which is what makes this a "unit
// test" in the same sense pipeline_crossreference_test.go already is
// (DB-gated, LLM-free) rather than the ANTHROPIC_API_KEY-gated,
// `integration`-tagged pipeline_crossreference_integration_test.go.

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

// TestDraftSkill_NonOpenAILLMClient_Succeeds proves DraftSkill drafts
// successfully through ANY configured LLMClient, not only *OpenAILLM.
func TestDraftSkill_NonOpenAILLMClient_Succeeds(t *testing.T) {
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
		Name:    "g03ae.nonopenai.parent-skill",
		Title:   "G03 autoexpand non-OpenAI-client parent",
		Content: "content for the F1 non-OpenAI-LLMClient DraftSkill test",
		Status:  models.SkillStatusActive,
		Kind:    models.SkillKindAtomic,
	}
	if err := store.Create(ctx, parent); err != nil {
		t.Fatalf("create parent skill: %v", err)
	}

	// Canned draft, in the exact JSON shape parseSkillDraft (llm.go)
	// expects, marshaled via encoding/json (not a hand-escaped literal) so
	// the fixture stays exact and readable.
	const childName = "g03ae-nonopenai-child-skill"
	const wantTitle = "Non-OpenAI Drafted Skill"
	draftPayload, err := json.Marshal(map[string]interface{}{
		"name":        childName,
		"version":     "0.1.0",
		"title":       wantTitle,
		"description": "A skill drafted via a non-OpenAI LLMClient (AnthropicLLM, fake transport).",
		"content":     "# Non-OpenAI Drafted Skill\n\nGenuine LLM-shaped content from the fake transport.",
		"metadata": map[string]interface{}{
			"tags":       []string{"go", "concurrency"},
			"domain":     "software-engineering",
			"complexity": "intermediate",
		},
		"resources": []interface{}{},
	})
	if err != nil {
		t.Fatalf("marshal canned draft payload: %v", err)
	}

	// anthropicMessageResponse / anthropicContentBlock are package-private
	// types defined in llm.go; building the fake response via them (rather
	// than a hand-written JSON string) keeps this fixture in lockstep with
	// the real Anthropic Messages API response shape AnthropicLLM.Generate
	// parses.
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

	// roundTripFunc / stubResponse are defined in llm_anthropic_test.go
	// (same package) -- the existing §11.4.27 sanctioned fake-transport
	// helpers for this package's Anthropic-client tests.
	llmClient := NewAnthropicLLM("test-key", "claude-opus-4-8", zap.NewNop())
	llmClient.SetHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return stubResponse(http.StatusOK, string(anthropicResp)), nil
	})})

	p := NewPipeline(store, nil, config.AutoExpandConfig{MaxDepth: 2, MaxNewSkillsPerRun: 5}, zap.NewNop(), WithLLMClient(llmClient))

	gap := Gap{
		SkillName:      parent.Name,
		MissingDepName: childName,
		SuggestedTitle: childName,
		Reason:         "test-seeded gap for the non-OpenAI LLMClient DraftSkill path",
	}

	draft, resources, err := p.DraftSkill(ctx, gap)
	if err != nil {
		t.Fatalf("DraftSkill with a non-OpenAI LLMClient (*AnthropicLLM) returned an error: %v "+
			"(the pre-fix type assertion p.llm.(*OpenAILLM) would return "+
			"\"unsupported LLM client type\" here)", err)
	}
	if draft == nil {
		t.Fatal("DraftSkill returned a nil draft with a nil error")
	}

	// Proves the LLM branch actually ran (not the createMinimalDraft
	// no-LLM fallback, which would produce the boilerplate "auto-generated
	// to fill a gap" template instead of this fixture's content).
	if draft.Title != wantTitle {
		t.Errorf("draft.Title = %q, want the fake-transport-drafted title %q "+
			"(got the no-LLM fallback instead of the LLM-drafted content)",
			draft.Title, wantTitle)
	}
	if draft.Name != childName {
		t.Errorf("draft.Name = %q, want %q", draft.Name, childName)
	}
	if len(resources) != 0 {
		t.Errorf("resources = %v, want empty (fixture declared zero resources)", resources)
	}
}
