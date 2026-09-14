// Package codegraph — sync automation watches for code changes (via
// CodeGraph filesystem watcher or periodic polling), triggers re-indexing,
// updates skill evidence in the database, and marks affected skills as stale
// in the registry (§11.4.80).
package codegraph

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Sync interfaces (dependency-injection seams for testing)
// ---------------------------------------------------------------------------

// EvidenceStore abstracts the persistence layer for skill evidence.
type EvidenceStore interface {
	// UpsertEvidence inserts or updates an evidence record.
	UpsertEvidence(ctx context.Context, ev EvidenceRecord) error
	// ListEvidenceByProject returns all evidence for a given project path.
	ListEvidenceByProject(ctx context.Context, projectPath string) ([]EvidenceRecord, error)
	// DeleteEvidenceByFile removes all evidence sourced from a specific file.
	DeleteEvidenceByFile(ctx context.Context, sourceFile string) error
}

// SkillRegistry abstracts the skill registry for marking skills stale.
type SkillRegistry interface {
	// MarkStale marks a skill as needing re-validation.
	MarkStale(ctx context.Context, skillID uuid.UUID) error
	// SkillsByProject returns skill IDs that have evidence from the given project.
	SkillsByProject(ctx context.Context, projectPath string) ([]uuid.UUID, error)
}

// EvidenceRecord is a persistence-layer evidence record used by the sync
// manager. It mirrors models.Evidence but avoids a direct import cycle.
type EvidenceRecord struct {
	ID            uuid.UUID
	SkillID       uuid.UUID
	SourceProject string
	SourceFile    string
	CodeSnippet   string
	Pattern       string
	Language      string
	Validated     bool
}

// ---------------------------------------------------------------------------
// SyncManager
// ---------------------------------------------------------------------------

// SyncManager coordinates change detection, re-indexing, and evidence
// updates. It supports two change-detection strategies: CodeGraph filesystem
// watcher (preferred, when available) and periodic polling (fallback).
type SyncManager struct {
	client   *MCPClient
	index    *IndexManager
	evidence EvidenceStore
	registry SkillRegistry
	cfg      config.CodeGraphConfig
	logger   *zap.Logger

	// watchedPaths tracks registered watch roots so the manager can
	// re-establish watchers after a reconnect.
	mu           sync.Mutex
	watchedPaths map[string]bool
	stopCh       chan struct{}
}

// NewSyncManager creates a new sync manager.
func NewSyncManager(
	client *MCPClient,
	index *IndexManager,
	evidence EvidenceStore,
	registry SkillRegistry,
	cfg config.CodeGraphConfig,
	logger *zap.Logger,
) *SyncManager {
	return &SyncManager{
		client:       client,
		index:        index,
		evidence:     evidence,
		registry:     registry,
		cfg:          cfg,
		logger:       logger,
		watchedPaths: make(map[string]bool),
		stopCh:       make(chan struct{}),
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Start begins the sync loop. If watch_enabled is true AND CodeGraph is
// available it uses filesystem watching; otherwise it falls back to periodic
// polling at the configured interval. Blocks until ctx is cancelled.
func (sm *SyncManager) Start(ctx context.Context) {
	sm.logger.Info("codegraph sync manager starting",
		zap.Bool("watch_enabled", sm.cfg.WatchEnabled),
		zap.Int("sync_interval_seconds", sm.cfg.SyncIntervalSeconds),
	)

	if sm.cfg.WatchEnabled && sm.client.IsAvailable() {
		sm.runWatchMode(ctx)
	} else {
		sm.runPollMode(ctx)
	}
}

// Stop signals the sync manager to shut down.
func (sm *SyncManager) Stop() {
	select {
	case <-sm.stopCh:
		// already stopped
	default:
		close(sm.stopCh)
	}
}

// ---------------------------------------------------------------------------
// Watch mode
// ---------------------------------------------------------------------------

// runWatchMode registers watchers on all known project paths and processes
// change events as they arrive.
func (sm *SyncManager) runWatchMode(ctx context.Context) {
	sm.mu.Lock()
	paths := make([]string, 0, len(sm.watchedPaths))
	for p := range sm.watchedPaths {
		paths = append(paths, p)
	}
	sm.mu.Unlock()

	// If no paths registered yet, start polling until paths appear.
	if len(paths) == 0 {
		sm.logger.Info("no project paths registered for watch, falling back to poll until paths are added")
		sm.runPollMode(ctx)
		return
	}

	var wg sync.WaitGroup
	for _, p := range paths {
		wg.Add(1)
		go func(projectPath string) {
			defer wg.Done()
			sm.watchProject(ctx, projectPath)
		}(p)
	}
	wg.Wait()
}

// watchProject subscribes to change events for a single project and
// triggers re-indexing on each change.
func (sm *SyncManager) watchProject(ctx context.Context, projectPath string) {
	ch, err := sm.client.WatchChanges(ctx, projectPath)
	if err != nil {
		sm.logger.Warn("failed to watch project, falling back to poll",
			zap.String("path", projectPath),
			zap.Error(err),
		)
		sm.pollProject(ctx, projectPath)
		return
	}

	for ev := range ch {
		sm.logger.Debug("code change detected",
			zap.String("path", ev.Path),
			zap.String("kind", ev.Kind),
		)

		if err := sm.handleChange(ctx, projectPath, ev); err != nil {
			sm.logger.Warn("failed to handle code change",
				zap.String("file", ev.Path),
				zap.Error(err),
			)
		}
	}

	// Channel closed (CodeGraph unavailable or ctx cancelled). Fall back to poll.
	sm.logger.Info("watch channel closed, switching to poll",
		zap.String("path", projectPath),
	)
	sm.pollProject(ctx, projectPath)
}

// ---------------------------------------------------------------------------
// Poll mode
// ---------------------------------------------------------------------------

// runPollMode periodically re-indexes all registered project paths.
func (sm *SyncManager) runPollMode(ctx context.Context) {
	interval := time.Duration(sm.cfg.SyncIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 5 * time.Minute
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Poll immediately on start.
	sm.pollAll(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-sm.stopCh:
			return
		case <-ticker.C:
			sm.pollAll(ctx)
		}
	}
}

// pollAll re-indexes every registered project path.
func (sm *SyncManager) pollAll(ctx context.Context) {
	sm.mu.Lock()
	paths := make([]string, 0, len(sm.watchedPaths))
	for p := range sm.watchedPaths {
		paths = append(paths, p)
	}
	sm.mu.Unlock()

	for _, p := range paths {
		sm.pollProject(ctx, p)
	}
}

// pollProject re-indexes a single project and updates evidence.
func (sm *SyncManager) pollProject(ctx context.Context, projectPath string) {
	sm.logger.Debug("polling project for changes", zap.String("path", projectPath))

	result, err := sm.index.IndexProject(ctx, projectPath)
	if err != nil {
		sm.logger.Warn("poll re-index failed",
			zap.String("path", projectPath),
			zap.Error(err),
		)
		return
	}

	if result.FilesIndexed == 0 {
		return // nothing new
	}

	// Mark all skills with evidence from this project as stale.
	if err := sm.markAffectedSkillsStale(ctx, projectPath); err != nil {
		sm.logger.Warn("failed to mark skills stale",
			zap.String("path", projectPath),
			zap.Error(err),
		)
	}

	sm.logger.Info("poll sync complete",
		zap.String("path", projectPath),
		zap.Int("files", result.FilesIndexed),
	)
}

// ---------------------------------------------------------------------------
// Change handling
// ---------------------------------------------------------------------------

// handleChange processes a single code change event.
func (sm *SyncManager) handleChange(ctx context.Context, projectPath string, ev ChangeEvent) error {
	switch ev.Kind {
	case "delete":
		// Remove evidence sourced from the deleted file.
		if err := sm.evidence.DeleteEvidenceByFile(ctx, ev.Path); err != nil {
			return fmt.Errorf("delete evidence for %q: %w", ev.Path, err)
		}
		sm.logger.Info("removed evidence for deleted file", zap.String("file", ev.Path))

	case "create", "modify":
		// Re-index the project (CodeGraph is file-granular but re-index is
		// project-scoped for consistency).
		if _, err := sm.index.IndexProject(ctx, projectPath); err != nil {
			return fmt.Errorf("re-index after %s of %q: %w", ev.Kind, ev.Path, err)
		}

	default:
		sm.logger.Debug("unknown change kind, ignoring",
			zap.String("kind", ev.Kind),
			zap.String("file", ev.Path),
		)
	}

	// Mark affected skills stale regardless of change kind.
	return sm.markAffectedSkillsStale(ctx, projectPath)
}

// markAffectedSkillsStale finds all skills that have evidence from the given
// project and marks them as stale in the registry.
func (sm *SyncManager) markAffectedSkillsStale(ctx context.Context, projectPath string) error {
	skillIDs, err := sm.registry.SkillsByProject(ctx, projectPath)
	if err != nil {
		return fmt.Errorf("lookup skills for project %q: %w", projectPath, err)
	}

	for _, id := range skillIDs {
		if err := sm.registry.MarkStale(ctx, id); err != nil {
			sm.logger.Warn("failed to mark skill stale",
				zap.String("skill_id", id.String()),
				zap.Error(err),
			)
			continue
		}
		sm.logger.Debug("skill marked stale",
			zap.String("skill_id", id.String()),
			zap.String("project", projectPath),
		)
	}

	if len(skillIDs) > 0 {
		sm.logger.Info("marked skills stale",
			zap.String("project", projectPath),
			zap.Int("count", len(skillIDs)),
		)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Project registration
// ---------------------------------------------------------------------------

// RegisterProject adds a project path to the set of watched/polled paths.
func (sm *SyncManager) RegisterProject(projectPath string) {
	absPath, err := filepath.Abs(projectPath)
	if err != nil {
		absPath = projectPath
	}

	sm.mu.Lock()
	sm.watchedPaths[absPath] = true
	sm.mu.Unlock()

	sm.logger.Info("project registered for sync", zap.String("path", absPath))
}

// UnregisterProject removes a project path from the watched set.
func (sm *SyncManager) UnregisterProject(projectPath string) {
	absPath, err := filepath.Abs(projectPath)
	if err != nil {
		absPath = projectPath
	}

	sm.mu.Lock()
	delete(sm.watchedPaths, absPath)
	sm.mu.Unlock()

	sm.logger.Info("project unregistered from sync", zap.String("path", absPath))
}

// RegisteredProjects returns the set of currently watched project paths.
func (sm *SyncManager) RegisteredProjects() []string {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	paths := make([]string, 0, len(sm.watchedPaths))
	for p := range sm.watchedPaths {
		paths = append(paths, p)
	}
	return paths
}
