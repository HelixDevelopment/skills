// Package autoexpand provides LLM integration for skill drafting in the
// HelixKnowledge auto-growth pipeline.
package autoexpand

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/sync/semaphore"
)

// ---------------------------------------------------------------------------
// LLMClient interface
// ---------------------------------------------------------------------------

// LLMClient abstracts LLM API calls for skill generation.
type LLMClient interface {
	// Generate creates text from a prompt with a token limit.
	Generate(ctx context.Context, prompt string, maxTokens int) (string, error)
}

// NewLLMClientFromConfig builds an LLMClient from the auto-expand
// configuration, dispatching on the configured provider. It mirrors the
// embedder factory (db.NewEmbedderFromConfig) in shape and fails closed on an
// unrecognized provider string rather than silently defaulting to one.
//
// Supported providers:
//   - "openai"             -> OpenAILLM against the OpenAI API
//   - "anthropic"          -> AnthropicLLM against the Anthropic Messages API
//   - "local" / "helixllm" -> OpenAILLM against an OpenAI-compatible base URL
//     (HelixAgent / HelixLLM are OpenAI-compatible chat-completions endpoints
//     reached by a base-URL swap).
func NewLLMClientFromConfig(cfg config.AutoExpandConfig, logger *zap.Logger) (LLMClient, error) {
	switch cfg.LLMProvider {
	case "openai":
		if cfg.LLMAPIKey == "" {
			logger.Warn("openai LLM client created without API key; requests will fail")
		}
		return NewOpenAILLM(cfg.LLMAPIKey, cfg.LLMModel, logger), nil
	case "anthropic":
		if cfg.LLMAPIKey == "" {
			logger.Warn("anthropic LLM client created without API key; requests will fail")
		}
		return NewAnthropicLLM(cfg.LLMAPIKey, cfg.LLMModel, logger), nil
	case "local", "helixllm":
		if cfg.LLMBaseURL == "" {
			return nil, fmt.Errorf("llm_provider %q requires llm_base_url", cfg.LLMProvider)
		}
		client := NewOpenAILLM(cfg.LLMAPIKey, cfg.LLMModel, logger)
		client.SetBaseURL(cfg.LLMBaseURL)
		return client, nil
	default:
		return nil, fmt.Errorf("unsupported llm_provider: %q (expected "+
			"\"openai\", \"anthropic\", \"local\", or \"helixllm\")", cfg.LLMProvider)
	}
}

// ---------------------------------------------------------------------------
// OpenAI LLM implementation
// ---------------------------------------------------------------------------

// OpenAILLM implements LLMClient using the OpenAI API with rate limiting.
type OpenAILLM struct {
	apiKey     string
	model      string
	baseURL    string
	httpClient *http.Client
	logger     *zap.Logger

	// Rate limiting
	sem      *semaphore.Weighted
	lastCall time.Time
	rateMu   sync.Mutex
	rpm      int // requests per minute
}

// NewOpenAILLM creates a new OpenAI LLM client.
func NewOpenAILLM(apiKey, model string, logger *zap.Logger) *OpenAILLM {
	if model == "" {
		model = "gpt-4o-mini"
	}
	return &OpenAILLM{
		apiKey:     apiKey,
		model:      model,
		baseURL:    "https://api.openai.com/v1",
		httpClient: &http.Client{Timeout: 120 * time.Second},
		logger:     logger,
		sem:        semaphore.NewWeighted(10), // max 10 concurrent requests
		rpm:        50,                        // 50 requests per minute
	}
}

// SetBaseURL allows overriding the API base URL (for tests or proxies).
func (c *OpenAILLM) SetBaseURL(url string) {
	c.baseURL = url
}

// SetHTTPClient replaces the default HTTP client.
func (c *OpenAILLM) SetHTTPClient(client *http.Client) {
	c.httpClient = client
}

// SetRateLimit sets the requests-per-minute rate limit.
func (c *OpenAILLM) SetRateLimit(rpm int) {
	c.rateMu.Lock()
	c.rpm = rpm
	c.rateMu.Unlock()
}

// ---------------------------------------------------------------------------
// Core generation
// ---------------------------------------------------------------------------

// Generate calls the OpenAI chat completions API with rate limiting.
func (c *OpenAILLM) Generate(ctx context.Context, prompt string, maxTokens int) (string, error) {
	// Acquire rate limit semaphore
	if err := c.sem.Acquire(ctx, 1); err != nil {
		return "", fmt.Errorf("rate limit acquire: %w", err)
	}
	defer c.sem.Release(1)

	// Enforce RPM rate limit
	c.rateMu.Lock()
	minInterval := time.Minute / time.Duration(max(c.rpm, 1))
	elapsed := time.Since(c.lastCall)
	if elapsed < minInterval {
		c.rateMu.Unlock()
		sleepTime := minInterval - elapsed
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(sleepTime):
		}
	} else {
		c.rateMu.Unlock()
	}

	c.rateMu.Lock()
	c.lastCall = time.Now()
	c.rateMu.Unlock()

	reqBody := openAIChatRequest{
		Model: c.model,
		Messages: []openAIMessage{
			{Role: "user", Content: prompt},
		},
		MaxTokens:   maxTokens,
		Temperature: 0.7,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("API request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10 MiB cap
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result openAIChatResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices in API response")
	}

	return result.Choices[0].Message.Content, nil
}

// ---------------------------------------------------------------------------
// Skill draft generation
// ---------------------------------------------------------------------------

// GenerateSkillDraft creates a complete skill draft using the LLM.
// It generates a skill definition with title, description, content,
// metadata, and suggested resources.
//
// This delegates to generateSkillDraft (below), the provider-agnostic
// drafting path that works against the LLMClient interface rather than
// this concrete type -- see that function's doc comment (F1/G20) for why
// DraftSkill (pipeline.go) calls generateSkillDraft directly instead of
// this method.
func (c *OpenAILLM) GenerateSkillDraft(ctx context.Context, skillName, existingContext string) (*models.Skill, []models.Resource, error) {
	c.logger.Debug("generating skill draft", zap.String("skill", skillName))

	draft, resources, err := generateSkillDraft(ctx, c, skillName, existingContext)
	if err != nil {
		return nil, nil, err
	}

	c.logger.Info("skill draft generated",
		zap.String("skill", draft.Name),
		zap.String("title", draft.Title),
		zap.Int("resources", len(resources)),
	)

	return draft, resources, nil
}

// generateSkillDraft drafts a skill via ANY LLMClient implementation: it
// builds the prompt (GeneratePrompt), invokes Generate through the
// LLMClient interface (never a concrete type), and parses the JSON
// response (parseSkillDraft).
//
// F1 (G03 fix-round-2 -- lands G20's type-assertion half, "Auto-expand ...
// couples to concrete `*OpenAILLM`", GAPS_AND_RISKS_REGISTER.md): this is
// the single provider-agnostic drafting path Pipeline.DraftSkill
// (pipeline.go) calls. DraftSkill previously type-asserted
// p.llm.(*OpenAILLM) directly and returned "unsupported LLM client type"
// for every OTHER LLMClient (*AnthropicLLM, and any future
// implementation) -- even though NewLLMClientFromConfig (above) already
// constructs those correctly for the "anthropic"/"local"/"helixllm"
// provider strings, so a validly-configured non-OpenAI worker could never
// draft a skill through the LLM branch at all. Routing through the
// interface here means every configured provider drafts successfully.
func generateSkillDraft(ctx context.Context, llm LLMClient, skillName, existingContext string) (*models.Skill, []models.Resource, error) {
	prompt := GeneratePrompt(skillName, existingContext)

	response, err := llm.Generate(ctx, prompt, 4000)
	if err != nil {
		return nil, nil, fmt.Errorf("LLM generation: %w", err)
	}

	// Parse the JSON response
	draft, resources, err := parseSkillDraft(skillName, response)
	if err != nil {
		return nil, nil, fmt.Errorf("parse draft: %w", err)
	}

	return draft, resources, nil
}

// ---------------------------------------------------------------------------
// Prompt engineering
// ---------------------------------------------------------------------------

// GeneratePrompt creates the system prompt for skill generation.
// The prompt is carefully crafted to produce structured, consistent output
// that can be parsed into skill objects.
func GeneratePrompt(skillName, existingContext string) string {
	return fmt.Sprintf(`You are a technical knowledge graph expert. Generate a skill definition for "%s".

Context from the existing knowledge graph:
%s

Generate a complete skill definition in the following JSON format:

{
  "name": "%s",
  "version": "0.1.0",
  "title": "Human-readable title",
  "description": "Clear, concise description of what this skill covers (2-3 sentences)",
  "content": "# Title\n\n## Overview\n\nDetailed markdown content covering key concepts, patterns, and best practices. Include code examples where relevant.\n\n## Key Concepts\n\n- Concept 1\n- Concept 2\n\n## Best Practices\n\n1. Practice 1\n2. Practice 2\n\n## Common Pitfalls\n\n- Pitfall 1\n- Pitfall 2\n\n## Resources\n\n- [Resource Title](URL)\n",
  "metadata": {
    "tags": ["tag1", "tag2"],
    "domain": "software-engineering",
    "complexity": "intermediate"
  },
  "resources": [
    {
      "url": "https://example.com/resource",
      "title": "Resource Title",
      "resource_type": "documentation"
    }
  ]
}

Requirements:
- Content must be accurate, specific, and actionable
- Include practical code examples where appropriate
- Follow markdown best practices
- Tags should be lowercase, hyphen-separated
- Domain should be a broad category like "software-engineering", "data-science", "devops"
- Complexity should be one of: "beginner", "intermediate", "advanced"
- Resources should be real, verifiable URLs when possible
- Do NOT fabricate URLs - leave resource array empty if you don't know valid URLs
- The content should be comprehensive enough to be genuinely useful

Respond with ONLY the JSON object, no markdown code fences or additional text.`, skillName, existingContext, skillName)
}

// ---------------------------------------------------------------------------
// Parsing
// ---------------------------------------------------------------------------

// parseSkillDraft parses the LLM JSON response into a Skill and Resources.
func parseSkillDraft(skillName, response string) (*models.Skill, []models.Resource, error) {
	// Clean up response: remove markdown code fences if present
	cleaned := response
	if idx := bytes.Index([]byte(cleaned), []byte("```json")); idx != -1 {
		start := idx + 7
		if endIdx := bytes.Index([]byte(cleaned[start:]), []byte("```")); endIdx != -1 {
			cleaned = cleaned[start : start+endIdx]
		}
	} else if idx := bytes.Index([]byte(cleaned), []byte("```")); idx != -1 {
		start := idx + 3
		if endIdx := bytes.Index([]byte(cleaned[start:]), []byte("```")); endIdx != -1 {
			cleaned = cleaned[start : start+endIdx]
		}
	}

	// Trim whitespace
	cleaned = string(bytes.TrimSpace([]byte(cleaned)))

	var raw struct {
		Name        string `json:"name"`
		Version     string `json:"version"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Content     string `json:"content"`
		Metadata    struct {
			Tags       []string `json:"tags"`
			Domain     string   `json:"domain"`
			Complexity string   `json:"complexity"`
		} `json:"metadata"`
		Resources []struct {
			URL          string `json:"url"`
			Title        string `json:"title"`
			ResourceType string `json:"resource_type"`
		} `json:"resources"`
	}

	if err := json.Unmarshal([]byte(cleaned), &raw); err != nil {
		return nil, nil, fmt.Errorf("unmarshal LLM response: %w", err)
	}

	// Build metadata JSON
	metadata := models.SkillMetadata{
		Tags:       raw.Metadata.Tags,
		Domain:     raw.Metadata.Domain,
		Complexity: raw.Metadata.Complexity,
	}
	metadataJSON, _ := json.Marshal(metadata)

	if raw.Version == "" {
		raw.Version = "0.1.0"
	}

	skill := &models.Skill{
		ID:          uuid.New(),
		Name:        skillName,
		Version:     raw.Version,
		Title:       raw.Title,
		Description: raw.Description,
		Content:     raw.Content,
		Metadata:    metadataJSON,
		Status:      models.SkillStatusDraft,
	}

	var resources []models.Resource
	for _, r := range raw.Resources {
		if r.URL == "" {
			continue // skip empty URLs
		}
		resources = append(resources, models.Resource{
			ID:           uuid.New(),
			URL:          r.URL,
			Title:        r.Title,
			ResourceType: r.ResourceType,
		})
	}

	return skill, resources, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// OpenAI API types
// ---------------------------------------------------------------------------

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature float64         `json:"temperature,omitempty"`
}

type openAIChatResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int           `json:"index"`
		Message openAIMessage `json:"message"`
		// FinishReason may be absent in some responses.
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// ---------------------------------------------------------------------------
// Anthropic LLM implementation
// ---------------------------------------------------------------------------

// anthropicAPIVersion is the stable Anthropic Messages API version string sent
// in the anthropic-version header.
const anthropicAPIVersion = "2023-06-01"

// AnthropicLLM implements LLMClient using the Anthropic Messages API
// (POST {baseURL}/v1/messages). It is a thin net/http client matching the
// house style of OpenAILLM.
//
// It deliberately omits temperature/top_p/top_k: current Claude models reject
// non-default sampling parameters with HTTP 400, so -- unlike the OpenAI client
// -- no sampling parameter is ever marshaled into the request body.
type AnthropicLLM struct {
	apiKey     string
	model      string
	baseURL    string
	httpClient *http.Client
	logger     *zap.Logger
}

// NewAnthropicLLM creates a new Anthropic Messages API client. An empty model
// defaults to the current general-purpose Claude model; it is
// operator-overridable and never a hardcoded mandate.
func NewAnthropicLLM(apiKey, model string, logger *zap.Logger) *AnthropicLLM {
	if model == "" {
		model = "claude-opus-4-8"
	}
	return &AnthropicLLM{
		apiKey:     apiKey,
		model:      model,
		baseURL:    "https://api.anthropic.com",
		httpClient: &http.Client{Timeout: 120 * time.Second},
		logger:     logger,
	}
}

// SetBaseURL allows overriding the API base URL (for tests or proxies).
func (c *AnthropicLLM) SetBaseURL(url string) {
	c.baseURL = url
}

// SetHTTPClient replaces the default HTTP client.
func (c *AnthropicLLM) SetHTTPClient(client *http.Client) {
	c.httpClient = client
}

// Generate calls the Anthropic Messages API. On ANY failure to produce genuine
// text -- HTTP error, malformed response, or a policy refusal -- it returns
// ("", non-nil error), never ("", nil). A refusal is HTTP 200 with no usable
// content: the exact "empty-but-successful" shape the auto-growth pipeline
// forbids, so this client fails closed at the provider layer rather than
// trusting every future caller to re-check it.
func (c *AnthropicLLM) Generate(ctx context.Context, prompt string, maxTokens int) (string, error) {
	reqBody := anthropicMessageRequest{
		Model:     c.model,
		MaxTokens: maxTokens,
		Messages:  []anthropicMessage{{Role: "user", Content: prompt}},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal anthropic request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v1/messages", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create anthropic request: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", anthropicAPIVersion)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("anthropic API request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10 MiB cap
	if err != nil {
		return "", fmt.Errorf("read anthropic response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errEnv anthropicErrorResponse
		if json.Unmarshal(respBody, &errEnv) == nil && errEnv.Error.Type != "" {
			return "", fmt.Errorf("anthropic API returned %d [%s]: %s (request_id=%s)",
				resp.StatusCode, errEnv.Error.Type, errEnv.Error.Message, resp.Header.Get("request-id"))
		}
		return "", fmt.Errorf("anthropic API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result anthropicMessageResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("unmarshal anthropic response: %w", err)
	}

	// A "refusal" is HTTP 200 with no usable content -- fail closed here rather
	// than return a silent empty success.
	if result.StopReason == "refusal" {
		category, explanation := "", ""
		if result.StopDetails != nil {
			category, explanation = result.StopDetails.Category, result.StopDetails.Explanation
		}
		return "", fmt.Errorf("anthropic refused the request (category=%q): %s", category, explanation)
	}

	var textParts []string
	for _, block := range result.Content {
		if block.Type == "text" {
			textParts = append(textParts, block.Text)
		}
	}
	if len(textParts) == 0 {
		return "", fmt.Errorf("anthropic response contained no text content (stop_reason=%q)", result.StopReason)
	}
	return strings.Join(textParts, ""), nil
}

// ---------------------------------------------------------------------------
// Anthropic API types
// ---------------------------------------------------------------------------

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicMessageRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type anthropicStopDetails struct {
	Type        string `json:"type"`
	Category    string `json:"category"`
	Explanation string `json:"explanation"`
}

type anthropicMessageResponse struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"`
	Role         string                  `json:"role"`
	Content      []anthropicContentBlock `json:"content"`
	Model        string                  `json:"model"`
	StopReason   string                  `json:"stop_reason"`
	StopSequence *string                 `json:"stop_sequence"`
	StopDetails  *anthropicStopDetails   `json:"stop_details"`
	Usage        struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type anthropicErrorResponse struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
	RequestID string `json:"request_id"`
}
