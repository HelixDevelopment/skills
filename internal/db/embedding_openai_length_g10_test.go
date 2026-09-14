package db

// G10 — OpenAI per-embed response-length guard
// (research/g10_embedding_provider_design.md §2.5, case 5/6; GAPS_AND_RISKS_REGISTER.md
// §G10 evidence §1.3). Prior to this fix, OpenAIEmbedder.Embed validated the
// *count* of returned rows and each row's *index* but never the *length* of any
// returned embedding, unlike LocalEmbedder (embedding.go:256-262), which already
// performs exactly this check. A provider/model that returns the wrong width
// (e.g. a model/proxy that does not honor the "dimensions" request parameter --
// see embedding.go's UNCONFIRMED note on text-embedding-ada-002,
// §11.4.6/§11.4.99) would hand a wrong-length vector straight through to a
// vector(N) insert, surfacing as an opaque pgvector error far from this call.
//
// These are the §11.4.115 RED-first regression guards for that fix, driven with
// a zero-network fake http.RoundTripper (the sanctioned unit-level transport
// double, §11.4.27 -- identical pattern to internal/autoexpand/llm_anthropic_test.go's
// roundTripFunc/stubResponse helpers). RED baseline: the pre-fix Embed body has
// no length check at all, so TestOpenAIEmbedder_Embed_RejectsWrongLengthVector
// FAILs (Embed returns the wrong-length vector with a nil error instead of an
// error). GREEN post-fix. A §1.1 mutation deleting the added
// `if len(d.Embedding) != e.dimensions { ... }` guard reproduces the RED case.
//
// The guard's real-world value does not depend on any one model's exact
// behavior: it is defense-in-depth against ANY response whose embedding
// length does not match what was configured -- an OpenAI-COMPATIBLE
// gateway/proxy that ignores or only partially honors the "dimensions"
// request parameter, or a provider-side regression that silently changes a
// model's output width. This test's fixture (respLen=1536 against a
// wantDim=768 configuration) is chosen as a plausible wrong-length response
// shape, not as an assertion about any specific model's documented behavior
// (see embedding.go's guard comment for the honest, explicitly-marked
// UNCONFIRMED boundary on the ada-002-specific claim, §11.4.6/§11.4.99).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/HelixDevelopment/skills/internal/config"
)

// g10RoundTripFunc lets a test supply a fake http.RoundTripper inline so
// OpenAIEmbedder is exercised with zero network access.
type g10RoundTripFunc func(*http.Request) (*http.Response, error)

func (f g10RoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func g10StubResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// g10OpenAIEmbedResponseBody builds a canned OpenAI /v1/embeddings response
// body carrying a single embedding of the given length at index 0.
func g10OpenAIEmbedResponseBody(t *testing.T, length int) string {
	t.Helper()
	vec := make([]float64, length)
	for i := range vec {
		vec[i] = 0.01
	}
	body := struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
			Index     int       `json:"index"`
			Object    string    `json:"object"`
		} `json:"data"`
		Model string `json:"model"`
	}{
		Model: "text-embedding-3-small",
	}
	body.Data = append(body.Data, struct {
		Embedding []float64 `json:"embedding"`
		Index     int       `json:"index"`
		Object    string    `json:"object"`
	}{Embedding: vec, Index: 0, Object: "embedding"})

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal stub OpenAI embed response: %v", err)
	}
	return string(raw)
}

// g10NewStubbedOpenAIEmbedder builds an OpenAIEmbedder wired to a fake
// transport that always returns a single embedding of respLen elements,
// configured to expect wantDim.
func g10NewStubbedOpenAIEmbedder(t *testing.T, wantDim, respLen int) *OpenAIEmbedder {
	t.Helper()
	e := NewOpenAIEmbedder(config.EmbeddingConfig{
		Provider:   "openai",
		APIKey:     "sk-test-not-a-real-key",
		Model:      "text-embedding-3-small",
		Dimensions: wantDim,
	})
	e.SetHTTPClient(&http.Client{
		Transport: g10RoundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return g10StubResponse(http.StatusOK, g10OpenAIEmbedResponseBody(t, respLen)), nil
		}),
	})
	return e
}

// TestOpenAIEmbedder_Embed_RejectsWrongLengthVector is the golden-FALSE case:
// a provider response whose embedding length does NOT match the configured
// Dimensions() MUST be rejected with a non-nil error, and the mismatched
// vector must NEVER be returned to the caller (no silent pad/truncate/accept).
func TestOpenAIEmbedder_Embed_RejectsWrongLengthVector(t *testing.T) {
	const wantDim = 768
	const gotLen = 1536 // a plausible wrong-length response; see embedding.go's UNCONFIRMED note on ada-002, §11.4.6/§11.4.99
	e := g10NewStubbedOpenAIEmbedder(t, wantDim, gotLen)

	vecs, err := e.Embed(context.Background(), []string{"probe text"})
	if err == nil {
		t.Fatalf("Embed() with a %d-length response against configured Dimensions()=%d returned (vecs=%v, nil error); "+
			"want a non-nil error -- a wrong-length vector must never be silently passed through", gotLen, wantDim, vecs)
	}
	if vecs != nil {
		t.Errorf("Embed() returned a non-nil vector slice alongside its error: %v (must not leak the mismatched vector)", vecs)
	}
	wantSubstrings := []string{fmt.Sprintf("%d", gotLen), fmt.Sprintf("%d", wantDim)}
	for _, s := range wantSubstrings {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q does not mention %q (got/expected dimension); the error should name both so a misconfiguration is diagnosable", err.Error(), s)
		}
	}
}

// TestOpenAIEmbedder_Embed_AcceptsCorrectLengthVector is the golden-TRUE
// companion: a provider response whose length matches Dimensions() must be
// accepted and returned unchanged -- the fix narrows the previously-permissive
// path, it must not break the legitimate one.
func TestOpenAIEmbedder_Embed_AcceptsCorrectLengthVector(t *testing.T) {
	const dim = 768
	e := g10NewStubbedOpenAIEmbedder(t, dim, dim)

	vecs, err := e.Embed(context.Background(), []string{"probe text"})
	if err != nil {
		t.Fatalf("Embed() with a correct-length (%d) response returned an unexpected error: %v", dim, err)
	}
	if len(vecs) != 1 {
		t.Fatalf("Embed() returned %d vectors, want 1", len(vecs))
	}
	if len(vecs[0]) != dim {
		t.Errorf("Embed() returned vector of length %d, want %d", len(vecs[0]), dim)
	}
}
