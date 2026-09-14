package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/HelixDevelopment/skills/internal/models"
)

// APIClient wraps HTTP calls to the skill system API
type APIClient struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

// NewAPIClient creates a new API client
func NewAPIClient(baseURL, apiKey string) *APIClient {
	return &APIClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 15 * time.Second},
	}
}

// SetBaseURL updates the API base URL
func (c *APIClient) SetBaseURL(url string) {
	c.baseURL = strings.TrimRight(url, "/")
}

// SetAPIKey updates the API key
func (c *APIClient) SetAPIKey(key string) {
	c.apiKey = key
}

// BaseURL returns the current base URL
func (c *APIClient) BaseURL() string {
	return c.baseURL
}

// HealthCheck tests if the API is reachable.
//
// It probes the server's OPEN health route at ROOT /health (cmd/server/main.go
// buildRouter registers "Health check (open)" at /health, and docs/API.md →
// "Health & Info → GET /health" documents the same). There is NO /api/v1/health
// route, so probing under /api/v1 would 404 and falsely report a healthy server
// unreachable (G52). Health is unauthenticated, so this request carries no
// X-API-Key and works whether or not a key is configured.
func (c *APIClient) HealthCheck(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return false
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode < 400
}

// request makes an authenticated HTTP request
func (c *APIClient) request(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	url := c.baseURL + path

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		// Send the key in the server-canonical X-API-Key header (see
		// internal/api/middleware.go APIKeyAuth, which reads X-API-Key). Sending
		// "Authorization: Bearer" here would 401 the moment auth is enforced (G35).
		req.Header.Set("X-API-Key", c.apiKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(bodyBytes))
	}

	return resp, nil
}

// ListFilter represents a filter for listing skills
type ListFilter struct {
	Key   string
	Value string
}

// ListSkills retrieves all skills with optional filtering
func (c *APIClient) ListSkills(ctx context.Context, filters ...ListFilter) ([]models.Skill, error) {
	path := "/api/v1/skills?limit=1000"
	for _, f := range filters {
		path += "&" + f.Key + "=" + f.Value
	}

	resp, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var skills []models.Skill
	if err := json.NewDecoder(resp.Body).Decode(&skills); err != nil {
		return nil, fmt.Errorf("decode skills: %w", err)
	}
	return skills, nil
}

// GetSkill retrieves a single skill by name
func (c *APIClient) GetSkill(ctx context.Context, name string) (*models.Skill, error) {
	resp, err := c.request(ctx, http.MethodGet, "/api/v1/skills/"+name, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var skill models.Skill
	if err := json.NewDecoder(resp.Body).Decode(&skill); err != nil {
		return nil, fmt.Errorf("decode skill: %w", err)
	}
	return &skill, nil
}

// Search performs a keyword search
func (c *APIClient) Search(ctx context.Context, query string, limit int) ([]models.SearchResult, error) {
	path := fmt.Sprintf("/api/v1/search?q=%s&limit=%d", urlEncode(query), limit)

	resp, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var results []models.SearchResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("decode search results: %w", err)
	}
	return results, nil
}

// GetRegistry retrieves all registry entries
func (c *APIClient) GetRegistry(ctx context.Context) ([]models.SkillRegistryEntry, error) {
	resp, err := c.request(ctx, http.MethodGet, "/api/v1/registry", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var entries []models.SkillRegistryEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode registry: %w", err)
	}
	return entries, nil
}

// GetRegistryStats retrieves registry statistics
func (c *APIClient) GetRegistryStats(ctx context.Context) (*RegistryStats, error) {
	resp, err := c.request(ctx, http.MethodGet, "/api/v1/registry/status", nil)
	if err != nil {
		// Fallback: compute from entries
		return c.computeStatsFallback(ctx)
	}
	defer resp.Body.Close()

	var status struct {
		TotalSkills int            `json:"total_skills"`
		MissingDeps int            `json:"missing_dependencies"`
		StaleSkills int            `json:"stale_skills"`
		Coverage    float64        `json:"average_coverage"`
		ByStatus    map[string]int `json:"by_status"`
		Health      string         `json:"health"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return c.computeStatsFallback(ctx)
	}

	return &RegistryStats{
		TotalSkills:    status.TotalSkills,
		Coverage:       status.Coverage,
		MissingDeps:    status.MissingDeps,
		StaleSkills:    status.StaleSkills,
		SkillsByStatus: status.ByStatus,
		Health:         status.Health,
	}, nil
}

// computeStatsFallback computes registry stats from entries when the status endpoint is unavailable
func (c *APIClient) computeStatsFallback(ctx context.Context) (*RegistryStats, error) {
	entries, err := c.GetRegistry(ctx)
	if err != nil {
		return nil, err
	}

	total := len(entries)
	missing := 0
	stale := 0
	var totalCoverage float64
	byStatus := make(map[string]int)

	for _, e := range entries {
		if len(e.MissingDeps) > 0 {
			missing++
		}
		if e.Stale {
			stale++
		}
		totalCoverage += e.Coverage
		byStatus["active"]++ // Best guess without individual skill data
	}

	avgCoverage := 0.0
	if total > 0 {
		avgCoverage = totalCoverage / float64(total)
	}

	health := "healthy"
	if missing > total/4 || stale > total/4 {
		health = "critical"
	} else if missing > 0 || stale > 0 {
		health = "warning"
	}

	return &RegistryStats{
		TotalSkills:    total,
		Coverage:       avgCoverage,
		MissingDeps:    missing,
		StaleSkills:    stale,
		SkillsByStatus: byStatus,
		Health:         health,
	}, nil
}

// GetTree retrieves the skill dependency tree
func (c *APIClient) GetTree(ctx context.Context, name string, depth int) (*models.SkillTreeNode, error) {
	path := fmt.Sprintf("/api/v1/skills/%s/tree?depth=%d", name, depth)

	resp, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var node models.SkillTreeNode
	if err := json.NewDecoder(resp.Body).Decode(&node); err != nil {
		return nil, fmt.Errorf("decode tree: %w", err)
	}
	return &node, nil
}

// urlEncode simple URL encoding for query parameters
func urlEncode(s string) string {
	return strings.ReplaceAll(s, " ", "+")
}
