package autoexpand

// G03 fix-round-3 (Fable-xhigh re-review Finding 4, §11.4.124): the exported
// `OpenAILLM.GenerateSkillDraft` (llm.go) had ZERO callers anywhere in the
// codebase -- production or test -- after the F1 delegation refactor moved
// `DraftSkill` (pipeline.go) onto the provider-agnostic `generateSkillDraft`
// package function instead. Per §11.4.124 (investigate-before-remove), a
// zero-caller EXPORTED method is not proof it is genuinely unneeded --
// `OpenAILLM.GenerateSkillDraft` is still part of this package's public API
// surface (any external caller programming directly against `*OpenAILLM`,
// rather than the `LLMClient` interface, can still call it) and its own doc
// comment (llm.go) explicitly says it "delegates to generateSkillDraft ...
// the provider-agnostic drafting path". The correct disposition is to WIRE
// IT IN (here: cover it with a direct unit test), not delete it.
//
// This test exercises the exported method directly (not via DraftSkill /
// generateSkillDraft indirectly through some other type, as
// pipeline_draftskill_nonopenai_test.go does for *AnthropicLLM), with a fake
// http.RoundTripper standing in for the OpenAI chat-completions endpoint
// (§11.4.27 sanctioned unit-level transport fake, zero network access) --
// proving GenerateSkillDraft returns a genuinely-parsed draft (title,
// version, description, content, metadata, resources), not just "no error".
//
// §1.1 paired mutation: breaking generateSkillDraft's parse step (e.g.
// dropping the resources loop, or no longer populating Metadata) in llm.go
// makes this test RED -- reverting restores GREEN byte-identically. The
// companion assertions that pin the resources/metadata parse specifically
// are the resources/metadata checks in
// TestOpenAILLM_GenerateSkillDraft_ReturnsParsedDraft below (the
// `gotMeta.Domain`/`gotMeta.Complexity` check and the
// `len(gotMeta.Tags) != 2` check for metadata, the `len(resources) != 1`
// check for resources): dropping the resources loop turns the
// `len(resources) != 1` check RED; no longer populating Metadata turns the
// `gotMeta.Domain`/`gotMeta.Complexity` check (or the
// `len(gotMeta.Tags) != 2` check) RED.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// buildOpenAIChatCompletionResponse wraps assistantContent (the JSON draft
// payload parseSkillDraft, llm.go, expects) into a realistic OpenAI
// chat-completions response envelope. Built via a plain map + json.Marshal
// (matching llm_anthropic_test.go's own raw-JSON-body convention in this
// package) rather than openAIChatResponse (llm.go) directly, since that
// type's Choices field is an unexported anonymous-struct slice not
// constructible from outside its declaring statement.
func buildOpenAIChatCompletionResponse(t *testing.T, assistantContent string) string {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{
		"id":      "chatcmpl-g03-fixround3-test",
		"object":  "chat.completion",
		"created": 1700000000,
		"model":   "gpt-4o-mini",
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": assistantContent,
				},
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     42,
			"completion_tokens": 128,
			"total_tokens":      170,
		},
	})
	if err != nil {
		t.Fatalf("marshal fake OpenAI chat-completion response: %v", err)
	}
	return string(body)
}

// TestOpenAILLM_GenerateSkillDraft_ReturnsParsedDraft proves the exported
// OpenAILLM.GenerateSkillDraft (llm.go) -- via its delegation to
// generateSkillDraft, the provider-agnostic drafting path DraftSkill
// (pipeline.go) itself calls -- genuinely drafts + parses a skill from a
// realistic fake OpenAI response, with no network access.
func TestOpenAILLM_GenerateSkillDraft_ReturnsParsedDraft(t *testing.T) {
	const (
		skillName   = "g03ae.fixround3.generateskilldraft-child"
		wantTitle   = "OpenAI GenerateSkillDraft Test Skill"
		wantVersion = "1.2.3"
		wantDesc    = "A skill drafted directly through OpenAILLM.GenerateSkillDraft."
		wantContent = "# OpenAI GenerateSkillDraft Test Skill\n\nGenuine parsed content from the fake transport."
	)

	draftPayload, err := json.Marshal(map[string]interface{}{
		"name":        skillName,
		"version":     wantVersion,
		"title":       wantTitle,
		"description": wantDesc,
		"content":     wantContent,
		"metadata": map[string]interface{}{
			"tags":       []string{"go", "http"},
			"domain":     "software-engineering",
			"complexity": "beginner",
		},
		"resources": []map[string]interface{}{
			{"url": "https://go.dev/doc/effective_go", "title": "Effective Go", "resource_type": "official-doc"},
		},
	})
	if err != nil {
		t.Fatalf("marshal canned draft payload: %v", err)
	}

	var capturedReq *http.Request
	var capturedBody []byte
	client := NewOpenAILLM("test-api-key", "gpt-4o-mini", zap.NewNop())
	client.SetHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		capturedReq = r
		capturedBody, _ = io.ReadAll(r.Body)
		return stubResponse(http.StatusOK, buildOpenAIChatCompletionResponse(t, string(draftPayload))), nil
	})})

	draft, resources, err := client.GenerateSkillDraft(context.Background(), skillName, "existing sibling skill context")
	if err != nil {
		t.Fatalf("GenerateSkillDraft returned an unexpected error: %v", err)
	}
	if draft == nil {
		t.Fatal("GenerateSkillDraft returned a nil draft with a nil error")
	}

	// --- Request mapping (proves the call actually reached the OpenAI leg) ---
	if capturedReq == nil {
		t.Fatal("no HTTP request was captured; GenerateSkillDraft did not call Generate")
	}
	if capturedReq.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", capturedReq.Method)
	}
	if !strings.HasSuffix(capturedReq.URL.Path, "/chat/completions") {
		t.Errorf("path = %q, want suffix /chat/completions", capturedReq.URL.Path)
	}
	if got := capturedReq.Header.Get("Authorization"); got != "Bearer test-api-key" {
		t.Errorf("Authorization header = %q, want %q", got, "Bearer test-api-key")
	}
	var reqBody map[string]interface{}
	if err := json.Unmarshal(capturedBody, &reqBody); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if reqBody["model"] != "gpt-4o-mini" {
		t.Errorf("request model = %v, want gpt-4o-mini", reqBody["model"])
	}

	// --- Parsed-draft assertions (proves generateSkillDraft's parse, not ---
	// --- merely "no error") ---
	if draft.Name != skillName {
		t.Errorf("draft.Name = %q, want %q (parseSkillDraft names the draft "+
			"from its skillName argument, not the response's own \"name\" field)",
			draft.Name, skillName)
	}
	if draft.Title != wantTitle {
		t.Errorf("draft.Title = %q, want %q", draft.Title, wantTitle)
	}
	if draft.Version != wantVersion {
		t.Errorf("draft.Version = %q, want %q", draft.Version, wantVersion)
	}
	if draft.Description != wantDesc {
		t.Errorf("draft.Description = %q, want %q", draft.Description, wantDesc)
	}
	if draft.Content != wantContent {
		t.Errorf("draft.Content = %q, want %q", draft.Content, wantContent)
	}
	if string(draft.Status) != "draft" {
		t.Errorf("draft.Status = %q, want %q", draft.Status, "draft")
	}

	var gotMeta struct {
		Tags       []string `json:"tags"`
		Domain     string   `json:"domain"`
		Complexity string   `json:"complexity"`
	}
	if err := json.Unmarshal(draft.Metadata, &gotMeta); err != nil {
		t.Fatalf("draft.Metadata is not valid JSON: %v", err)
	}
	if gotMeta.Domain != "software-engineering" || gotMeta.Complexity != "beginner" {
		t.Errorf("draft.Metadata domain/complexity = %+v, want domain=software-engineering complexity=beginner", gotMeta)
	}
	if len(gotMeta.Tags) != 2 || gotMeta.Tags[0] != "go" || gotMeta.Tags[1] != "http" {
		t.Errorf("draft.Metadata tags = %v, want [go http]", gotMeta.Tags)
	}

	if len(resources) != 1 {
		t.Fatalf("resources = %v, want exactly 1", resources)
	}
	if resources[0].URL != "https://go.dev/doc/effective_go" {
		t.Errorf("resources[0].URL = %q, want the fixture URL", resources[0].URL)
	}
	if resources[0].Title != "Effective Go" {
		t.Errorf("resources[0].Title = %q, want %q", resources[0].Title, "Effective Go")
	}
	if resources[0].ResourceType != "official-doc" {
		t.Errorf("resources[0].ResourceType = %q, want %q", resources[0].ResourceType, "official-doc")
	}
}

// TestOpenAILLM_GenerateSkillDraft_MalformedResponse_ReturnsError proves
// GenerateSkillDraft propagates a parse failure as an error rather than
// silently returning a zero-value draft -- the negative case that makes the
// positive parse assertions above load-bearing (a GenerateSkillDraft that
// always returned (nil, nil, someErr) regardless of input would still pass a
// suite with only a happy-path case).
func TestOpenAILLM_GenerateSkillDraft_MalformedResponse_ReturnsError(t *testing.T) {
	client := NewOpenAILLM("test-api-key", "gpt-4o-mini", zap.NewNop())
	client.SetHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return stubResponse(http.StatusOK, buildOpenAIChatCompletionResponse(t, "not valid json at all")), nil
	})})

	draft, resources, err := client.GenerateSkillDraft(context.Background(), "g03ae.fixround3.malformed", "ctx")
	if err == nil {
		t.Fatalf("expected an error for a non-JSON draft payload, got draft=%+v resources=%v", draft, resources)
	}
	if draft != nil {
		t.Errorf("expected a nil draft on parse failure, got %+v", draft)
	}
	if resources != nil {
		t.Errorf("expected nil resources on parse failure, got %v", resources)
	}
}
