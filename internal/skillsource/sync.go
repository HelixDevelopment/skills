// Package skillsource — sync.go implements the per-source scan orchestrator
// that coordinates the end-to-end skill source sync pipeline:
//
//	fetch -> parse -> map -> dedup -> import
//
// The orchestrator is a pure coordination layer: it owns no I/O of its own
// beyond delegating to injected interfaces (SourceStore for source registry
// CRUD, SkillStore for skill import, Fetcher/Parser/Mapper for the
// pipeline stages). Every dependency is injected via an interface so the
// orchestrator is fully testable without a database, network, or filesystem.
//
// Design references:
//   - docs/source_ingestion/WIRING_PLAN.md §3.6 (scan orchestrator)
//   - docs/source_ingestion/TRACKED_ITEMS.md G82 (this file)
package skillsource

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/HelixDevelopment/skills/internal/source/github"
	"github.com/HelixDevelopment/skills/internal/source/mapper"
	"github.com/HelixDevelopment/skills/internal/source/skillmd"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Interfaces for dependency injection
// ---------------------------------------------------------------------------

// SourceStoreReader is the minimal interface the orchestrator needs from the
// skill-source registry. It is satisfied by *Store (store.go) but defined as
// an interface so tests can substitute a mock without a database.
type SourceStoreReader interface {
	// GetByID retrieves a skill source by its unique identifier.
	GetByID(ctx context.Context, id uuid.UUID) (*SkillSource, error)
	// UpdateSyncStatus changes only the sync state fields (sync_status,
	// error_message, last_sync) without touching configuration.
	UpdateSyncStatus(ctx context.Context, id uuid.UUID, status SyncStatus, errMsg string) error
}

// SkillStoreWriter is the minimal interface the orchestrator needs from the
// skill store for importing skills. It is satisfied by *skill.Store
// (internal/skill/store.go) but defined as an interface so tests can
// substitute a mock without a database.
type SkillStoreWriter interface {
	// Create inserts (or upserts) a skill. The skill's Name is used as the
	// dedup key — Create's ON CONFLICT (name) DO UPDATE clause means calling
	// Create for an existing name updates rather than errors.
	Create(ctx context.Context, skill *models.Skill) error
	// GetByName retrieves a skill by its unique name. Returns
	// skill.ErrSkillNotFound when no skill with that name exists.
	GetByName(ctx context.Context, name string) (*models.Skill, error)
}

// FetchResult is one item returned by a Fetcher — the raw content of a
// single file plus the path it was fetched from.
type FetchResult struct {
	// Path is the repo-relative or root-relative path of the file
	// (e.g. "systematic-debugging/SKILL.md").
	Path string
	// Content is the raw file bytes.
	Content []byte
}

// Fetcher fetches all SKILL.md-like files from a source. The orchestrator
// calls Fetcher once per sync, receiving all items in a single batch.
type Fetcher interface {
	// Fetch retrieves all fetchable items from the configured source.
	Fetch(ctx context.Context) ([]FetchResult, error)
}

// ---------------------------------------------------------------------------
// Orchestrator
// ---------------------------------------------------------------------------

// Orchestrator coordinates the end-to-end skill source sync pipeline:
// fetch -> parse -> map -> dedup -> import. It is a pure coordination layer
// — no I/O beyond delegating to injected interfaces.
type Orchestrator struct {
	sourceStore SourceStoreReader
	skillStore  SkillStoreWriter
	logger      *zap.Logger
	// licenseAllowlist is the set of license identifiers that permit
	// redistribution of the upstream skill body. An empty upstream license
	// (undeclared) is always treated as NOT allowed, regardless of this
	// list. See mapper.Map's own license-gate documentation.
	licenseAllowlist []string
}

// NewOrchestrator creates a new sync orchestrator. logger defaults to a
// no-op if nil.
func NewOrchestrator(sourceStore SourceStoreReader, skillStore SkillStoreWriter, logger *zap.Logger) *Orchestrator {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Orchestrator{
		sourceStore: sourceStore,
		skillStore:  skillStore,
		logger:      logger,
	}
}

// WithLicenseAllowlist configures the license allowlist used by the mapper
// stage to gate skill content redistribution. Returns the receiver for
// fluent wiring. An empty allowlist means ALL licenses are gated (no body
// redistributed).
func (o *Orchestrator) WithLicenseAllowlist(allowlist []string) *Orchestrator {
	o.licenseAllowlist = allowlist
	return o
}

// SyncResult summarizes a sync operation.
type SyncResult struct {
	// SourceID is the source that was synced.
	SourceID uuid.UUID
	// Fetched is the number of raw files retrieved from the source.
	Fetched int
	// Parsed is the number of files that were successfully parsed as
	// SKILL.md (had valid frontmatter with a name field).
	Parsed int
	// Imported is the number of skills that were created or updated in the
	// skill store (passed dedup + import).
	Imported int
	// Skipped is the number of skills skipped as duplicates (already exist
	// in the store with identical content hash — not yet implemented, see
	// note in dedupSkills).
	Skipped int
	// Errors collects non-fatal per-item error messages (a single fetch or
	// parse failure does not abort the entire sync).
	Errors []string
	// Duration is the wall-clock time for the entire sync operation.
	Duration time.Duration
}

// SyncSource runs the full pipeline for a single source identified by
// sourceID. It:
//  1. Loads the source from the registry.
//  2. Transitions the source to "syncing" status.
//  3. Dispatches to the appropriate fetcher (GitHub, filesystem, or URL).
//  4. Parses each fetched item's content as a SKILL.md.
//  5. Maps parsed skills to models.Skill via the mapper package.
//  6. Deduplicates against existing skills in the store.
//  7. Imports (creates/upserts) new or changed skills.
//  8. Transitions the source to "completed" or "failed" status.
//
// SyncSource never panics: recoverable per-item errors are collected in
// SyncResult.Errors; only a hard infrastructure failure (cannot reach the
// source store, cannot mark sync status) returns a non-nil error.
func (o *Orchestrator) SyncSource(ctx context.Context, sourceID uuid.UUID) (*SyncResult, error) {
	start := time.Now()

	result := &SyncResult{SourceID: sourceID}

	// Step 1: Load source.
	source, err := o.sourceStore.GetByID(ctx, sourceID)
	if err != nil {
		return nil, fmt.Errorf("sync: load source %s: %w", sourceID, err)
	}

	if !source.Enabled {
		o.logger.Info("sync: source is disabled, skipping",
			zap.String("source_id", sourceID.String()),
			zap.String("name", source.Name),
		)
		result.Duration = time.Since(start)
		return result, nil
	}

	// Step 2: Mark as syncing.
	if err := o.sourceStore.UpdateSyncStatus(ctx, sourceID, SyncStatusSyncing, ""); err != nil {
		return nil, fmt.Errorf("sync: mark syncing for source %s: %w", sourceID, err)
	}

	// Run the pipeline; on any hard error, mark as failed.
	result, err = o.runPipeline(ctx, source, result)
	result.Duration = time.Since(start)

	if err != nil {
		// Best-effort: mark as failed. If this also fails, log but return
		// the original pipeline error.
		if markErr := o.sourceStore.UpdateSyncStatus(ctx, sourceID, SyncStatusFailed, err.Error()); markErr != nil {
			o.logger.Error("sync: failed to mark source as failed",
				zap.String("source_id", sourceID.String()),
				zap.Error(markErr),
			)
		}
		return result, err
	}

	// Step 8: Mark as completed.
	if markErr := o.sourceStore.UpdateSyncStatus(ctx, sourceID, SyncStatusCompleted, ""); markErr != nil {
		o.logger.Error("sync: failed to mark source as completed",
			zap.String("source_id", sourceID.String()),
			zap.Error(markErr),
		)
		// Not a hard error — the sync itself succeeded.
	}

	o.logger.Info("sync: completed",
		zap.String("source_id", sourceID.String()),
		zap.String("name", source.Name),
		zap.Int("fetched", result.Fetched),
		zap.Int("parsed", result.Parsed),
		zap.Int("imported", result.Imported),
		zap.Int("skipped", result.Skipped),
		zap.Int("errors", len(result.Errors)),
		zap.Duration("duration", result.Duration),
	)

	return result, nil
}

// runPipeline executes the fetch -> parse -> map -> dedup -> import stages.
// It returns a hard error only when the pipeline cannot continue; per-item
// errors are collected in result.Errors.
func (o *Orchestrator) runPipeline(ctx context.Context, source *SkillSource, result *SyncResult) (*SyncResult, error) {
	// Stage 1: Fetch.
	items, err := o.fetchSource(ctx, source)
	if err != nil {
		return result, fmt.Errorf("fetch: %w", err)
	}
	result.Fetched = len(items)

	if len(items) == 0 {
		o.logger.Info("sync: no items fetched",
			zap.String("source_id", source.ID.String()),
		)
		return result, nil
	}

	// Stage 2: Parse.
	parsed, parseErrors := o.parseContent(items)
	result.Errors = append(result.Errors, parseErrors...)
	result.Parsed = len(parsed)

	if len(parsed) == 0 {
		o.logger.Info("sync: no items parsed successfully",
			zap.String("source_id", source.ID.String()),
		)
		return result, nil
	}

	// Stage 3: Map.
	mapped, mapErrors := o.mapSkills(parsed, source)
	result.Errors = append(result.Errors, mapErrors...)

	if len(mapped) == 0 {
		o.logger.Info("sync: no skills mapped",
			zap.String("source_id", source.ID.String()),
		)
		return result, nil
	}

	// Stage 4: Dedup.
	toImport, dedupSkipped := o.dedupSkills(ctx, mapped)
	result.Skipped += dedupSkipped

	// Stage 5: Import.
	imported, importErrors := o.importSkills(ctx, toImport)
	result.Errors = append(result.Errors, importErrors...)
	result.Imported = imported

	return result, nil
}

// ---------------------------------------------------------------------------
// Pipeline stages
// ---------------------------------------------------------------------------

// fetchSource dispatches to the appropriate fetcher based on source type and
// returns all fetched items. For GitHub sources it constructs a GitHub client,
// lists the tree, and fetches each SKILL.md blob. For filesystem sources it
// walks the directory. URL sources are not yet implemented (returns an error).
func (o *Orchestrator) fetchSource(ctx context.Context, source *SkillSource) ([]FetchResult, error) {
	switch source.SourceType {
	case SourceTypeGitHub:
		return o.fetchGitHub(ctx, source)
	case SourceTypeFilesystem:
		return o.fetchFilesystem(ctx, source)
	case SourceTypeURL:
		return nil, fmt.Errorf("URL source type not yet implemented")
	default:
		return nil, fmt.Errorf("unsupported source type: %q", source.SourceType)
	}
}

// fetchGitHub fetches SKILL.md files from a GitHub repository.
func (o *Orchestrator) fetchGitHub(ctx context.Context, source *SkillSource) ([]FetchResult, error) {
	var cfg GitHubConfig
	if err := unmarshalConfig(source.Config, &cfg); err != nil {
		return nil, fmt.Errorf("github config: %w", err)
	}
	if cfg.Owner == "" || cfg.Repo == "" {
		return nil, fmt.Errorf("github config: owner and repo are required")
	}
	ref := cfg.Ref
	if ref == "" {
		ref = "main"
	}

	token := github.TokenFromEnv("HELIX_SOURCE_SYNC_GITHUB_TOKEN")
	client := github.NewClient(token, o.logger)

	// List the tree to find SKILL.md files.
	tree, err := client.ListTreeRecursive(ctx, cfg.Owner, cfg.Repo, ref)
	if err != nil {
		return nil, fmt.Errorf("github list tree %s/%s@%s: %w", cfg.Owner, cfg.Repo, ref, err)
	}
	if tree.Truncated {
		o.logger.Warn("sync: github tree listing truncated; some SKILL.md files may be missing",
			zap.String("source_id", source.ID.String()),
			zap.String("owner", cfg.Owner),
			zap.String("repo", cfg.Repo),
		)
	}

	// Filter to SKILL.md files under the configured path prefix.
	prefix := strings.TrimSuffix(cfg.Path, "/")
	var skillPaths []string
	for _, entry := range tree.Entries {
		if entry.Type != "blob" {
			continue
		}
		if !isSKILLMD(entry.Path) {
			continue
		}
		if prefix != "" && !strings.HasPrefix(entry.Path, prefix+"/") && entry.Path != prefix {
			continue
		}
		skillPaths = append(skillPaths, entry.Path)
	}

	if len(skillPaths) == 0 {
		return nil, nil
	}

	// Fetch each SKILL.md blob.
	var results []FetchResult
	for _, path := range skillPaths {
		blob, err := client.FetchBlob(ctx, cfg.Owner, cfg.Repo, path, ref, "")
		if err != nil {
			o.logger.Warn("sync: failed to fetch blob",
				zap.String("source_id", source.ID.String()),
				zap.String("path", path),
				zap.Error(err),
			)
			continue // skip this file, continue with others
		}
		if blob.NotModified {
			continue // cached copy is still current
		}
		results = append(results, FetchResult{
			Path:    path,
			Content: blob.Content,
		})
	}

	return results, nil
}

// fetchFilesystem fetches SKILL.md files from a local directory.
func (o *Orchestrator) fetchFilesystem(ctx context.Context, source *SkillSource) ([]FetchResult, error) {
	var cfg FilesystemConfig
	if err := unmarshalConfig(source.Config, &cfg); err != nil {
		return nil, fmt.Errorf("filesystem config: %w", err)
	}
	if cfg.RootPath == "" {
		return nil, fmt.Errorf("filesystem config: root_path is required")
	}

	// Walk the directory looking for SKILL.md files.
	var results []FetchResult
	err := filepath.Walk(cfg.RootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Check context cancellation between iterations.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if info.IsDir() {
			return nil
		}
		if !isSKILLMD(path) {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			o.logger.Warn("sync: failed to read file",
				zap.String("source_id", source.ID.String()),
				zap.String("path", path),
				zap.Error(err),
			)
			return nil // skip, continue walk
		}
		// Use path relative to root.
		relPath, err := filepath.Rel(cfg.RootPath, path)
		if err != nil {
			relPath = path
		}
		results = append(results, FetchResult{
			Path:    filepath.ToSlash(relPath),
			Content: content,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("filesystem walk %s: %w", cfg.RootPath, err)
	}

	return results, nil
}

// parseContent runs the SKILL.md parser on each fetched item. Items that
// fail to parse are logged and collected as non-fatal errors; successfully
// parsed items are returned.
func (o *Orchestrator) parseContent(items []FetchResult) ([]*skillmd.ParsedSkill, []string) {
	var parsed []*skillmd.ParsedSkill
	var errors []string

	for _, item := range items {
		ps, err := skillmd.Parse(item.Content, item.Path)
		if err != nil {
			msg := fmt.Sprintf("parse %s: %v", item.Path, err)
			o.logger.Warn("sync: parse failure",
				zap.String("path", item.Path),
				zap.Error(err),
			)
			errors = append(errors, msg)
			continue
		}
		parsed = append(parsed, ps)
	}

	return parsed, errors
}

// mapSkills converts parsed skills to models.Skill values via the mapper
// package. The source's Name is used as the sourceSlug for namespacing. A
// source-specific permalink is built from the source config for license-gated
// skills.
func (o *Orchestrator) mapSkills(parsed []*skillmd.ParsedSkill, source *SkillSource) ([]*mapper.Result, []string) {
	permalink := o.buildSourcePermalink(source)
	var results []*mapper.Result
	var errors []string

	for _, ps := range parsed {
		result, err := mapper.Map(ps, source.Name, o.licenseAllowlist, permalink)
		if err != nil {
			msg := fmt.Sprintf("map %s: %v", ps.SourcePath, err)
			o.logger.Warn("sync: map failure",
				zap.String("source_path", ps.SourcePath),
				zap.Error(err),
			)
			errors = append(errors, msg)
			continue
		}
		results = append(results, result)
	}

	return results, errors
}

// dedupSkills checks each mapped skill against the existing store by name.
// Skills that already exist are skipped (counted in the returned skip count).
// This is a simple name-based dedup; the orchestrator relies on the mapper's
// namespacing (sourceSlug.Name) to avoid cross-source collisions, and on
// Create's ON CONFLICT (name) DO UPDATE for idempotent re-imports.
//
// A future enhancement (tracked separately) could compare ContentHash to
// skip unchanged skills at the orchestrator level, avoiding the mapper +
// store round-trip for unchanged files. For now, every parsed skill is
// passed through to import, where Create's upsert handles the no-op case.
func (o *Orchestrator) dedupSkills(ctx context.Context, mapped []*mapper.Result) (toImport []*mapper.Result, skipped int) {
	for _, m := range mapped {
		existing, err := o.skillStore.GetByName(ctx, m.Skill.Name)
		if err != nil {
			// ErrSkillNotFound is expected — means this is a genuinely new
			// skill, include it for import.
			if existing == nil {
				toImport = append(toImport, m)
				continue
			}
			// A real DB error — log and include for import (let Create's
			// upsert handle it).
			o.logger.Warn("sync: dedup lookup error, proceeding with import",
				zap.String("skill_name", m.Skill.Name),
				zap.Error(err),
			)
			toImport = append(toImport, m)
			continue
		}
		// Skill exists — for now, always re-import (Create's upsert handles
		// it). A future ContentHash comparison could skip here.
		toImport = append(toImport, m)
	}

	return toImport, skipped
}

// importSkills creates (or upserts) each skill via the skill store. Per-item
// errors are logged and collected as non-fatal errors.
func (o *Orchestrator) importSkills(ctx context.Context, toImport []*mapper.Result) (imported int, errors []string) {
	for _, m := range toImport {
		if err := o.skillStore.Create(ctx, m.Skill); err != nil {
			msg := fmt.Sprintf("import %s: %v", m.Skill.Name, err)
			o.logger.Warn("sync: import failure",
				zap.String("skill_name", m.Skill.Name),
				zap.Error(err),
			)
			errors = append(errors, msg)
			continue
		}
		imported++
		o.logger.Debug("sync: imported skill",
			zap.String("skill_name", m.Skill.Name),
			zap.Bool("license_skipped", m.LicenseSkipped),
		)
	}

	return imported, errors
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// buildSourcePermalink constructs a best-effort "see upstream" URL for
// license-gated skills. For GitHub sources this is the repo tree URL at the
// configured ref/path; for other source types it returns empty.
func (o *Orchestrator) buildSourcePermalink(source *SkillSource) string {
	if source.SourceType != SourceTypeGitHub {
		return ""
	}
	var cfg GitHubConfig
	if err := unmarshalConfig(source.Config, &cfg); err != nil {
		return ""
	}
	if cfg.Owner == "" || cfg.Repo == "" {
		return ""
	}
	ref := cfg.Ref
	if ref == "" {
		ref = "main"
	}
	url := fmt.Sprintf("https://github.com/%s/%s/tree/%s", cfg.Owner, cfg.Repo, ref)
	if cfg.Path != "" {
		url += "/" + strings.TrimPrefix(cfg.Path, "/")
	}
	return url
}

// isSKILLMD reports whether path ends with "/SKILL.md" or is exactly
// "SKILL.md" (case-insensitive comparison of the filename component).
func isSKILLMD(path string) bool {
	base := filepath.Base(path)
	return strings.EqualFold(base, "SKILL.md")
}

// unmarshalConfig is a thin wrapper around json.Unmarshal for source config
// payloads. It returns a clear error when the config is malformed.
func unmarshalConfig(raw []byte, out interface{}) error {
	if len(raw) == 0 {
		return nil // empty config is valid for some source types
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("unmarshal config: %w", err)
	}
	return nil
}
