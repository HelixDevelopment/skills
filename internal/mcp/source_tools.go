package mcp

// ============================================================================
// Skill Source MCP Tools
//
// Three tools that enable AI agents to manage skill sources — external
// repositories or filesystem paths from which SKILL.md files are ingested
// into the skill graph. These tools close gap G86 in the GAPS register.
//
// Each source is registered with a unique name, a type (github, filesystem,
// or url), and a type-specific configuration blob. The source_list tool
// enumerates registered sources, and source_sync triggers a rescan of a
// single source's SKILL.md files via the sync orchestrator (G82).
//
// All state is persisted in the skill_sources database table via
// internal/skillsource.Store (G74). The sync pipeline
// (fetch → parse → map → dedup → import) is coordinated by
// internal/skillsource.Orchestrator (G82).
// ============================================================================

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/HelixDevelopment/skills/internal/skillsource"
	"github.com/google/uuid"
	mcp_go "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Source data model
// ---------------------------------------------------------------------------

// SourceType classifies where a skill source's content lives.
type SourceType string

const (
	SourceTypeGitHub     SourceType = "github"
	SourceTypeFilesystem SourceType = "filesystem"
	SourceTypeURL        SourceType = "url"
)

// SourceStatus tracks the lifecycle state of a registered skill source.
type SourceStatus string

const (
	SourceStatusActive   SourceStatus = "active"
	SourceStatusSyncing  SourceStatus = "syncing"
	SourceStatusError    SourceStatus = "error"
	SourceStatusDisabled SourceStatus = "disabled"
)

// SkillSource is one registered skill source. It mirrors the fields the
// not-yet-implemented skill_sources database table will carry, held in
// memory for now.
type SkillSource struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	SourceType SourceType             `json:"source_type"`
	Config     map[string]interface{} `json:"config"`
	Status     SourceStatus           `json:"status"`
	LastSyncAt *time.Time             `json:"last_sync_at,omitempty"`
	LastError  string                 `json:"last_error,omitempty"`
	SkillCount int                    `json:"skill_count"`
	Enabled    bool                   `json:"enabled"`
	CreatedAt  time.Time              `json:"created_at"`
	UpdatedAt  time.Time              `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// In-memory source store
// ---------------------------------------------------------------------------

// sourceStore is an in-memory registry of skill sources, keyed by source ID.
// It is populated by source_register and queried by source_list / source_sync.
// The mutex guards concurrent access from multiple MCP tool calls.
type sourceStore struct {
	mu      sync.RWMutex
	entries map[string]*SkillSource
	nextSeq int
}

// newSourceStore creates an empty source store.
func newSourceStore() *sourceStore {
	return &sourceStore{
		entries: make(map[string]*SkillSource),
		nextSeq: 1,
	}
}

// insert adds a new source to the store. The caller must hold no lock.
func (st *sourceStore) insert(src *SkillSource) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.entries[src.ID] = src
}

// get retrieves a source by ID. Returns nil if not found.
func (st *sourceStore) get(id string) *SkillSource {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.entries[id]
}

// getByName retrieves a source by its human-readable name. Returns nil if
// not found.
func (st *sourceStore) getByName(name string) *SkillSource {
	st.mu.RLock()
	defer st.mu.RUnlock()
	for _, src := range st.entries {
		if src.Name == name {
			return src
		}
	}
	return nil
}

// list returns all sources, optionally filtered to enabled-only.
func (st *sourceStore) list(enabledOnly bool) []*SkillSource {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]*SkillSource, 0, len(st.entries))
	for _, src := range st.entries {
		if enabledOnly && !src.Enabled {
			continue
		}
		out = append(out, src)
	}
	return out
}

// nextID generates a monotonically increasing source ID.
func (st *sourceStore) nextID() string {
	st.mu.Lock()
	defer st.mu.Unlock()
	id := fmt.Sprintf("src_%d", st.nextSeq)
	st.nextSeq++
	return id
}

// updateSync records the outcome of a sync attempt on the given source.
func (st *sourceStore) updateSync(id string, success bool, errMsg string, skillCount int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	src, ok := st.entries[id]
	if !ok {
		return
	}
	now := time.Now().UTC()
	src.UpdatedAt = now
	if success {
		src.Status = SourceStatusActive
		src.LastError = ""
		src.LastSyncAt = &now
		src.SkillCount = skillCount
	} else {
		src.Status = SourceStatusError
		src.LastError = errMsg
	}
}

// ---------------------------------------------------------------------------
// Tool 11: source_register — Register a new skill source
// ---------------------------------------------------------------------------

func (s *MCPServer) registerSourceRegister() {
	tool := mcp_go.NewTool("source_register",
		mcp_go.WithDescription(
			"Register a new skill source — a GitHub repository, local filesystem path, "+
				"or URL from which SKILL.md files are ingested into the skill graph. "+
				"The source is created in an enabled state and can be synced via "+
				"source_sync. Each source must have a unique name.",
		),
		mcp_go.WithString("name",
			mcp_go.Required(),
			mcp_go.Description("Human-readable name for this source (must be unique, e.g. 'awesome-skills-repo')"),
		),
		mcp_go.WithString("source_type",
			mcp_go.Required(),
			mcp_go.Description("Type of source: 'github' (GitHub repository), 'filesystem' (local directory), or 'url' (remote URL)"),
		),
		mcp_go.WithObject("config",
			mcp_go.Required(),
			mcp_go.Description("Type-specific configuration. For github: {owner, repo, ref?, path?, token?}. "+
				"For filesystem: {root_path}. For url: {base_url, pattern?}."),
		),
	)

	s.server.AddTool(tool, func(ctx context.Context, request mcp_go.CallToolRequest) (*mcp_go.CallToolResult, error) {
		name, _ := request.GetArguments()["name"].(string)
		if name = strings.TrimSpace(name); name == "" {
			return s.newToolError("name parameter is required"), nil
		}

		sourceTypeRaw, _ := request.GetArguments()["source_type"].(string)
		sourceType := skillsource.SourceType(strings.ToLower(strings.TrimSpace(sourceTypeRaw)))
		if !sourceType.IsValid() {
			return s.newToolError(
				fmt.Sprintf("source_type must be one of 'github', 'filesystem', 'url'; got %q", sourceTypeRaw),
			), nil
		}

		configRaw, ok := request.GetArguments()["config"]
		if !ok {
			return s.newToolError("config parameter is required"), nil
		}
		configMap, ok := configRaw.(map[string]interface{})
		if !ok {
			return s.newToolError("config must be a JSON object"), nil
		}

		// Type-specific config validation.
		if err := validateSourceConfig(SourceType(sourceType), configMap); err != nil {
			return s.newToolError(fmt.Sprintf("invalid config: %v", err)), nil
		}

		// Marshal config to JSON for DB storage.
		configJSON, err := json.Marshal(configMap)
		if err != nil {
			return s.newToolError(fmt.Sprintf("failed to marshal config: %v", err)), nil
		}

		src := &skillsource.SkillSource{
			Name:       name,
			SourceType: sourceType,
			Config:     configJSON,
			Enabled:    true,
		}

		if err := s.skillSourceStore.Create(ctx, src); err != nil {
			if errors.Is(err, skillsource.ErrSourceExists) {
				return s.newToolError(fmt.Sprintf("a source named %q already exists", name)), nil
			}
			return s.newToolError(fmt.Sprintf("failed to create source: %v", err)), nil
		}

		s.logger.Info("source registered",
			zap.String("id", src.ID.String()),
			zap.String("name", src.Name),
			zap.String("type", string(src.SourceType)),
		)

		return s.newToolResult(map[string]interface{}{
			"success":     true,
			"message":     fmt.Sprintf("Source %q registered successfully", name),
			"source_id":   src.ID.String(),
			"name":        src.Name,
			"source_type": string(src.SourceType),
			"status":      string(src.SyncStatus),
			"created_at":  src.CreatedAt,
		}), nil
	})
}

// ---------------------------------------------------------------------------
// Tool 12: source_list — List registered skill sources
// ---------------------------------------------------------------------------

func (s *MCPServer) registerSourceList() {
	tool := mcp_go.NewTool("source_list",
		mcp_go.WithDescription(
			"List all registered skill sources with their current status, "+
				"last sync time, and skill count. Optionally filter to only enabled sources.",
		),
		mcp_go.WithBoolean("enabled_only",
			mcp_go.Description("If true, return only enabled sources (default: false, returns all)"),
			mcp_go.DefaultBool(false),
		),
	)

	s.server.AddTool(tool, func(ctx context.Context, request mcp_go.CallToolRequest) (*mcp_go.CallToolResult, error) {
		enabledOnly := false
		if eo, ok := request.GetArguments()["enabled_only"]; ok {
			if eb, ok := eo.(bool); ok {
				enabledOnly = eb
			}
		}

		s.logger.Debug("source_list", zap.Bool("enabled_only", enabledOnly))

		sources, err := s.skillSourceStore.List(ctx, enabledOnly)
		if err != nil {
			return s.newToolError(fmt.Sprintf("failed to list sources: %v", err)), nil
		}

		type sourceInfo struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			SourceType string `json:"source_type"`
			Status     string `json:"status"`
			Enabled    bool   `json:"enabled"`
			LastSyncAt string `json:"last_sync_at,omitempty"`
			ErrorMsg   string `json:"error_message,omitempty"`
			CreatedAt  string `json:"created_at"`
		}

		items := make([]sourceInfo, 0, len(sources))
		for _, src := range sources {
			si := sourceInfo{
				ID:         src.ID.String(),
				Name:       src.Name,
				SourceType: string(src.SourceType),
				Status:     string(src.SyncStatus),
				Enabled:    src.Enabled,
				CreatedAt:  src.CreatedAt.Format(time.RFC3339),
			}
			if src.LastSync != nil {
				si.LastSyncAt = src.LastSync.Format(time.RFC3339)
			}
			if src.ErrorMessage != "" {
				si.ErrorMsg = src.ErrorMessage
			}
			items = append(items, si)
		}

		return s.newToolResult(map[string]interface{}{
			"count":   len(items),
			"sources": items,
		}), nil
	})
}

// ---------------------------------------------------------------------------
// Tool 13: source_sync — Trigger a sync/rescan for a specific source
// ---------------------------------------------------------------------------

func (s *MCPServer) registerSourceSync() {
	tool := mcp_go.NewTool("source_sync",
		mcp_go.WithDescription(
			"Trigger a sync/rescan of a registered skill source. This re-fetches "+
				"SKILL.md files from the source (GitHub repo, filesystem path, or URL), "+
				"parses them, and imports new or updated skills into the graph. "+
				"Use source_list to find the source_id.",
		),
		mcp_go.WithString("source_id",
			mcp_go.Required(),
			mcp_go.Description("The ID of the source to sync (e.g. 'src_1'), as returned by source_register or source_list"),
		),
	)

	s.server.AddTool(tool, func(ctx context.Context, request mcp_go.CallToolRequest) (*mcp_go.CallToolResult, error) {
		sourceID, _ := request.GetArguments()["source_id"].(string)
		if sourceID = strings.TrimSpace(sourceID); sourceID == "" {
			return s.newToolError("source_id parameter is required"), nil
		}

		id, err := uuid.Parse(sourceID)
		if err != nil {
			return s.newToolError(fmt.Sprintf("invalid source_id %q: %v", sourceID, err)), nil
		}

		// Load source to get its name for the response.
		src, err := s.skillSourceStore.GetByID(ctx, id)
		if err != nil {
			if errors.Is(err, skillsource.ErrSourceNotFound) {
				return s.newToolError(fmt.Sprintf("source %q not found", sourceID)), nil
			}
			return s.newToolError(fmt.Sprintf("failed to load source: %v", err)), nil
		}

		if !src.Enabled {
			return s.newToolError(fmt.Sprintf("source %q (%s) is disabled; enable it before syncing", src.Name, sourceID)), nil
		}

		if src.SyncStatus == skillsource.SyncStatusSyncing {
			return s.newToolError(fmt.Sprintf("source %q (%s) is already syncing", src.Name, sourceID)), nil
		}

		s.logger.Info("source_sync started",
			zap.String("id", src.ID.String()),
			zap.String("name", src.Name),
			zap.String("type", string(src.SourceType)),
		)

		// Run the full sync pipeline via the orchestrator (G82).
		result, syncErr := s.sourceOrchestrator.SyncSource(ctx, id)

		if syncErr != nil {
			s.logger.Error("source_sync failed",
				zap.String("id", src.ID.String()),
				zap.String("name", src.Name),
				zap.Error(syncErr),
			)
			return s.newToolResult(map[string]interface{}{
				"success":   false,
				"source_id": sourceID,
				"name":      src.Name,
				"error":     syncErr.Error(),
				"status":    string(skillsource.SyncStatusFailed),
			}), nil
		}

		s.logger.Info("source_sync completed",
			zap.String("id", src.ID.String()),
			zap.String("name", src.Name),
			zap.Int("fetched", result.Fetched),
			zap.Int("parsed", result.Parsed),
			zap.Int("imported", result.Imported),
		)

		return s.newToolResult(map[string]interface{}{
			"success":     true,
			"source_id":   sourceID,
			"name":        src.Name,
			"fetched":     result.Fetched,
			"parsed":      result.Parsed,
			"imported":    result.Imported,
			"skipped":     result.Skipped,
			"errors":      result.Errors,
			"status":      string(skillsource.SyncStatusCompleted),
			"duration_ms": result.Duration.Milliseconds(),
		}), nil
	})
}

// ---------------------------------------------------------------------------
// Config validation helpers
// ---------------------------------------------------------------------------

// validateSourceConfig checks that the config map contains the required
// fields for the given source type. Returns nil on success.
func validateSourceConfig(st SourceType, config map[string]interface{}) error {
	switch st {
	case SourceTypeGitHub:
		return validateGitHubConfig(config)
	case SourceTypeFilesystem:
		return validateFilesystemConfig(config)
	case SourceTypeURL:
		return validateURLConfig(config)
	default:
		return fmt.Errorf("unknown source type %q", st)
	}
}

// validateGitHubConfig checks that a github source config has the required
// fields: owner (non-empty string) and repo (non-empty string). Optional
// fields: ref (defaults to "main"), path (subdirectory filter), token
// (GitHub API token — callers SHOULD use environment variables instead of
// embedding a token in the config, per §11.4.10).
func validateGitHubConfig(config map[string]interface{}) error {
	owner, _ := config["owner"].(string)
	if strings.TrimSpace(owner) == "" {
		return fmt.Errorf("github config requires a non-empty 'owner' field")
	}
	repo, _ := config["repo"].(string)
	if strings.TrimSpace(repo) == "" {
		return fmt.Errorf("github config requires a non-empty 'repo' field")
	}
	// ref is optional — defaults to "main" at sync time.
	// path is optional — nil means scan the entire repo.
	// token is optional — nil means unauthenticated (rate-limited).
	return nil
}

// validateFilesystemConfig checks that a filesystem source config has the
// required field: root_path (non-empty string).
func validateFilesystemConfig(config map[string]interface{}) error {
	rootPath, _ := config["root_path"].(string)
	if strings.TrimSpace(rootPath) == "" {
		return fmt.Errorf("filesystem config requires a non-empty 'root_path' field")
	}
	return nil
}

// validateURLConfig checks that a url source config has the required field:
// base_url (non-empty string, valid URL). Optional: pattern (glob/regex
// for filtering).
func validateURLConfig(config map[string]interface{}) error {
	baseURL, _ := config["base_url"].(string)
	if strings.TrimSpace(baseURL) == "" {
		return fmt.Errorf("url config requires a non-empty 'base_url' field")
	}
	if _, err := url.ParseRequestURI(baseURL); err != nil {
		return fmt.Errorf("url config 'base_url' is not a valid URL: %v", err)
	}
	return nil
}
