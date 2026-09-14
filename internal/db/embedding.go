package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/HelixDevelopment/skills/internal/config"

	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Embedder interface
// ---------------------------------------------------------------------------

// Embedder converts text into dense vector embeddings suitable for
// storage in pgvector columns and similarity search.
type Embedder interface {
	// Embed returns a vector for each input text. The returned slice has
	// the same length as texts. Each inner slice has Dimensions() elements.
	Embed(ctx context.Context, texts []string) ([][]float32, error)

	// Dimensions returns the embedding vector size (e.g. 768, 1536).
	Dimensions() int
}

// ---------------------------------------------------------------------------
// OpenAI implementation
// ---------------------------------------------------------------------------

// OpenAIEmbedder calls the OpenAI Embeddings API.
type OpenAIEmbedder struct {
	apiKey     string
	model      string
	dimensions int
	baseURL    string
	httpClient *http.Client
}

// compile-time interface check.
var _ Embedder = (*OpenAIEmbedder)(nil)

// NewOpenAIEmbedder creates an OpenAI embedder from configuration.
func NewOpenAIEmbedder(cfg config.EmbeddingConfig) *OpenAIEmbedder {
	return &OpenAIEmbedder{
		apiKey:     cfg.APIKey,
		model:      cfg.Model,
		dimensions: cfg.Dimensions,
		baseURL:    "https://api.openai.com/v1",
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// SetHTTPClient replaces the default HTTP client (useful for tests or
// custom transport).
func (e *OpenAIEmbedder) SetHTTPClient(client *http.Client) {
	e.httpClient = client
}

// Embed calls the OpenAI embeddings endpoint.
func (e *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	log := zap.L().With(zap.Int("batch_size", len(texts)), zap.String("model", e.model))

	// OpenAI supports up to 2048 items per request for embedding-3 models.
	const maxBatch = 2048
	if len(texts) > maxBatch {
		return nil, fmt.Errorf("batch size %d exceeds OpenAI limit of %d", len(texts), maxBatch)
	}

	reqBody := openAIEmbedRequest{
		Model: e.model,
		Input: texts,
	}
	// e.dimensions > 0 is a construction invariant for every embedder built
	// via the sole production factory, NewEmbedderFromConfig (Fable round-2
	// finding F4) -- this branch is never false on that path. It remains
	// here (rather than being made unconditional) only because
	// openAIEmbedRequest.Dimensions carries a `json:"dimensions,omitempty"`
	// tag: an unconditional `reqBody.Dimensions = e.dimensions` already
	// produces an IDENTICAL request body for e.dimensions == 0 (omitempty
	// drops the zero value), so this guard's sole remaining, intentional
	// effect is defending a directly-constructed OpenAIEmbedder (bypassing
	// the factory -- e.g. a test) with a negative e.dimensions from leaking
	// that negative value onto the wire; omitempty alone would not catch
	// that case. Any e.dimensions <= 0 still fails every real call below at
	// the unconditional response-length guard, loudly and fail-closed.
	if e.dimensions > 0 {
		reqBody.Dimensions = e.dimensions
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal OpenAI embed request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		e.baseURL+"/embeddings", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create OpenAI embed request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OpenAI embed request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10 MiB cap
	if err != nil {
		return nil, fmt.Errorf("read OpenAI embed response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenAI embed API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result openAIEmbedResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshal OpenAI embed response: %w", err)
	}

	if len(result.Data) != len(texts) {
		return nil, fmt.Errorf("OpenAI returned %d embeddings for %d inputs", len(result.Data), len(texts))
	}

	// Sort by index to maintain input order.
	vectors := make([][]float32, len(texts))
	for _, d := range result.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("OpenAI returned invalid embedding index %d", d.Index)
		}
		// G10 per-embed response-length guard (research/g10_embedding_provider_design.md
		// §2.5, §11.4.201): this codebase requested a width above (reqBody.Dimensions) but,
		// until this fix, never verified the width it got back -- unlike LocalEmbedder's
		// Dimensions() check below, which this mirrors exactly. This is defense-in-depth
		// against ANY response whose embedding length does not match what was configured:
		// an OpenAI-COMPATIBLE gateway/proxy that ignores (or only partially honors) the
		// "dimensions" request parameter, or a provider-side regression that silently
		// changes a model's output width. UNCONFIRMED: whether the OpenAI-hosted
		// text-embedding-ada-002 model specifically ignores the "dimensions" parameter and
		// always returns 1536, or instead rejects the request outright -- the "dimensions"
		// parameter is documented as supported only for text-embedding-3-and-later models,
		// and older-model behavior when it is sent has not been verified here against the
		// current API reference (§11.4.6/§11.4.99). Either way, a wrong-length vector must
		// be rejected HERE, at the source, rather than handed through to a vector(N) column
		// where the mismatch would surface as an opaque insert/query-time pgvector error far
		// from this call -- NEVER silently padded, truncated, or accepted.
		if len(d.Embedding) != e.dimensions {
			return nil, fmt.Errorf("OpenAI returned vector length %d at index %d, expected %d",
				len(d.Embedding), d.Index, e.dimensions)
		}
		vec := make([]float32, len(d.Embedding))
		for i, v := range d.Embedding {
			vec[i] = float32(v)
		}
		vectors[d.Index] = vec
	}

	log.Debug("embeddings generated", zap.Int("count", len(vectors)))
	return vectors, nil
}

// Dimensions returns the configured embedding dimensionality.
func (e *OpenAIEmbedder) Dimensions() int {
	return e.dimensions
}

// OpenAI API types.

type openAIEmbedRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type openAIEmbedResponse struct {
	Data  []openAIEmbedData `json:"data"`
	Model string            `json:"model"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

type openAIEmbedData struct {
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
	Object    string    `json:"object"`
}

// ---------------------------------------------------------------------------
// Local model implementation (HTTP server)
// ---------------------------------------------------------------------------

// LocalEmbedder calls a local embedding model server via HTTP. The
// expected endpoint accepts a JSON POST body { "texts": [...] } and
// returns { "embeddings": [[...], ...] }.
type LocalEmbedder struct {
	endpoint   string
	dimensions int
	httpClient *http.Client
}

// compile-time interface check.
var _ Embedder = (*LocalEmbedder)(nil)

// NewLocalEmbedder creates a local embedder from configuration.
func NewLocalEmbedder(cfg config.EmbeddingConfig) *LocalEmbedder {
	return &LocalEmbedder{
		endpoint:   cfg.LocalEndpoint,
		dimensions: cfg.Dimensions,
		httpClient: &http.Client{
			Timeout: 120 * time.Second, // local models may be slower
		},
	}
}

// SetHTTPClient replaces the default HTTP client.
func (e *LocalEmbedder) SetHTTPClient(client *http.Client) {
	e.httpClient = client
}

// Embed calls the local embedding server.
func (e *LocalEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	if e.endpoint == "" {
		return nil, fmt.Errorf("local embedder endpoint not configured")
	}

	log := zap.L().With(zap.Int("batch_size", len(texts)), zap.String("endpoint", e.endpoint))

	reqBody := localEmbedRequest{Texts: texts}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal local embed request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		e.endpoint+"/embed", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create local embed request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("local embed request to %s failed: %w", e.endpoint, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20)) // 50 MiB cap
	if err != nil {
		return nil, fmt.Errorf("read local embed response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("local embed API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result localEmbedResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshal local embed response: %w", err)
	}

	if len(result.Embeddings) != len(texts) {
		return nil, fmt.Errorf("local embedder returned %d embeddings for %d inputs",
			len(result.Embeddings), len(texts))
	}

	// Validate dimensions.
	for i, vec := range result.Embeddings {
		if len(vec) != e.dimensions {
			return nil, fmt.Errorf("local embedder returned vector length %d at index %d, expected %d",
				len(vec), i, e.dimensions)
		}
	}

	log.Debug("local embeddings generated", zap.Int("count", len(result.Embeddings)))
	return result.Embeddings, nil
}

// Dimensions returns the configured embedding dimensionality.
func (e *LocalEmbedder) Dimensions() int {
	return e.dimensions
}

// Local embedder API types.

type localEmbedRequest struct {
	Texts []string `json:"texts"`
}

type localEmbedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// ---------------------------------------------------------------------------
// Provider factory
// ---------------------------------------------------------------------------

// NewEmbedderFromConfig creates the appropriate Embedder implementation
// based on the embedding provider configured in the application config.
//
// Supported providers:
//   - "openai"  -> OpenAIEmbedder
//   - "local"   -> LocalEmbedder
//
// NewEmbedderFromConfig is fail-closed on an incompletely-specified provider:
// it errors rather than construct an Embedder that is guaranteed to fail its
// first real request. This is the SINGLE source of truth for "is this
// provider fully configured" -- callers (e.g. internal/mcp.NewMCPServer's
// §G29 hybrid-search wiring) determine whether to attach an embedder solely
// from this function's error return, never by re-deriving the same
// per-provider policy a second time (Fable code-review remediation, finding
// 6a: a duplicated "is it configured" check is a second source of truth that
// can silently drift from this one).
//
// A positive cfg.Dimensions is a CONSTRUCTION INVARIANT enforced here, once,
// for every provider (Fable round-2 finding F4): both OpenAIEmbedder.Embed
// and LocalEmbedder.Embed unconditionally reject any response whose
// embedding length does not equal e.dimensions -- a zero (or negative)
// e.dimensions would make every single Embed call fail that check, since a
// real provider response is never zero-length. config.Load()'s own
// validate() already rejects embedding.dimensions <= 0 before a *Config ever
// reaches this factory, but this factory is the SOLE production constructor
// (see above) and MUST NOT rely solely on that upstream, call-site-external
// check to hold an invariant its own returned Embedder depends on to ever
// succeed -- the check belongs at the point of construction.
func NewEmbedderFromConfig(cfg config.EmbeddingConfig) (Embedder, error) {
	if cfg.Dimensions <= 0 {
		return nil, fmt.Errorf("embedding provider requires a positive dimensions value, got %d", cfg.Dimensions)
	}
	switch cfg.Provider {
	case "openai":
		if cfg.APIKey == "" {
			// Previously this case only WARNED and still returned a
			// constructed (but unusable) OpenAIEmbedder -- every caller that
			// used the constructed embedder's success (err == nil) as its
			// "configured" signal would then wire in an embedder whose FIRST
			// real query issues an actual HTTP request to OpenAI with an
			// empty Bearer token, gets a 401, and only THEN degrades to
			// keyword-only (Store.Search's embErr handling) -- a wasted
			// failing network round-trip on every single search instead of a
			// zero-cost skip. Erroring here (matching the "local" case's
			// existing fail-closed missing-endpoint check immediately below)
			// makes the missing-credential case detectable BEFORE any
			// request is attempted, for every caller, not just one that
			// happens to reimplement the same check.
			return nil, fmt.Errorf("openai embedder requires api_key configuration")
		}
		return NewOpenAIEmbedder(cfg), nil
	case "local":
		if cfg.LocalEndpoint == "" {
			return nil, fmt.Errorf("local embedder requires local_endpoint configuration")
		}
		return NewLocalEmbedder(cfg), nil
	default:
		return nil, fmt.Errorf("unsupported embedding provider: %q (expected "+
			"\"openai\" or \"local\")", cfg.Provider)
	}
}

// ---------------------------------------------------------------------------
// Batch / async helpers
// ---------------------------------------------------------------------------

// EmbedBatch breaks a large text slice into smaller batches and embeds
// them sequentially. If a single batch fails the entire operation fails.
func EmbedBatch(
	ctx context.Context,
	embedder Embedder,
	texts []string,
	batchSize int,
) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if batchSize <= 0 {
		batchSize = 64
	}

	var all [][]float32
	for i := 0; i < len(texts); i += batchSize {
		end := i + batchSize
		if end > len(texts) {
			end = len(texts)
		}

		batch, err := embedder.Embed(ctx, texts[i:end])
		if err != nil {
			return nil, fmt.Errorf("embed batch %d-%d: %w", i, end, err)
		}
		all = append(all, batch...)
	}

	return all, nil
}

// AsyncEmbedResult holds the result of a single async embedding job.
type AsyncEmbedResult struct {
	Index  int
	Vector []float32
	Error  error
}

// EmbedAsync embeds each text in a separate goroutine (up to maxConcurrency
// at a time) and returns the results via a channel. The channel is closed
// when all work is complete.
//
// Callers should range over the returned channel until closed.
func EmbedAsync(
	ctx context.Context,
	embedder Embedder,
	texts []string,
	maxConcurrency int,
) <-chan AsyncEmbedResult {
	if maxConcurrency <= 0 {
		maxConcurrency = 4
	}

	results := make(chan AsyncEmbedResult, len(texts))

	go func() {
		defer close(results)

		sem := make(chan struct{}, maxConcurrency)
		var wg sync.WaitGroup

		for i, text := range texts {
			select {
			case <-ctx.Done():
				results <- AsyncEmbedResult{Index: i, Error: ctx.Err()}
				continue
			case sem <- struct{}{}:
			}

			wg.Add(1)
			go func(idx int, t string) {
				defer wg.Done()
				defer func() { <-sem }()

				vecs, err := embedder.Embed(ctx, []string{t})
				if err != nil {
					results <- AsyncEmbedResult{Index: idx, Error: err}
					return
				}
				if len(vecs) > 0 {
					results <- AsyncEmbedResult{Index: idx, Vector: vecs[0]}
				}
			}(i, text)
		}

		wg.Wait()
	}()

	return results
}
