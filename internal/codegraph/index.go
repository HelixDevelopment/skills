// Package codegraph — index management maps CodeGraph symbols to skill
// evidence entries and deduplicates evidence by (source_project, source_file,
// pattern) so that repeated indexing of the same codebase does not create
// duplicate evidence rows (§11.4.79).
package codegraph

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// IndexManager
// ---------------------------------------------------------------------------

// IndexManager orchestrates the lifecycle of a CodeGraph code index and
// translates index results into skill evidence records that can be persisted
// in the knowledge graph database.
type IndexManager struct {
	client *MCPClient
	logger *zap.Logger

	// evidenceIndex deduplicates evidence by a content-derived key.
	mu           sync.RWMutex
	evidenceKeys map[string]uuid.UUID // key → evidence ID
}

// NewIndexManager creates a new index manager backed by the given MCP client.
func NewIndexManager(client *MCPClient, logger *zap.Logger) *IndexManager {
	return &IndexManager{
		client:       client,
		logger:       logger,
		evidenceKeys: make(map[string]uuid.UUID),
	}
}

// ---------------------------------------------------------------------------
// Index lifecycle
// ---------------------------------------------------------------------------

// IndexProject submits a project to CodeGraph for indexing and returns the
// index result. When CodeGraph is unavailable the result is empty but the
// call succeeds (graceful degradation).
func (m *IndexManager) IndexProject(ctx context.Context, projectPath string) (*IndexResult, error) {
	m.logger.Info("indexing project via codegraph", zap.String("path", projectPath))

	result, err := m.client.IndexProject(ctx, projectPath)
	if err != nil {
		return nil, fmt.Errorf("index project %q: %w", projectPath, err)
	}

	m.logger.Info("project indexed",
		zap.String("path", projectPath),
		zap.Int("files", result.FilesIndexed),
		zap.Int("symbols", result.SymbolsFound),
	)
	return result, nil
}

// QuerySymbols searches the CodeGraph index for symbols matching a pattern.
func (m *IndexManager) QuerySymbols(ctx context.Context, pattern string) ([]Symbol, error) {
	return m.client.QuerySymbols(ctx, pattern)
}

// GetDependencies returns dependency edges for a single file.
func (m *IndexManager) GetDependencies(ctx context.Context, file string) ([]Dependency, error) {
	return m.client.GetDependencies(ctx, file)
}

// ---------------------------------------------------------------------------
// Symbol → Evidence mapping
// ---------------------------------------------------------------------------

// SymbolToEvidence converts a single CodeGraph symbol into a skill evidence
// record. The evidence is NOT persisted here; the caller is responsible for
// writing it to the database.
func (m *IndexManager) SymbolToEvidence(symbol Symbol, skillID uuid.UUID, projectPath string) models.Evidence {
	return models.Evidence{
		ID:            uuid.New(),
		SkillID:       skillID,
		SourceProject: projectPath,
		SourceFile:    symbol.File,
		CodeSnippet:   symbol.Signature,
		Pattern:       symbol.Kind,
		Language:      symbol.Language,
		Validated:     false,
		CreatedAt:     time.Now().UTC(),
	}
}

// SymbolsToEvidence converts a batch of symbols to evidence records,
// deduplicating by (source_project, source_file, pattern). Returns only
// NEW evidence entries that were not already seen.
func (m *IndexManager) SymbolsToEvidence(symbols []Symbol, skillID uuid.UUID, projectPath string) []models.Evidence {
	var out []models.Evidence
	for _, sym := range symbols {
		key := evidenceKey(projectPath, sym.File, sym.Kind)

		m.mu.RLock()
		_, exists := m.evidenceKeys[key]
		m.mu.RUnlock()

		if exists {
			continue
		}

		ev := m.SymbolToEvidence(sym, skillID, projectPath)
		m.mu.Lock()
		m.evidenceKeys[key] = ev.ID
		m.mu.Unlock()

		out = append(out, ev)
	}
	return out
}

// DeduplicateEvidence filters a slice of evidence records, removing any that
// have already been registered by (source_project, source_file, pattern).
// Returns only the evidence entries that are new.
func (m *IndexManager) DeduplicateEvidence(evidence []models.Evidence) []models.Evidence {
	var out []models.Evidence
	for _, ev := range evidence {
		key := evidenceKey(ev.SourceProject, ev.SourceFile, ev.Pattern)

		m.mu.RLock()
		_, exists := m.evidenceKeys[key]
		m.mu.RUnlock()

		if exists {
			continue
		}

		m.mu.Lock()
		m.evidenceKeys[key] = ev.ID
		m.mu.Unlock()

		out = append(out, ev)
	}
	return out
}

// MarkEvidenceSeen registers an evidence entry's dedup key so that future
// SymbolsToEvidence / DeduplicateEvidence calls treat it as already present.
// Useful when loading existing evidence from the database at startup.
func (m *IndexManager) MarkEvidenceSeen(sourceProject, sourceFile, pattern string, id uuid.UUID) {
	key := evidenceKey(sourceProject, sourceFile, pattern)
	m.mu.Lock()
	m.evidenceKeys[key] = id
	m.mu.Unlock()
}

// ClearEvidenceCache resets the deduplication cache. Useful when a full
// re-index is desired.
func (m *IndexManager) ClearEvidenceCache() {
	m.mu.Lock()
	m.evidenceKeys = make(map[string]uuid.UUID)
	m.mu.Unlock()
}

// EvidenceCacheSize returns the number of tracked dedup keys.
func (m *IndexManager) EvidenceCacheSize() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.evidenceKeys)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// evidenceKey computes a deterministic dedup key from the evidence triple.
func evidenceKey(sourceProject, sourceFile, pattern string) string {
	h := sha256.Sum256([]byte(sourceProject + "|" + sourceFile + "|" + pattern))
	return fmt.Sprintf("%x", h[:16])
}
