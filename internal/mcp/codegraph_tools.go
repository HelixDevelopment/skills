package mcp

// ============================================================================
// CodeGraph MCP Tools
//
// Three tools that expose the codeanalysis package capabilities via MCP,
// enabling AI agents to analyze codebases, search extracted entities, and
// retrieve code graph statistics. Each tool that accepts a project_path
// enforces the §G31 path-traversal guard (ValidateProjectPath) before any
// filesystem walk.
// ============================================================================

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/HelixDevelopment/skills/internal/codeanalysis"

	mcp_go "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// codeGraphResult stores a single project's analysis result keyed by its
// canonical project path. The mutex guards concurrent access from multiple
// MCP tool calls.
type codeGraphResult struct {
	mu     sync.RWMutex
	result *codeanalysis.AnalysisResult
}

// codeGraphStore is an in-memory store of code graph analysis results, keyed
// by canonical project path. It is populated by codegraph_analyze and queried
// by codegraph_search and codegraph_stats.
type codeGraphStore struct {
	mu      sync.RWMutex
	entries map[string]*codeGraphResult
}

// newCodeGraphStore creates an empty code graph store.
func newCodeGraphStore() *codeGraphStore {
	return &codeGraphStore{
		entries: make(map[string]*codeGraphResult),
	}
}

// put stores or replaces an analysis result for the given project path.
func (s *codeGraphStore) put(projectPath string, result *codeanalysis.AnalysisResult) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[projectPath]
	if !ok {
		entry = &codeGraphResult{}
		s.entries[projectPath] = entry
	}
	entry.mu.Lock()
	entry.result = result
	entry.mu.Unlock()
}

// get retrieves the analysis result for the given project path.
// Returns nil if no result exists.
func (s *codeGraphStore) get(projectPath string) *codeanalysis.AnalysisResult {
	s.mu.RLock()
	entry, ok := s.entries[projectPath]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	entry.mu.RLock()
	defer entry.mu.RUnlock()
	return entry.result
}

// listPaths returns all stored project paths.
func (s *codeGraphStore) listPaths() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	paths := make([]string, 0, len(s.entries))
	for p := range s.entries {
		paths = append(paths, p)
	}
	return paths
}

// ============================================================================
// Tool 8: codegraph_analyze - Analyze a project and build its code graph
// ============================================================================

func (s *MCPServer) registerCodeGraphAnalyze() {
	tool := mcp_go.NewTool("codegraph_analyze",
		mcp_go.WithDescription(
			"Analyze a project directory and build its code graph. "+
				"Returns entities (functions, classes, types, interfaces, structs), "+
				"relationships (imports, pattern co-occurrence), and file-level metrics "+
				"(language breakdown, file counts). Results are cached in memory for "+
				"subsequent codegraph_search and codegraph_stats queries. "+
				"The project_path is validated against the configured allowed root "+
				"(§G31 path-traversal guard) before any filesystem walk begins.",
		),
		mcp_go.WithString("project_path",
			mcp_go.Required(),
			mcp_go.Description("Absolute or relative path to the project directory to analyze"),
		),
		mcp_go.WithArray("languages",
			mcp_go.Description("Programming languages to analyze (e.g., [\"go\", \"python\"]). "+
				"Omit to analyze all supported languages."),
			mcp_go.DefaultArray([]interface{}{}),
		),
	)

	s.server.AddTool(tool, func(ctx context.Context, request mcp_go.CallToolRequest) (*mcp_go.CallToolResult, error) {
		projectPath, _ := request.GetArguments()["project_path"].(string)
		if projectPath == "" {
			return s.newToolError("project_path parameter is required"), nil
		}

		// §G31 path-traversal / LFI guard: reject BEFORE the analyzer ever
		// walks the filesystem, fail-closed.
		canonPath, err := codeanalysis.ValidateProjectPath(projectPath, s.cfg.CodeAnalysis.AllowedRoot)
		if err != nil {
			s.logger.Warn("codegraph_analyze rejected project_path",
				zap.String("path", projectPath),
				zap.Error(err),
			)
			return s.newToolError(fmt.Sprintf("Rejected project_path: %v", err)), nil
		}
		projectPath = canonPath

		// Parse optional languages filter.
		var languages []string
		if langs, ok := request.GetArguments()["languages"]; ok {
			if langArr, ok := langs.([]interface{}); ok {
				for _, l := range langArr {
					if ls, ok := l.(string); ok && ls != "" {
						languages = append(languages, ls)
					}
				}
			}
		}

		s.logger.Debug("codegraph_analyze",
			zap.String("path", projectPath),
			zap.Strings("languages", languages),
		)

		// Run the analysis.
		analyzer := codeanalysis.NewAnalyzer(s.cfg.CodeAnalysis, s.logger)
		result, err := analyzer.AnalyzeProject(ctx, projectPath)
		if err != nil {
			s.logger.Error("codegraph_analyze failed", zap.Error(err))
			return s.newToolError(fmt.Sprintf("Analysis failed: %v", err)), nil
		}

		// Cache the result for codegraph_search / codegraph_stats.
		s.codeGraphResults.put(projectPath, result)

		// Build entity summary from analysis results.
		entityCounts := make(map[string]int)
		for _, p := range result.Patterns {
			entityCounts[p.Type]++
		}

		// Build language breakdown.
		langBreakdown := make(map[string]int)
		for lang, count := range result.Languages {
			langBreakdown[lang] = count
		}

		return s.newToolResult(map[string]interface{}{
			"success":      true,
			"project_path": projectPath,
			"summary": map[string]interface{}{
				"languages":      langBreakdown,
				"total_imports":  len(result.Imports),
				"total_patterns": len(result.Patterns),
				"entity_counts":  entityCounts,
			},
			"imports":  result.Imports,
			"patterns": result.Patterns,
		}), nil
	})
}

// ============================================================================
// Tool 9: codegraph_search - Search the code graph for entities
// ============================================================================

func (s *MCPServer) registerCodeGraphSearch() {
	tool := mcp_go.NewTool("codegraph_search",
		mcp_go.WithDescription(
			"Search the code graph for entities by name, pattern, or type. "+
				"Queries results from a previous codegraph_analyze call. "+
				"Filter by entity_type to narrow results to specific kinds of "+
				"entities (function, class, type, interface, struct). "+
				"Results are scored by relevance.",
		),
		mcp_go.WithString("query",
			mcp_go.Required(),
			mcp_go.Description("Search query - matches entity names, pattern types, and snippets"),
		),
		mcp_go.WithString("entity_type",
			mcp_go.Description("Filter by entity type: function, class, type, interface, struct, or pattern type (e.g., 'mvc', 'repository')"),
		),
		mcp_go.WithNumber("limit",
			mcp_go.Description("Maximum number of results to return (default: 10, max: 100)"),
			mcp_go.DefaultNumber(10),
		),
	)

	s.server.AddTool(tool, func(ctx context.Context, request mcp_go.CallToolRequest) (*mcp_go.CallToolResult, error) {
		query, _ := request.GetArguments()["query"].(string)
		if query == "" {
			return s.newToolError("query parameter is required"), nil
		}

		entityType := ""
		if et, ok := request.GetArguments()["entity_type"]; ok {
			entityType, _ = et.(string)
		}

		limit := 10
		if l, ok := request.GetArguments()["limit"]; ok {
			if lf, ok := l.(float64); ok {
				limit = int(lf)
				if limit < 1 {
					limit = 1
				}
				if limit > 100 {
					limit = 100
				}
			}
		}

		s.logger.Debug("codegraph_search",
			zap.String("query", query),
			zap.String("entity_type", entityType),
			zap.Int("limit", limit),
		)

		// Search across all stored analysis results.
		type searchHit struct {
			Name       string  `json:"name"`
			Type       string  `json:"type"`
			File       string  `json:"file"`
			Line       int     `json:"line"`
			Snippet    string  `json:"snippet,omitempty"`
			Confidence float64 `json:"confidence,omitempty"`
			Score      float64 `json:"score"`
			Project    string  `json:"project_path"`
		}

		var hits []searchHit
		queryLower := strings.ToLower(query)

		paths := s.codeGraphResults.listPaths()
		for _, projectPath := range paths {
			result := s.codeGraphResults.get(projectPath)
			if result == nil {
				continue
			}

			// Search patterns.
			for _, p := range result.Patterns {
				if entityType != "" && !matchesEntityType(p.Type, entityType) {
					continue
				}
				score := scoreMatch(queryLower, p.Type, p.Snippet, p.File)
				if score > 0 {
					hits = append(hits, searchHit{
						Name:       p.Type,
						Type:       "pattern:" + p.Type,
						File:       p.File,
						Line:       p.Line,
						Snippet:    p.Snippet,
						Confidence: p.Confidence,
						Score:      score,
						Project:    projectPath,
					})
				}
			}

			// Search imports.
			if entityType == "" || entityType == "import" {
				for _, imp := range result.Imports {
					score := scoreMatch(queryLower, imp.Path, "", imp.File)
					if score > 0 {
						hits = append(hits, searchHit{
							Name:    imp.Path,
							Type:    "import",
							File:    imp.File,
							Line:    imp.Line,
							Score:   score,
							Project: projectPath,
						})
					}
				}
			}
		}

		// Sort by score descending (simple insertion sort for small sets).
		for i := 1; i < len(hits); i++ {
			for j := i; j > 0 && hits[j].Score > hits[j-1].Score; j-- {
				hits[j], hits[j-1] = hits[j-1], hits[j]
			}
		}

		// Apply limit.
		if len(hits) > limit {
			hits = hits[:limit]
		}

		if len(hits) == 0 {
			return s.newToolResultRaw(`{"results": [], "message": "No entities found matching the query. Run codegraph_analyze first if you haven't yet."}`), nil
		}

		return s.newToolResult(map[string]interface{}{
			"query":       query,
			"entity_type": entityType,
			"count":       len(hits),
			"results":     hits,
		}), nil
	})
}

// ============================================================================
// Tool 10: codegraph_stats - Get code graph statistics
// ============================================================================

func (s *MCPServer) registerCodeGraphStats() {
	tool := mcp_go.NewTool("codegraph_stats",
		mcp_go.WithDescription(
			"Get code graph statistics for a previously analyzed project. "+
				"Returns entity counts by type, import counts, pattern counts, "+
				"language breakdown, and total file counts. "+
				"Requires a prior codegraph_analyze call for the project.",
		),
		mcp_go.WithString("project_path",
			mcp_go.Required(),
			mcp_go.Description("Absolute path to the project directory (must match a previous codegraph_analyze call)"),
		),
	)

	s.server.AddTool(tool, func(ctx context.Context, request mcp_go.CallToolRequest) (*mcp_go.CallToolResult, error) {
		projectPath, _ := request.GetArguments()["project_path"].(string)
		if projectPath == "" {
			return s.newToolError("project_path parameter is required"), nil
		}

		// §G31 path-traversal guard: canonicalize and validate.
		canonPath, err := codeanalysis.ValidateProjectPath(projectPath, s.cfg.CodeAnalysis.AllowedRoot)
		if err != nil {
			s.logger.Warn("codegraph_stats rejected project_path",
				zap.String("path", projectPath),
				zap.Error(err),
			)
			return s.newToolError(fmt.Sprintf("Rejected project_path: %v", err)), nil
		}
		projectPath = canonPath

		s.logger.Debug("codegraph_stats", zap.String("path", projectPath))

		result := s.codeGraphResults.get(projectPath)
		if result == nil {
			return s.newToolError(
				fmt.Sprintf("No analysis results found for %q. Run codegraph_analyze first.", projectPath),
			), nil
		}

		// Entity counts by type.
		entityCounts := make(map[string]int)
		for _, p := range result.Patterns {
			entityCounts[p.Type]++
		}

		// Total file count across all languages.
		totalFiles := 0
		for _, count := range result.Languages {
			totalFiles += count
		}

		return s.newToolResult(map[string]interface{}{
			"project_path":       projectPath,
			"total_files":        totalFiles,
			"total_imports":      len(result.Imports),
			"total_patterns":     len(result.Patterns),
			"entity_counts":      entityCounts,
			"language_breakdown": result.Languages,
		}), nil
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// matchesEntityType checks if a pattern type matches the requested entity
// type filter. Supports both direct matches ("function", "class", "struct",
// "interface") and pattern-type matches ("mvc", "repository", etc.).
func matchesEntityType(patternType, filter string) bool {
	filter = strings.ToLower(filter)
	patternType = strings.ToLower(patternType)

	// Direct match.
	if patternType == filter {
		return true
	}

	// Map entity_type filter to known pattern types.
	entityTypeMap := map[string][]string{
		"function":  {"concurrency", "error-handling", "testing"},
		"class":     {"mvc", "mvvm", "mvp", "singleton", "factory"},
		"struct":    {"repository", "factory"},
		"type":      {"mvc", "mvvm", "mvp", "singleton", "factory", "repository"},
		"interface": {"dependency-injection", "observer"},
	}

	if mapped, ok := entityTypeMap[filter]; ok {
		for _, m := range mapped {
			if patternType == m {
				return true
			}
		}
	}

	// Substring match for pattern types (e.g., "rest" matches "rest-api").
	return strings.Contains(patternType, filter) || strings.Contains(filter, patternType)
}

// scoreMatch computes a simple relevance score for a search query against
// a candidate name, snippet, and file path. Returns 0 if no match.
func scoreMatch(queryLower, name, snippet, file string) float64 {
	nameLower := strings.ToLower(name)
	snippetLower := strings.ToLower(snippet)
	fileLower := strings.ToLower(file)

	// Exact name match: highest score.
	if nameLower == queryLower {
		return 1.0
	}

	// Name contains query.
	if strings.Contains(nameLower, queryLower) {
		return 0.8
	}

	// Query contains name (short name matched by longer query).
	if strings.Contains(queryLower, nameLower) && nameLower != "" {
		return 0.6
	}

	// Snippet contains query.
	if snippetLower != "" && strings.Contains(snippetLower, queryLower) {
		return 0.4
	}

	// File path contains query.
	if strings.Contains(fileLower, queryLower) {
		return 0.2
	}

	return 0
}
