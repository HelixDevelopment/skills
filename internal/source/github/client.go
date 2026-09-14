// Package github provides a minimal, hand-rolled REST client for the
// GitHub API, used by the skill-source ingestion pipeline (see
// docs/source_ingestion/WIRING_PLAN.md §3.1/§1.9 for the design this file
// implements) to fetch a repository's file tree, head commit SHA, and
// individual file contents.
//
// This is deliberately NOT built on a vendor SDK (no google/go-github
// dependency exists in go.mod today, verified 2026-07-16) — it mirrors
// internal/autoexpand.OpenAILLM / AnthropicLLM's own hand-rolled net/http
// client shape (constructor + SetBaseURL/SetHTTPClient test-injection
// setters), which is this project's own established pattern for talking to
// a small external REST surface without pulling in a heavyweight SDK.
package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// TreeEntry is one entry in a repository's git tree, as returned by the
// GitHub "get a tree" API (GET /repos/{owner}/{repo}/git/trees/{ref}).
type TreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"` // "blob" or "tree"
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
}

// treeResponse is the raw shape of the "get a tree" API response.
type treeResponse struct {
	SHA       string      `json:"sha"`
	Tree      []TreeEntry `json:"tree"`
	Truncated bool        `json:"truncated"`
}

// commitResponse is the (partial) raw shape of the "get a commit" API
// response (GET /repos/{owner}/{repo}/commits/{ref}) — only the top-level
// sha field is needed by this client.
type commitResponse struct {
	SHA string `json:"sha"`
}

// contentResponse is the raw shape of the "get repository content" API
// response (GET /repos/{owner}/{repo}/contents/{path}) for a single file.
type contentResponse struct {
	SHA      string `json:"sha"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

// RateLimit captures the GitHub API rate-limit state reported by the
// standard X-RateLimit-* response headers.
type RateLimit struct {
	Limit     int
	Remaining int
	Reset     time.Time
}

// Client is a minimal REST client for the GitHub API.
type Client struct {
	token      string
	baseURL    string
	httpClient *http.Client
	logger     *zap.Logger
}

// NewClient creates a GitHub REST API client. token may be empty for
// unauthenticated (more tightly rate-limited) access to public
// repositories. The token is never logged by this client (see doJSON /
// FetchBlob below — only request paths and rate-limit counters are
// logged, never header values) per the credentials-handling mandate
// (§11.4.10).
func NewClient(token string, logger *zap.Logger) *Client {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Client{
		token:      token,
		baseURL:    "https://api.github.com",
		httpClient: &http.Client{Timeout: 60 * time.Second},
		logger:     logger,
	}
}

// TokenFromEnv reads a GitHub API token from the named environment
// variable and nothing else. It never hardcodes a token value — callers
// choose which variable name to read (e.g. a future
// HELIX_SOURCE_SYNC_GITHUB_TOKEN once the config wiring lands, or a plain
// GITHUB_TOKEN for ad-hoc/local use). It returns "" when the variable is
// unset or empty; callers MUST treat "" as "no authentication", never
// silently substitute a hardcoded fallback (§11.4.10).
func TokenFromEnv(varName string) string {
	return os.Getenv(varName)
}

// SetBaseURL overrides the API base URL. Used by tests to point the
// client at an httptest.Server instead of the real GitHub API, and would
// also allow pointing at a GitHub Enterprise Server instance.
func (c *Client) SetBaseURL(baseURL string) {
	c.baseURL = baseURL
}

// SetHTTPClient replaces the default HTTP client (for test injection of
// custom transports/timeouts).
func (c *Client) SetHTTPClient(client *http.Client) {
	c.httpClient = client
}

// RateLimitStatus reads the standard GitHub rate-limit headers off an
// HTTP response. It never errors — a response with no rate-limit headers
// (e.g. a test fixture, or a non-GitHub host) simply yields a zero-value
// RateLimit, which callers can detect via Limit == 0.
func (c *Client) RateLimitStatus(resp *http.Response) RateLimit {
	var rl RateLimit
	if resp == nil {
		return rl
	}
	rl.Limit, _ = strconv.Atoi(resp.Header.Get("X-RateLimit-Limit"))
	rl.Remaining, _ = strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining"))
	if resetStr := resp.Header.Get("X-RateLimit-Reset"); resetStr != "" {
		if unixSecs, err := strconv.ParseInt(resetStr, 10, 64); err == nil {
			rl.Reset = time.Unix(unixSecs, 0)
		}
	}
	return rl
}

// apiVersion is the GitHub REST API version this client speaks
// (https://docs.github.com/en/rest/about-the-rest-api/api-versions),
// pinned explicitly on every request per GitHub's own recommendation so a
// future GitHub-side default-version bump can never silently change this
// client's response shapes out from under it.
const apiVersion = "2022-11-28"

// newRequest builds a GitHub API request with the headers every GitHub
// REST call needs: Accept (the modern JSON media type), User-Agent (GitHub
// rejects requests without one), X-GitHub-Api-Version (pins the API
// version, see apiVersion), and — only if a token was configured —
// Authorization. The Authorization header VALUE is never logged anywhere
// in this file.
func (c *Client) newRequest(ctx context.Context, method, path string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("github: create request %s %s: %w", method, path, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "helix-skill-system-source-ingestion")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return req, nil
}

// maxErrorBodyBytes bounds how much of a non-2xx response body is embedded
// verbatim into an error string. Without a bound, a large HTML error page
// or an oversized JSON payload could balloon an error message (and any
// downstream log line built from it) up to the full 10 MiB read cap this
// client applies to response bodies.
const maxErrorBodyBytes = 512

// truncateBody returns body's first maxErrorBodyBytes bytes for embedding
// in an error message, appending a note of how many bytes were omitted
// when body exceeds the cap. body under the cap is returned unchanged.
func truncateBody(body []byte) string {
	if len(body) <= maxErrorBodyBytes {
		return string(body)
	}
	return fmt.Sprintf("%s... (truncated, %d bytes total)", string(body[:maxErrorBodyBytes]), len(body))
}

// doJSON issues a GET request against path and decodes a 200 JSON response
// into out. A non-2xx response (other than the caller-handled 304, which
// callers of doJSON never need — only FetchBlob uses conditional requests)
// is returned as an error carrying the status code and response body.
func (c *Client) doJSON(ctx context.Context, path string, out interface{}) error {
	req, err := c.newRequest(ctx, http.MethodGet, path)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("github: request %s: %w", path, err)
	}
	defer resp.Body.Close()

	if rl := c.RateLimitStatus(resp); rl.Limit > 0 && rl.Remaining == 0 {
		c.logger.Warn("github API rate limit exhausted",
			zap.String("path", path),
			zap.Int("limit", rl.Limit),
			zap.Time("reset", rl.Reset),
		)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10 MiB cap
	if err != nil {
		return fmt.Errorf("github: read response %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github: %s returned %d: %s", path, resp.StatusCode, truncateBody(body))
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("github: unmarshal response %s: %w", path, err)
		}
	}
	return nil
}

// ListTreeResult is ListTreeRecursive's return value: the listed tree
// entries plus whether GitHub's "get a tree" API reported the listing as
// truncated.
//
// W-b remediation: ListTreeRecursive previously returned a bare
// []TreeEntry and only LOGGED the API's "truncated" bit — a caller had
// no programmatic way to detect a partial listing at all, only an
// operator watching logs could. GitHub truncates very large trees
// (>100,000 entries or >7MB of tree data); Truncated=true means Entries
// is a PARTIAL listing of the tree, and a caller (a future scan
// orchestrator per docs/source_ingestion/WIRING_PLAN.md §3.6) needs this
// signal to decide whether to fall back to a shallow git clone, surface
// an operator warning, or otherwise treat the listing as incomplete
// rather than silently scanning a subset of the repository as if it were
// the whole thing.
type ListTreeResult struct {
	Entries   []TreeEntry
	Truncated bool
}

// ListTreeRecursive lists every path in owner/repo at ref via the "get a
// tree" API with recursive=1. GitHub truncates very large trees (>100,000
// entries or >7MB of tree data); a truncated response is both logged as a
// warning (never silently swallowed) AND surfaced on the returned
// ListTreeResult.Truncated field, since a partial listing is still
// useful to the caller but the caller needs a way to detect "this
// listing may be incomplete" other than parsing a log line.
func (c *Client) ListTreeRecursive(ctx context.Context, owner, repo, ref string) (*ListTreeResult, error) {
	if owner == "" || repo == "" || ref == "" {
		return nil, fmt.Errorf("github: ListTreeRecursive requires non-empty owner, repo, and ref")
	}
	path := fmt.Sprintf("/repos/%s/%s/git/trees/%s?recursive=1", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(ref))
	var out treeResponse
	if err := c.doJSON(ctx, path, &out); err != nil {
		return nil, fmt.Errorf("github: list tree %s/%s@%s: %w", owner, repo, ref, err)
	}
	if out.Truncated {
		c.logger.Warn("github tree listing truncated by API; some paths may be missing",
			zap.String("owner", owner), zap.String("repo", repo), zap.String("ref", ref))
	}
	return &ListTreeResult{Entries: out.Tree, Truncated: out.Truncated}, nil
}

// GetHeadSHA resolves ref (a branch/tag name, or already a commit SHA) to
// its current commit SHA. Callers use this as a cheap "has anything
// changed since the last scan?" gate before doing any tree listing or
// blob fetch.
func (c *Client) GetHeadSHA(ctx context.Context, owner, repo, ref string) (string, error) {
	if owner == "" || repo == "" || ref == "" {
		return "", fmt.Errorf("github: GetHeadSHA requires non-empty owner, repo, and ref")
	}
	path := fmt.Sprintf("/repos/%s/%s/commits/%s", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(ref))
	var out commitResponse
	if err := c.doJSON(ctx, path, &out); err != nil {
		return "", fmt.Errorf("github: get head sha %s/%s@%s: %w", owner, repo, ref, err)
	}
	if out.SHA == "" {
		return "", fmt.Errorf("github: get head sha %s/%s@%s: empty sha in response", owner, repo, ref)
	}
	return out.SHA, nil
}

// BlobResult is FetchBlob's return value. Content and SHA are populated
// only when NotModified is false. ETag is populated whenever the server
// sent one — on a fresh 200 fetch AND on a 304 — since GitHub returns the
// same ETag value in both cases; callers persist ETag and pass it back as
// FetchBlob's etag argument on the next call for that same path to enable
// conditional-request caching.
type BlobResult struct {
	// Content is the file's decoded (base64-decoded) byte content. Nil
	// when NotModified is true.
	Content []byte

	// SHA is the blob's git SHA, as reported by the "get repository
	// content" API. Empty when NotModified is true.
	SHA string

	// ETag is the response's ETag header value (already includes any
	// surrounding quotes GitHub sends, e.g. `"abc123"`), suitable for
	// passing back verbatim as FetchBlob's etag argument. Empty if the
	// server sent no ETag header (e.g. a non-GitHub test fixture).
	ETag string

	// NotModified is true when the server returned 304 Not Modified in
	// response to the caller's If-None-Match (built from a previously
	// persisted ETag) — the caller's cached copy is still current, and
	// Content/SHA carry no data.
	NotModified bool
}

// FetchBlob fetches a single file's content at path (in owner/repo, at
// ref) via the "get repository content" API. If etag is non-empty it is
// sent as If-None-Match; a 304 response yields BlobResult.NotModified=true
// and no Content/SHA (the caller's cached copy is still current). ref may
// be empty to fetch the repository's default branch.
//
// Caching flow: a caller that wants conditional-request caching persists
// the ETag from a prior FetchBlob call's BlobResult and passes it back as
// this call's etag argument; when the upstream file is unchanged the
// server replies 304 and BlobResult.NotModified is true. Without
// persisting and replaying that ETag, no caller can ever reach a genuine
// 304 — the etag parameter alone is not self-sustaining.
func (c *Client) FetchBlob(ctx context.Context, owner, repo, path, ref, etag string) (*BlobResult, error) {
	if owner == "" || repo == "" || path == "" {
		return nil, fmt.Errorf("github: FetchBlob requires non-empty owner, repo, and path")
	}
	reqPath := fmt.Sprintf("/repos/%s/%s/contents/%s", url.PathEscape(owner), url.PathEscape(repo), pathEscapeSegments(path))
	if ref != "" {
		reqPath += "?ref=" + url.QueryEscape(ref)
	}
	req, err := c.newRequest(ctx, http.MethodGet, reqPath)
	if err != nil {
		return nil, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: fetch blob %s/%s/%s: %w", owner, repo, path, err)
	}
	defer resp.Body.Close()

	if rl := c.RateLimitStatus(resp); rl.Limit > 0 && rl.Remaining == 0 {
		c.logger.Warn("github API rate limit exhausted",
			zap.String("path", reqPath),
			zap.Int("limit", rl.Limit),
			zap.Time("reset", rl.Reset),
		)
	}

	respETag := resp.Header.Get("ETag")

	if resp.StatusCode == http.StatusNotModified {
		return &BlobResult{ETag: respETag, NotModified: true}, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10 MiB cap
	if err != nil {
		return nil, fmt.Errorf("github: fetch blob %s/%s/%s: read response: %w", owner, repo, path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github: fetch blob %s/%s/%s: API returned %d: %s", owner, repo, path, resp.StatusCode, truncateBody(body))
	}

	var out contentResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("github: fetch blob %s/%s/%s: unmarshal response: %w", owner, repo, path, err)
	}
	if out.Encoding != "base64" {
		return nil, fmt.Errorf("github: fetch blob %s/%s/%s: unsupported content encoding %q", owner, repo, path, out.Encoding)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("github: fetch blob %s/%s/%s: decode base64 content: %w", owner, repo, path, err)
	}
	return &BlobResult{Content: decoded, SHA: out.SHA, ETag: respETag}, nil
}

// pathEscapeSegments percent-encodes each "/"-separated segment of a
// repository-relative path independently, so the "/" separators
// themselves are preserved (url.PathEscape alone would also escape them).
func pathEscapeSegments(p string) string {
	segments := strings.Split(p, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return strings.Join(segments, "/")
}
