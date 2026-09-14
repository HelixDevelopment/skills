package autoexpand

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/HelixDevelopment/skills/internal/config"
	"go.uber.org/zap"
)

// roundTripFunc lets a test supply a fake http.RoundTripper inline so the
// Anthropic client is exercised with zero network access (§11.4.27 sanctioned
// unit-level transport fake).
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func stubResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// ---------------------------------------------------------------------------
// Factory dispatch (tests 1-5)
// ---------------------------------------------------------------------------

// Test 1 (mutation M1 target): the anthropic branch must return a distinct
// *AnthropicLLM, never a mislabeled OpenAI client.
func TestNewLLMClientFromConfig_Anthropic_ReturnsAnthropicLLM(t *testing.T) {
	client, err := NewLLMClientFromConfig(config.AutoExpandConfig{
		LLMProvider: "anthropic",
		LLMModel:    "claude-opus-4-8",
		LLMAPIKey:   "test-key",
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ac, ok := client.(*AnthropicLLM)
	if !ok {
		t.Fatalf("expected *AnthropicLLM, got %T", client)
	}
	if ac.model != "claude-opus-4-8" {
		t.Errorf("model = %q, want claude-opus-4-8", ac.model)
	}
	if ac.apiKey != "test-key" {
		t.Errorf("apiKey not propagated to *AnthropicLLM")
	}
}

func TestNewLLMClientFromConfig_Openai_StillWorks(t *testing.T) {
	client, err := NewLLMClientFromConfig(config.AutoExpandConfig{
		LLMProvider: "openai",
		LLMModel:    "gpt-4o-mini",
		LLMAPIKey:   "test-key",
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := client.(*OpenAILLM); !ok {
		t.Fatalf("expected *OpenAILLM, got %T", client)
	}
}

func TestNewLLMClientFromConfig_LocalOrHelixLLM_UsesConfiguredBaseURL(t *testing.T) {
	for _, provider := range []string{"local", "helixllm"} {
		provider := provider
		t.Run(provider, func(t *testing.T) {
			client, err := NewLLMClientFromConfig(config.AutoExpandConfig{
				LLMProvider: provider,
				LLMModel:    "some-model",
				LLMBaseURL:  "http://127.0.0.1:8443/v1",
			}, zap.NewNop())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			oc, ok := client.(*OpenAILLM)
			if !ok {
				t.Fatalf("expected *OpenAILLM, got %T", client)
			}
			if oc.baseURL != "http://127.0.0.1:8443/v1" {
				t.Errorf("baseURL = %q, want the configured value", oc.baseURL)
			}
		})
	}
}

func TestNewLLMClientFromConfig_LocalWithoutBaseURL_FailsClosed(t *testing.T) {
	client, err := NewLLMClientFromConfig(config.AutoExpandConfig{
		LLMProvider: "local",
	}, zap.NewNop())
	if err == nil {
		t.Fatalf("expected an error for local provider without llm_base_url")
	}
	if client != nil {
		t.Errorf("expected nil client on fail-closed, got %T", client)
	}
}

func TestNewLLMClientFromConfig_UnknownProvider_FailsClosed(t *testing.T) {
	client, err := NewLLMClientFromConfig(config.AutoExpandConfig{
		LLMProvider: "bogus",
	}, zap.NewNop())
	if err == nil {
		t.Fatalf("expected an error for an unknown provider")
	}
	if client != nil {
		t.Errorf("expected nil client on fail-closed, got %T", client)
	}
}

// ---------------------------------------------------------------------------
// AnthropicLLM.Generate (tests 6-9)
// ---------------------------------------------------------------------------

// Test 6: request/response field + header mapping, and the §2.1 correctness
// point -- NO temperature/top_p/top_k may ever be marshaled into the body.
func TestAnthropicLLM_Generate_MapsRequestFieldsAndHeaders(t *testing.T) {
	var captured *http.Request
	var capturedBody []byte
	client := NewAnthropicLLM("secret-key", "claude-opus-4-8", zap.NewNop())
	client.SetHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		captured = r
		capturedBody, _ = io.ReadAll(r.Body)
		return stubResponse(http.StatusOK,
			`{"type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"hi"}]}`), nil
	})})

	if _, err := client.Generate(context.Background(), "hello", 512); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if captured.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", captured.Method)
	}
	if !strings.HasSuffix(captured.URL.Path, "/v1/messages") {
		t.Errorf("path = %q, want suffix /v1/messages", captured.URL.Path)
	}
	if got := captured.Header.Get("x-api-key"); got != "secret-key" {
		t.Errorf("x-api-key = %q, want secret-key", got)
	}
	if got := captured.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want 2023-06-01", got)
	}
	if got := captured.Header.Get("content-type"); got != "application/json" {
		t.Errorf("content-type = %q, want application/json", got)
	}

	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if body["model"] != "claude-opus-4-8" {
		t.Errorf("model field = %v, want claude-opus-4-8", body["model"])
	}
	if body["max_tokens"] != float64(512) {
		t.Errorf("max_tokens field = %v, want 512", body["max_tokens"])
	}
	if _, ok := body["messages"]; !ok {
		t.Errorf("messages field missing from request body")
	}
	for _, k := range []string{"temperature", "top_p", "top_k"} {
		if _, present := body[k]; present {
			t.Errorf("sampling param %q must NOT be sent to the Anthropic Messages API", k)
		}
	}
}

func TestAnthropicLLM_Generate_ParsesTextContent(t *testing.T) {
	client := NewAnthropicLLM("k", "m", zap.NewNop())
	client.SetHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return stubResponse(http.StatusOK,
			`{"type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"part one "},{"type":"text","text":"part two"}]}`), nil
	})})
	out, err := client.Generate(context.Background(), "p", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "part one part two" {
		t.Errorf("Generate = %q, want the joined text content", out)
	}
}

func TestAnthropicLLM_Generate_NonOKStatus_ReturnsWrappedError(t *testing.T) {
	client := NewAnthropicLLM("k", "m", zap.NewNop())
	client.SetHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp := stubResponse(http.StatusUnauthorized,
			`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"},"request_id":"req_123"}`)
		resp.Header.Set("request-id", "req_123")
		return resp, nil
	})})
	out, err := client.Generate(context.Background(), "p", 100)
	if err == nil {
		t.Fatalf("expected an error on HTTP 401")
	}
	if out != "" {
		t.Errorf("expected empty output on error, got %q", out)
	}
	for _, want := range []string{"authentication_error", "invalid x-api-key"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should contain %q", err.Error(), want)
		}
	}
}

// Test 9 (mutation M2 target): a refusal is HTTP 200 with no usable content;
// Generate MUST return ("", non-nil error), never ("", nil).
func TestAnthropicLLM_Generate_RefusalStopReason_ReturnsError(t *testing.T) {
	client := NewAnthropicLLM("k", "m", zap.NewNop())
	client.SetHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return stubResponse(http.StatusOK,
			`{"type":"message","role":"assistant","stop_reason":"refusal","content":[],"stop_details":{"type":"refusal","category":"cyber","explanation":"declined"}}`), nil
	})})
	out, err := client.Generate(context.Background(), "p", 100)
	if err == nil {
		t.Fatalf("expected an error on refusal; a silent (%q, nil) empty-success is forbidden", out)
	}
	if out != "" {
		t.Errorf("expected empty output on refusal, got %q", out)
	}
	// Assert the refusal-BRANCH message specifically (not merely any error), so
	// deleting the refusal check genuinely flips this test RED -- otherwise the
	// empty-content fallback guard (which also mentions stop_reason=refusal)
	// would keep it green and the §1.1 mutation would not be load-bearing.
	if !strings.Contains(err.Error(), "refused the request") {
		t.Errorf("error %q should be the refusal-branch error", err.Error())
	}
	if !strings.Contains(err.Error(), "cyber") {
		t.Errorf("error %q should cite the refusal category", err.Error())
	}
}
