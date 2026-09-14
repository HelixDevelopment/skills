package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/HelixDevelopment/skills/internal/codeanalysis"
	"github.com/HelixDevelopment/skills/internal/models"

	mcp_go "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// ============================================================================
// Tool 1: skill_search - Vector/hybrid search across skills
// ============================================================================

func (s *MCPServer) registerSkillSearch() {
	tool := mcp_go.NewTool("skill_search",
		mcp_go.WithDescription(
			"Search the skill graph using text or vector similarity. "+
				"Returns skills matching the query with relevance scores. "+
				"Use this to find relevant skills when answering questions or solving problems. "+
				"Score scale note (§G29): when no embedding is populated for any matching "+
				"skill, score is a pg_trgm text-similarity value in [0, 1]. Once semantic "+
				"(vector) recall is active for a query, score instead becomes a fused "+
				"Reciprocal Rank Fusion relevance value, typically in the much smaller "+
				"~0.01-0.03 range -- always compare scores WITHIN one response's results "+
				"(relative ranking), never against a fixed absolute threshold from a "+
				"previous call.",
		),
		mcp_go.WithString("query",
			mcp_go.Required(),
			mcp_go.Description("Search query text - can be keywords, natural language, or skill name"),
		),
		mcp_go.WithNumber("limit",
			mcp_go.Description("Maximum number of results to return (default: 5, max: 50)"),
			mcp_go.DefaultNumber(5),
		),
	)

	s.server.AddTool(tool, func(ctx context.Context, request mcp_go.CallToolRequest) (*mcp_go.CallToolResult, error) {
		query, _ := request.GetArguments()["query"].(string)
		if query == "" {
			return s.newToolError("query parameter is required"), nil
		}

		limit := 5
		if l, ok := request.GetArguments()["limit"]; ok {
			if lf, ok := l.(float64); ok {
				limit = int(lf)
				if limit < 1 {
					limit = 1
				}
				if limit > 50 {
					limit = 50
				}
			}
		}

		s.logger.Debug("skill_search", zap.String("query", query), zap.Int("limit", limit))

		results, err := s.skillStore.Search(ctx, query, limit)
		if err != nil {
			s.logger.Error("skill_search failed", zap.Error(err))
			return s.newToolError(fmt.Sprintf("search failed: %v", err)), nil
		}

		type resultItem struct {
			Name        string  `json:"name"`
			Title       string  `json:"title"`
			Description string  `json:"description"`
			Status      string  `json:"status"`
			Score       float64 `json:"score"`
		}

		items := make([]resultItem, 0, len(results))
		for _, r := range results {
			items = append(items, resultItem{
				Name:        r.Skill.Name,
				Title:       r.Skill.Title,
				Description: r.Skill.Description,
				Status:      string(r.Skill.Status),
				Score:       r.Score,
			})
		}

		if len(items) == 0 {
			return s.newToolResultRaw(`{"results": [], "message": "No skills found matching the query."}`), nil
		}

		return s.newToolResult(map[string]interface{}{
			"query":   query,
			"count":   len(items),
			"results": items,
		}), nil
	})
}

// ============================================================================
// Tool 2: skill_get - Retrieve full skill by name
// ============================================================================

func (s *MCPServer) registerSkillGet() {
	tool := mcp_go.NewTool("skill_get",
		mcp_go.WithDescription(
			"Retrieve the complete skill record including dependencies and resources. "+
				"Use the exact skill name (from skill_search results).",
		),
		mcp_go.WithString("name",
			mcp_go.Required(),
			mcp_go.Description("Exact skill name (e.g., 'go-concurrency-patterns')"),
		),
	)

	s.server.AddTool(tool, func(ctx context.Context, request mcp_go.CallToolRequest) (*mcp_go.CallToolResult, error) {
		name, _ := request.GetArguments()["name"].(string)
		if name == "" {
			return s.newToolError("name parameter is required"), nil
		}

		s.logger.Debug("skill_get", zap.String("name", name))

		skill, err := s.skillStore.GetByName(ctx, name)
		if err != nil {
			s.logger.Error("skill_get failed", zap.Error(err))
			return s.newToolError(fmt.Sprintf("failed to get skill: %v", err)), nil
		}

		type depInfo struct {
			Name         string `json:"name"`
			Title        string `json:"title"`
			RelationType string `json:"relation_type"`
		}

		type resInfo struct {
			URL          string `json:"url"`
			Title        string `json:"title"`
			ResourceType string `json:"resource_type"`
		}

		deps := make([]depInfo, 0, len(skill.Dependencies))
		for _, d := range skill.Dependencies {
			deps = append(deps, depInfo{
				Name:         d.DependsOnName,
				Title:        d.DependsOnTitle,
				RelationType: string(d.RelationType),
			})
		}

		resources := make([]resInfo, 0, len(skill.Resources))
		for _, r := range skill.Resources {
			resources = append(resources, resInfo{
				URL:          r.URL,
				Title:        r.Title,
				ResourceType: r.ResourceType,
			})
		}

		var metadata models.SkillMetadata
		if skill.Metadata != nil {
			_ = json.Unmarshal(skill.Metadata, &metadata)
		}

		return s.newToolResult(map[string]interface{}{
			"name":         skill.Name,
			"version":      skill.Version,
			"title":        skill.Title,
			"description":  skill.Description,
			"content":      skill.Content,
			"status":       string(skill.Status),
			"metadata":     metadata,
			"dependencies": deps,
			"resources":    resources,
			"created_at":   skill.CreatedAt,
			"updated_at":   skill.UpdatedAt,
		}), nil
	})
}

// ============================================================================
// Tool 3: skill_tree - Get dependency tree
// ============================================================================

func (s *MCPServer) registerSkillTree() {
	tool := mcp_go.NewTool("skill_tree",
		mcp_go.WithDescription(
			"Get the full dependency tree for a skill, showing requires/extends/recommends relationships. "+
				"Useful for understanding prerequisites and related skills.",
		),
		mcp_go.WithString("name",
			mcp_go.Required(),
			mcp_go.Description("Root skill name to build the tree from"),
		),
		mcp_go.WithNumber("depth",
			mcp_go.Description("Maximum tree depth to traverse (default: 5, max: 10)"),
			mcp_go.DefaultNumber(5),
		),
	)

	s.server.AddTool(tool, func(ctx context.Context, request mcp_go.CallToolRequest) (*mcp_go.CallToolResult, error) {
		name, _ := request.GetArguments()["name"].(string)
		if name == "" {
			return s.newToolError("name parameter is required"), nil
		}

		depth := 5
		if d, ok := request.GetArguments()["depth"]; ok {
			if df, ok := d.(float64); ok {
				depth = int(df)
				if depth < 1 {
					depth = 1
				}
				if depth > 10 {
					depth = 10
				}
			}
		}

		s.logger.Debug("skill_tree", zap.String("name", name), zap.Int("depth", depth))

		// G06: use the canonical recursive-CTE GetDependencyTree, which assembles
		// the FULL N-level dependency tree cycle-guarded (see
		// internal/skill/graph.go). serializeTreeNode recurses over node.Children,
		// so the whole nested tree -- not just depth-1 -- reaches the MCP client.
		tree, err := s.skillStore.GetDependencyTree(ctx, name, depth)
		if err != nil {
			s.logger.Error("skill_tree failed", zap.Error(err))
			return s.newToolError(fmt.Sprintf("failed to get skill tree: %v", err)), nil
		}

		return s.newToolResult(serializeTreeNode(tree)), nil
	})
}

// serializeTreeNode converts a SkillTreeNode to a serializable map.
func serializeTreeNode(node *models.SkillTreeNode) map[string]interface{} {
	children := make([]map[string]interface{}, 0, len(node.Children))
	for _, child := range node.Children {
		children = append(children, serializeTreeNode(&child))
	}

	var metadata models.SkillMetadata
	if node.Skill.Metadata != nil {
		_ = json.Unmarshal(node.Skill.Metadata, &metadata)
	}

	return map[string]interface{}{
		"name":        node.Skill.Name,
		"title":       node.Skill.Title,
		"description": node.Skill.Description,
		"status":      string(node.Skill.Status),
		"depth":       node.Depth,
		"metadata":    metadata,
		"children":    children,
	}
}

// ============================================================================
// Tool 4: skill_create - Create a new skill (agents contribute knowledge!)
// ============================================================================

func (s *MCPServer) registerSkillCreate() {
	tool := mcp_go.NewTool("skill_create",
		mcp_go.WithDescription(
			"Create or update a skill in the knowledge graph. "+
				"Skills use TOML format with name, version, title, description, content, metadata, "+
				"dependencies (requires/extends/recommends), and resources. "+
				"Use skill_get to see examples of existing skills.",
		),
		mcp_go.WithString("toml",
			mcp_go.Required(),
			mcp_go.Description("Complete TOML-formatted skill definition"),
		),
	)

	s.server.AddTool(tool, func(ctx context.Context, request mcp_go.CallToolRequest) (*mcp_go.CallToolResult, error) {
		tomlStr, _ := request.GetArguments()["toml"].(string)
		if tomlStr == "" {
			return s.newToolError("toml parameter is required"), nil
		}

		s.logger.Debug("skill_create", zap.Int("toml_length", len(tomlStr)))

		// Fail-closed, NON-EXECUTING validation BEFORE persistence (§G03
		// request-path): screens resource URLs (SSRF, §G21) and records a real
		// verdict. The skill is persisted as `draft`; it is promoted to
		// `validated`/`active` only through the validation lifecycle, never
		// auto-approved on creation.
		validationSummary := s.validateForCreate(ctx, []byte(tomlStr))

		// Parse and create skill from TOML (persisted as draft).
		skill, err := s.skillStore.ImportFromTOML(ctx, []byte(tomlStr))
		if err != nil {
			s.logger.Error("skill_create failed", zap.Error(err))
			return s.newToolError(fmt.Sprintf("Failed to create skill: %v", err)), nil
		}

		return s.newToolResult(map[string]interface{}{
			"success":    true,
			"message":    fmt.Sprintf("Skill '%s' created successfully", skill.Name),
			"skill_id":   skill.ID,
			"skill_name": skill.Name,
			"title":      skill.Title,
			"version":    skill.Version,
			"status":     string(skill.Status),
			"validation": validationSummary,
		}), nil
	})
}

// ============================================================================
// Tool 5: learn_from_project - Submit a project path for analysis
// ============================================================================

func (s *MCPServer) registerLearnFromProject() {
	tool := mcp_go.NewTool("learn_from_project",
		mcp_go.WithDescription(
			"Submit a codebase path for automated skill extraction and learning. "+
				"The system will analyze source files, identify patterns, and create/update skills. "+
				"Specify languages to focus the analysis (e.g., go, python, rust).",
		),
		mcp_go.WithString("project_path",
			mcp_go.Required(),
			mcp_go.Description("Absolute or relative path to the project directory"),
		),
		mcp_go.WithArray("languages",
			mcp_go.Description("Programming languages to analyze (e.g., [\"go\", \"python\"])"),
			mcp_go.DefaultArray([]interface{}{}),
		),
	)

	s.server.AddTool(tool, func(ctx context.Context, request mcp_go.CallToolRequest) (*mcp_go.CallToolResult, error) {
		projectPath, _ := request.GetArguments()["project_path"].(string)
		if projectPath == "" {
			return s.newToolError("project_path parameter is required"), nil
		}

		// §G31 path-traversal / LFI guard (GAPS_AND_RISKS_REGISTER.md): reject
		// BEFORE the analyzer ever walks the filesystem, fail-closed. This is
		// the primary enforcement point -- the (currently unauthenticated) MCP
		// surface must never let a caller-supplied project_path resolve
		// outside the configured allowlisted root, whether via ".." traversal,
		// an absolute host path, or a symlink planted inside the root that
		// resolves outside it. See also the defense-in-depth check inside
		// codeanalysis.Analyzer.AnalyzeProject itself.
		canonPath, err := codeanalysis.ValidateProjectPath(projectPath, s.cfg.CodeAnalysis.AllowedRoot)
		if err != nil {
			s.logger.Warn("learn_from_project rejected project_path",
				zap.String("path", projectPath),
				zap.Error(err),
			)
			return s.newToolError(fmt.Sprintf("Rejected project_path: %v", err)), nil
		}
		projectPath = canonPath

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

		s.logger.Debug("learn_from_project",
			zap.String("path", projectPath),
			zap.Strings("languages", languages),
		)

		job, err := s.skillStore.SubmitLearningJob(ctx, projectPath, languages)
		if err != nil {
			s.logger.Error("learn_from_project failed", zap.Error(err))
			return s.newToolError(fmt.Sprintf("Failed to submit learning job: %v", err)), nil
		}

		return s.newToolResult(map[string]interface{}{
			"success":      true,
			"message":      "Learning job submitted successfully",
			"job_id":       job.ID,
			"project_path": projectPath,
			"languages":    languages,
			"status":       job.Status,
		}), nil
	})
}

// ============================================================================
// Tool 6: missing_skills - List gaps in the knowledge graph
// ============================================================================

func (s *MCPServer) registerMissingSkills() {
	tool := mcp_go.NewTool("missing_skills",
		mcp_go.WithDescription(
			"Find gaps in the knowledge graph - skills that are missing dependencies "+
				"or have incomplete coverage. Use this to identify areas that need expansion. "+
				"Optionally filter by domain.",
		),
		mcp_go.WithString("domain",
			mcp_go.Description("Filter by domain (e.g., 'backend', 'frontend', 'devops') - optional"),
		),
	)

	s.server.AddTool(tool, func(ctx context.Context, request mcp_go.CallToolRequest) (*mcp_go.CallToolResult, error) {
		domain := ""
		if d, ok := request.GetArguments()["domain"]; ok {
			domain, _ = d.(string)
		}

		s.logger.Debug("missing_skills", zap.String("domain", domain))

		entries, err := s.skillStore.GetMissingSkills(ctx, domain)
		if err != nil {
			s.logger.Error("missing_skills failed", zap.Error(err))
			return s.newToolError(fmt.Sprintf("Failed to get missing skills: %v", err)), nil
		}

		type entryInfo struct {
			SkillName   string   `json:"skill_name"`
			MissingDeps []string `json:"missing_dependencies"`
			Stale       bool     `json:"stale"`
			Coverage    float64  `json:"coverage"`
		}

		items := make([]entryInfo, 0, len(entries))
		for _, e := range entries {
			items = append(items, entryInfo{
				SkillName:   e.SkillName,
				MissingDeps: e.MissingDeps,
				Stale:       e.Stale,
				Coverage:    e.Coverage,
			})
		}

		if len(items) == 0 {
			return s.newToolResult(map[string]interface{}{
				"domain":  domain,
				"count":   0,
				"message": "No gaps found! All skills have complete dependencies.",
			}), nil
		}

		return s.newToolResult(map[string]interface{}{
			"domain":         domain,
			"count":          len(items),
			"missing_skills": items,
		}), nil
	})
}

// ============================================================================
// Tool 7: get_coverage - Get coverage report for a domain
// ============================================================================

func (s *MCPServer) registerGetCoverage() {
	tool := mcp_go.NewTool("get_coverage",
		mcp_go.WithDescription(
			"Get a comprehensive coverage report for a domain or the entire skill graph. "+
				"Shows total skills, dependency coverage, evidence coverage, and identifies gaps. "+
				"Use this to assess the health of the knowledge graph.",
		),
		mcp_go.WithString("domain",
			mcp_go.Description("Domain to report on (e.g., 'backend', 'frontend') - leave empty for all domains"),
		),
	)

	s.server.AddTool(tool, func(ctx context.Context, request mcp_go.CallToolRequest) (*mcp_go.CallToolResult, error) {
		domain := ""
		if d, ok := request.GetArguments()["domain"]; ok {
			domain, _ = d.(string)
		}

		s.logger.Debug("get_coverage", zap.String("domain", domain))

		report, err := s.registry.GetCoverageReport(ctx, domain)
		if err != nil {
			s.logger.Error("get_coverage failed", zap.Error(err))
			return s.newToolError(fmt.Sprintf("Failed to get coverage report: %v", err)), nil
		}

		return s.newToolResult(report), nil
	})
}
