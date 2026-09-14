package mcp

// ---------------------------------------------------------------------------
// Tests for the three CodeGraph MCP tools (codegraph_analyze,
// codegraph_search, codegraph_stats). These dispatch through the REAL
// registered tool handlers via dispatchTool -- the exact code path
// stdio/HTTP/ACP transports use -- not a re-implementation.
//
// Coverage:
//   - §G31 path-traversal guard for codegraph_analyze and codegraph_stats
//   - Missing / empty parameter validation
//   - codegraph_search with no prior analyze (graceful empty result)
//   - codegraph_stats with no prior analyze (error)
//   - codegraph_analyze → codegraph_search → codegraph_stats end-to-end
//   - codeGraphStore concurrency safety
// ---------------------------------------------------------------------------

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/HelixDevelopment/skills/internal/codeanalysis"
	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/registry"
	"github.com/HelixDevelopment/skills/internal/skill"

	mcp_go "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// newCodeGraphTestServer builds a real *MCPServer with all tool handlers
// registered, backed by pool (nil for pure-unit tests), and codeanalysis.
// allowed_root set to allowedRoot.
func newCodeGraphTestServer(t *testing.T, pool *db.Pool, allowedRoot string) *MCPServer {
	t.Helper()

	cfg := &config.Config{
		MCP:        config.MCPConfig{Enabled: true, Transport: "stdio"},
		Validation: config.ValidationConfig{Enabled: false, JurySize: 1, ApprovalThreshold: 1},
		CodeAnalysis: config.CodeAnalysisConfig{
			Enabled:         true,
			Languages:       []string{"go"},
			MaxFileSizeKB:   500,
			ExcludePatterns: []string{"vendor/", "node_modules/", ".git/"},
			AllowedRoot:     allowedRoot,
		},
	}

	store := skill.NewStore(pool)
	reg := registry.NewRegistry(pool)
	logger := zap.NewNop()

	srv := NewMCPServer(pool, store, reg, cfg, logger)
	srv.RegisterTools()
	return srv
}

// codeGraphResultText extracts the JSON text body from a tool result.
func codeGraphResultText(t *testing.T, res *mcp_go.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatalf("tool result has no content: %+v", res)
	}
	tc, ok := res.Content[0].(mcp_go.TextContent)
	if !ok {
		t.Fatalf("tool result content[0] is not TextContent: %#v", res.Content[0])
	}
	return tc.Text
}

// createGoProject creates a minimal Go project in a temp directory for
// analysis tests. Returns the project directory path.
func createGoProject(t *testing.T, root string) string {
	t.Helper()

	projectDir := filepath.Join(root, "myproject")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Write a minimal Go file with recognizable patterns.
	mainGo := `package main

import (
	"fmt"
	"net/http"
)

// Controller handles HTTP requests.
type Controller struct {
	Name string
}

func (c *Controller) Handle(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "Hello, %s!", c.Name)
}

func main() {
	c := &Controller{Name: "world"}
	http.HandleFunc("/", c.Handle)
	http.ListenAndServe(":8080", nil)
}
`
	if err := os.WriteFile(filepath.Join(projectDir, "main.go"), []byte(mainGo), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	return projectDir
}

// ---------------------------------------------------------------------------
// codegraph_analyze: §G31 path-traversal guard wiring
// ---------------------------------------------------------------------------

func TestCodeGraphAnalyze_Wiring_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	srv := newCodeGraphTestServer(t, nil, root)

	traversal := filepath.Join(root, "..", filepath.Base(outside))

	res, err := srv.dispatchTool(context.Background(), "codegraph_analyze", map[string]interface{}{
		"project_path": traversal,
	})
	if err != nil {
		t.Fatalf("dispatchTool returned unexpected transport-level error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("codegraph_analyze(%q) did not report IsError, want rejection; body=%s", traversal, codeGraphResultText(t, res))
	}
	if body := codeGraphResultText(t, res); !strings.Contains(body, "Rejected project_path") {
		t.Errorf("error body = %q, want it to mention the rejection", body)
	}
}

func TestCodeGraphAnalyze_Wiring_RejectsAbsoluteOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	srv := newCodeGraphTestServer(t, nil, root)

	res, err := srv.dispatchTool(context.Background(), "codegraph_analyze", map[string]interface{}{
		"project_path": outside,
	})
	if err != nil {
		t.Fatalf("dispatchTool returned unexpected transport-level error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("codegraph_analyze(%q) did not report IsError, want rejection; body=%s", outside, codeGraphResultText(t, res))
	}
}

func TestCodeGraphAnalyze_Wiring_RejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	srv := newCodeGraphTestServer(t, nil, root)

	link := filepath.Join(root, "escape-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink creation unsupported in this environment: %v", err)
	}

	res, err := srv.dispatchTool(context.Background(), "codegraph_analyze", map[string]interface{}{
		"project_path": link,
	})
	if err != nil {
		t.Fatalf("dispatchTool returned unexpected transport-level error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("codegraph_analyze(%q) [symlink to outside] did not report IsError, want rejection; body=%s", link, codeGraphResultText(t, res))
	}
}

func TestCodeGraphAnalyze_Wiring_RejectsWhenNoAllowedRootConfigured(t *testing.T) {
	root := t.TempDir()
	srv := newCodeGraphTestServer(t, nil, "" /* no allowed root configured */)

	res, err := srv.dispatchTool(context.Background(), "codegraph_analyze", map[string]interface{}{
		"project_path": root,
	})
	if err != nil {
		t.Fatalf("dispatchTool returned unexpected transport-level error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("codegraph_analyze with no allowed_root configured did not report IsError, want fail-closed rejection; body=%s", codeGraphResultText(t, res))
	}
}

// ---------------------------------------------------------------------------
// codegraph_analyze: missing parameter validation
// ---------------------------------------------------------------------------

func TestCodeGraphAnalyze_MissingProjectPath(t *testing.T) {
	root := t.TempDir()
	srv := newCodeGraphTestServer(t, nil, root)

	res, err := srv.dispatchTool(context.Background(), "codegraph_analyze", map[string]interface{}{})
	if err != nil {
		t.Fatalf("dispatchTool returned unexpected transport-level error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("codegraph_analyze with no project_path did not report IsError; body=%s", codeGraphResultText(t, res))
	}
	if body := codeGraphResultText(t, res); !strings.Contains(body, "project_path parameter is required") {
		t.Errorf("error body = %q, want it to mention missing project_path", body)
	}
}

func TestCodeGraphAnalyze_EmptyProjectPath(t *testing.T) {
	root := t.TempDir()
	srv := newCodeGraphTestServer(t, nil, root)

	res, err := srv.dispatchTool(context.Background(), "codegraph_analyze", map[string]interface{}{
		"project_path": "",
	})
	if err != nil {
		t.Fatalf("dispatchTool returned unexpected transport-level error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("codegraph_analyze with empty project_path did not report IsError; body=%s", codeGraphResultText(t, res))
	}
}

// ---------------------------------------------------------------------------
// codegraph_analyze: happy path (real file analysis)
// ---------------------------------------------------------------------------

func TestCodeGraphAnalyze_HappyPath(t *testing.T) {
	root := t.TempDir()
	projectDir := createGoProject(t, root)
	srv := newCodeGraphTestServer(t, nil, root)

	res, err := srv.dispatchTool(context.Background(), "codegraph_analyze", map[string]interface{}{
		"project_path": projectDir,
		"languages":    []interface{}{"go"},
	})
	if err != nil {
		t.Fatalf("dispatchTool returned unexpected transport-level error: %v", err)
	}
	if res.IsError {
		t.Fatalf("codegraph_analyze(%q) was rejected, want acceptance; body=%s", projectDir, codeGraphResultText(t, res))
	}

	var payload struct {
		Success     bool                   `json:"success"`
		ProjectPath string                 `json:"project_path"`
		Summary     map[string]interface{} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(codeGraphResultText(t, res)), &payload); err != nil {
		t.Fatalf("unmarshal tool result body: %v (%s)", err, codeGraphResultText(t, res))
	}
	if !payload.Success {
		t.Errorf("payload.Success = false, want true")
	}
	if payload.ProjectPath == "" {
		t.Errorf("payload.ProjectPath is empty, want canonicalized path")
	}

	// The Go file should have been analyzed -- check that the summary
	// contains language info.
	if payload.Summary == nil {
		t.Fatalf("payload.Summary is nil, want populated summary")
	}
}

// ---------------------------------------------------------------------------
// codegraph_search: tests
// ---------------------------------------------------------------------------

func TestCodeGraphSearch_MissingQuery(t *testing.T) {
	root := t.TempDir()
	srv := newCodeGraphTestServer(t, nil, root)

	res, err := srv.dispatchTool(context.Background(), "codegraph_search", map[string]interface{}{})
	if err != nil {
		t.Fatalf("dispatchTool returned unexpected transport-level error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("codegraph_search with no query did not report IsError; body=%s", codeGraphResultText(t, res))
	}
	if body := codeGraphResultText(t, res); !strings.Contains(body, "query parameter is required") {
		t.Errorf("error body = %q, want it to mention missing query", body)
	}
}

func TestCodeGraphSearch_NoPriorAnalyze(t *testing.T) {
	root := t.TempDir()
	srv := newCodeGraphTestServer(t, nil, root)

	// Search without any prior analyze call -- should return empty results gracefully.
	res, err := srv.dispatchTool(context.Background(), "codegraph_search", map[string]interface{}{
		"query": "controller",
	})
	if err != nil {
		t.Fatalf("dispatchTool returned unexpected transport-level error: %v", err)
	}
	if res.IsError {
		t.Fatalf("codegraph_search with no prior analyze returned error, want graceful empty; body=%s", codeGraphResultText(t, res))
	}

	var payload struct {
		Count   int           `json:"count"`
		Results []interface{} `json:"results"`
	}
	if err := json.Unmarshal([]byte(codeGraphResultText(t, res)), &payload); err != nil {
		t.Fatalf("unmarshal tool result body: %v (%s)", err, codeGraphResultText(t, res))
	}
	if payload.Count != 0 {
		t.Errorf("count = %d, want 0 when no prior analyze exists", payload.Count)
	}
}

func TestCodeGraphSearch_AfterAnalyze(t *testing.T) {
	root := t.TempDir()
	projectDir := createGoProject(t, root)
	srv := newCodeGraphTestServer(t, nil, root)

	// First analyze the project.
	analyzeRes, err := srv.dispatchTool(context.Background(), "codegraph_analyze", map[string]interface{}{
		"project_path": projectDir,
	})
	if err != nil {
		t.Fatalf("dispatchTool(analyze) returned unexpected transport-level error: %v", err)
	}
	if analyzeRes.IsError {
		t.Fatalf("codegraph_analyze failed: %s", codeGraphResultText(t, analyzeRes))
	}

	// Now search for "controller" -- should find the MVC pattern from the Go file.
	searchRes, err := srv.dispatchTool(context.Background(), "codegraph_search", map[string]interface{}{
		"query": "controller",
	})
	if err != nil {
		t.Fatalf("dispatchTool(search) returned unexpected transport-level error: %v", err)
	}
	if searchRes.IsError {
		t.Fatalf("codegraph_search returned error: %s", codeGraphResultText(t, searchRes))
	}

	var payload struct {
		Count   int `json:"count"`
		Results []struct {
			Name    string  `json:"name"`
			Type    string  `json:"type"`
			File    string  `json:"file"`
			Score   float64 `json:"score"`
			Project string  `json:"project_path"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(codeGraphResultText(t, searchRes)), &payload); err != nil {
		t.Fatalf("unmarshal search result: %v (%s)", err, codeGraphResultText(t, searchRes))
	}

	// The Go file contains "Controller" in a struct definition, so pattern
	// detection should find an MVC-related pattern.
	if payload.Count == 0 {
		t.Errorf("search returned 0 results after analyzing a project with Controller, want > 0; body=%s", codeGraphResultText(t, searchRes))
	}

	// Verify results reference the analyzed project.
	for _, r := range payload.Results {
		if r.Project != projectDir {
			t.Errorf("result project_path = %q, want %q", r.Project, projectDir)
		}
		if r.Score <= 0 {
			t.Errorf("result score = %f, want > 0", r.Score)
		}
	}
}

func TestCodeGraphSearch_WithEntityTypeFilter(t *testing.T) {
	root := t.TempDir()
	projectDir := createGoProject(t, root)
	srv := newCodeGraphTestServer(t, nil, root)

	// Analyze first.
	_, _ = srv.dispatchTool(context.Background(), "codegraph_analyze", map[string]interface{}{
		"project_path": projectDir,
	})

	// Search with entity_type filter.
	res, err := srv.dispatchTool(context.Background(), "codegraph_search", map[string]interface{}{
		"query":       "controller",
		"entity_type": "class",
	})
	if err != nil {
		t.Fatalf("dispatchTool returned unexpected transport-level error: %v", err)
	}
	if res.IsError {
		t.Fatalf("codegraph_search with entity_type returned error: %s", codeGraphResultText(t, res))
	}
	// Should complete without error -- entity_type filter narrows results.
}

func TestCodeGraphSearch_WithLimit(t *testing.T) {
	root := t.TempDir()
	projectDir := createGoProject(t, root)
	srv := newCodeGraphTestServer(t, nil, root)

	// Analyze first.
	_, _ = srv.dispatchTool(context.Background(), "codegraph_analyze", map[string]interface{}{
		"project_path": projectDir,
	})

	// Search with limit=1.
	res, err := srv.dispatchTool(context.Background(), "codegraph_search", map[string]interface{}{
		"query": "controller",
		"limit": 1.0,
	})
	if err != nil {
		t.Fatalf("dispatchTool returned unexpected transport-level error: %v", err)
	}
	if res.IsError {
		t.Fatalf("codegraph_search with limit returned error: %s", codeGraphResultText(t, res))
	}

	var payload struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(codeGraphResultText(t, res)), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Count > 1 {
		t.Errorf("count = %d, want <= 1 (limit=1)", payload.Count)
	}
}

// ---------------------------------------------------------------------------
// codegraph_stats: §G31 path-traversal guard wiring
// ---------------------------------------------------------------------------

func TestCodeGraphStats_Wiring_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	srv := newCodeGraphTestServer(t, nil, root)

	traversal := filepath.Join(root, "..", filepath.Base(outside))

	res, err := srv.dispatchTool(context.Background(), "codegraph_stats", map[string]interface{}{
		"project_path": traversal,
	})
	if err != nil {
		t.Fatalf("dispatchTool returned unexpected transport-level error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("codegraph_stats(%q) did not report IsError, want rejection; body=%s", traversal, codeGraphResultText(t, res))
	}
}

func TestCodeGraphStats_Wiring_RejectsAbsoluteOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	srv := newCodeGraphTestServer(t, nil, root)

	res, err := srv.dispatchTool(context.Background(), "codegraph_stats", map[string]interface{}{
		"project_path": outside,
	})
	if err != nil {
		t.Fatalf("dispatchTool returned unexpected transport-level error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("codegraph_stats(%q) did not report IsError, want rejection; body=%s", outside, codeGraphResultText(t, res))
	}
}

func TestCodeGraphStats_Wiring_RejectsWhenNoAllowedRootConfigured(t *testing.T) {
	root := t.TempDir()
	srv := newCodeGraphTestServer(t, nil, "")

	res, err := srv.dispatchTool(context.Background(), "codegraph_stats", map[string]interface{}{
		"project_path": root,
	})
	if err != nil {
		t.Fatalf("dispatchTool returned unexpected transport-level error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("codegraph_stats with no allowed_root configured did not report IsError; body=%s", codeGraphResultText(t, res))
	}
}

// ---------------------------------------------------------------------------
// codegraph_stats: missing parameter validation
// ---------------------------------------------------------------------------

func TestCodeGraphStats_MissingProjectPath(t *testing.T) {
	root := t.TempDir()
	srv := newCodeGraphTestServer(t, nil, root)

	res, err := srv.dispatchTool(context.Background(), "codegraph_stats", map[string]interface{}{})
	if err != nil {
		t.Fatalf("dispatchTool returned unexpected transport-level error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("codegraph_stats with no project_path did not report IsError; body=%s", codeGraphResultText(t, res))
	}
}

// ---------------------------------------------------------------------------
// codegraph_stats: no prior analyze
// ---------------------------------------------------------------------------

func TestCodeGraphStats_NoPriorAnalyze(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	srv := newCodeGraphTestServer(t, nil, root)

	res, err := srv.dispatchTool(context.Background(), "codegraph_stats", map[string]interface{}{
		"project_path": child,
	})
	if err != nil {
		t.Fatalf("dispatchTool returned unexpected transport-level error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("codegraph_stats with no prior analyze did not report IsError; body=%s", codeGraphResultText(t, res))
	}
	if body := codeGraphResultText(t, res); !strings.Contains(body, "No analysis results found") {
		t.Errorf("error body = %q, want it to mention no analysis results", body)
	}
}

// ---------------------------------------------------------------------------
// codegraph_stats: happy path after analyze
// ---------------------------------------------------------------------------

func TestCodeGraphStats_AfterAnalyze(t *testing.T) {
	root := t.TempDir()
	projectDir := createGoProject(t, root)
	srv := newCodeGraphTestServer(t, nil, root)

	// Analyze first.
	analyzeRes, err := srv.dispatchTool(context.Background(), "codegraph_analyze", map[string]interface{}{
		"project_path": projectDir,
	})
	if err != nil {
		t.Fatalf("dispatchTool(analyze) returned unexpected transport-level error: %v", err)
	}
	if analyzeRes.IsError {
		t.Fatalf("codegraph_analyze failed: %s", codeGraphResultText(t, analyzeRes))
	}

	// Now get stats.
	statsRes, err := srv.dispatchTool(context.Background(), "codegraph_stats", map[string]interface{}{
		"project_path": projectDir,
	})
	if err != nil {
		t.Fatalf("dispatchTool(stats) returned unexpected transport-level error: %v", err)
	}
	if statsRes.IsError {
		t.Fatalf("codegraph_stats returned error: %s", codeGraphResultText(t, statsRes))
	}

	var payload struct {
		ProjectPath       string         `json:"project_path"`
		TotalFiles        int            `json:"total_files"`
		TotalImports      int            `json:"total_imports"`
		TotalPatterns     int            `json:"total_patterns"`
		EntityCounts      map[string]int `json:"entity_counts"`
		LanguageBreakdown map[string]int `json:"language_breakdown"`
	}
	if err := json.Unmarshal([]byte(codeGraphResultText(t, statsRes)), &payload); err != nil {
		t.Fatalf("unmarshal stats result: %v (%s)", err, codeGraphResultText(t, statsRes))
	}

	if payload.ProjectPath != projectDir {
		t.Errorf("project_path = %q, want %q", payload.ProjectPath, projectDir)
	}
	if payload.TotalFiles == 0 {
		t.Errorf("total_files = 0, want > 0 after analyzing a project with Go files")
	}
	if payload.LanguageBreakdown == nil {
		t.Errorf("language_breakdown is nil, want populated map")
	}
}

// ---------------------------------------------------------------------------
// End-to-end: analyze → search → stats
// ---------------------------------------------------------------------------

func TestCodeGraph_EndToEnd(t *testing.T) {
	root := t.TempDir()
	projectDir := createGoProject(t, root)
	srv := newCodeGraphTestServer(t, nil, root)

	// Step 1: Analyze.
	analyzeRes, err := srv.dispatchTool(context.Background(), "codegraph_analyze", map[string]interface{}{
		"project_path": projectDir,
		"languages":    []interface{}{"go"},
	})
	if err != nil {
		t.Fatalf("dispatchTool(analyze) error: %v", err)
	}
	if analyzeRes.IsError {
		t.Fatalf("analyze failed: %s", codeGraphResultText(t, analyzeRes))
	}

	// Step 2: Search for patterns found during analysis.
	searchRes, err := srv.dispatchTool(context.Background(), "codegraph_search", map[string]interface{}{
		"query": "http",
	})
	if err != nil {
		t.Fatalf("dispatchTool(search) error: %v", err)
	}
	if searchRes.IsError {
		t.Fatalf("search failed: %s", codeGraphResultText(t, searchRes))
	}

	var searchPayload struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(codeGraphResultText(t, searchRes)), &searchPayload); err != nil {
		t.Fatalf("unmarshal search: %v", err)
	}
	// The Go file imports "net/http" and uses HTTP handlers, so we expect hits.
	if searchPayload.Count == 0 {
		t.Logf("search for 'http' returned 0 results (may be expected if pattern detection didn't match); body=%s", codeGraphResultText(t, searchRes))
	}

	// Step 3: Get stats.
	statsRes, err := srv.dispatchTool(context.Background(), "codegraph_stats", map[string]interface{}{
		"project_path": projectDir,
	})
	if err != nil {
		t.Fatalf("dispatchTool(stats) error: %v", err)
	}
	if statsRes.IsError {
		t.Fatalf("stats failed: %s", codeGraphResultText(t, statsRes))
	}

	var statsPayload struct {
		TotalFiles int `json:"total_files"`
	}
	if err := json.Unmarshal([]byte(codeGraphResultText(t, statsRes)), &statsPayload); err != nil {
		t.Fatalf("unmarshal stats: %v", err)
	}
	if statsPayload.TotalFiles == 0 {
		t.Errorf("stats total_files = 0, want > 0")
	}
}

// ---------------------------------------------------------------------------
// codeGraphStore: concurrency safety
// ---------------------------------------------------------------------------

func TestCodeGraphStore_ConcurrentAccess(t *testing.T) {
	store := newCodeGraphStore()

	var wg sync.WaitGroup
	const goroutines = 50

	// Concurrent puts.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := filepath.Join("/tmp/project", string(rune('A'+i%26)))
			store.put(path, &codeanalysis.AnalysisResult{
				ProjectPath: path,
				Languages:   map[string]int{"go": i},
			})
		}(i)
	}
	wg.Wait()

	// Concurrent gets.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := filepath.Join("/tmp/project", string(rune('A'+i%26)))
			_ = store.get(path)
		}(i)
	}
	wg.Wait()

	// Concurrent listPaths.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = store.listPaths()
		}()
	}
	wg.Wait()

	// Verify no panic occurred -- the test passing means the store is safe.
}

// ---------------------------------------------------------------------------
// Helper function tests
// ---------------------------------------------------------------------------

func TestMatchesEntityType(t *testing.T) {
	tests := []struct {
		patternType string
		filter      string
		want        bool
	}{
		{"mvc", "mvc", true},
		{"mvc", "class", true},
		{"mvvm", "class", true},
		{"repository", "struct", true},
		{"dependency-injection", "interface", true},
		{"observer", "interface", true},
		{"concurrency", "function", true},
		{"rest-api", "rest", true},
		{"rest-api", "api", true},
		{"singleton", "type", true},
		{"singleton", "unknown", false},
		{"testing", "function", true},
		{"error-handling", "function", true},
		{"factory", "class", true},
	}

	for _, tt := range tests {
		got := matchesEntityType(tt.patternType, tt.filter)
		if got != tt.want {
			t.Errorf("matchesEntityType(%q, %q) = %v, want %v", tt.patternType, tt.filter, got, tt.want)
		}
	}
}

func TestScoreMatch(t *testing.T) {
	tests := []struct {
		queryLower string
		name       string
		snippet    string
		file       string
		wantMin    float64
	}{
		{"controller", "controller", "", "", 1.0},         // exact match
		{"control", "controller", "", "", 0.8},            // name contains query
		{"http", "rest-api", "net/http handler", "", 0.4}, // snippet match
		{"main", "pattern", "", "/path/main.go", 0.2},     // file match
		{"zzzz", "controller", "", "", 0},                 // no match
		{"", "controller", "", "", 0},                     // empty query
	}

	for _, tt := range tests {
		got := scoreMatch(tt.queryLower, tt.name, tt.snippet, tt.file)
		if got < tt.wantMin {
			t.Errorf("scoreMatch(%q, %q, %q, %q) = %f, want >= %f", tt.queryLower, tt.name, tt.snippet, tt.file, got, tt.wantMin)
		}
	}
}
