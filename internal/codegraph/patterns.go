// Package codegraph — pattern extraction analyzes CodeGraph index data to
// discover skill-relevant patterns: API usage, architecture, dependency, and
// configuration patterns. It maps discovered patterns to existing skills or
// suggests new skills when no match is found (§11.4.79).
package codegraph

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Pattern types
// ---------------------------------------------------------------------------

// PatternCategory classifies the kind of extracted pattern.
type PatternCategory string

const (
	PatternAPIUsage      PatternCategory = "api_usage"
	PatternArchitecture  PatternCategory = "architecture"
	PatternDependency    PatternCategory = "dependency"
	PatternConfiguration PatternCategory = "configuration"
)

// ExtractedPattern is a skill-relevant pattern discovered from the CodeGraph
// index. It may map to an existing skill or represent a candidate for a new
// skill.
type ExtractedPattern struct {
	ID          uuid.UUID       `json:"id"`
	Category    PatternCategory `json:"category"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Files       []string        `json:"files"`
	Confidence  float64         `json:"confidence"` // 0.0 – 1.0
	// SkillID is set when the pattern maps to an existing skill.
	SkillID *uuid.UUID `json:"skill_id,omitempty"`
	// SuggestedSkillName is set when no existing skill matches.
	SuggestedSkillName string `json:"suggested_skill_name,omitempty"`
}

// PatternExtractorConfig holds tuning knobs for the pattern extractor.
type PatternExtractorConfig struct {
	// MinConfidence is the minimum confidence threshold for a pattern to be
	// returned. Patterns below this are silently discarded.
	MinConfidence float64
	// MaxPatterns caps the number of patterns returned per extraction run.
	MaxPatterns int
}

// DefaultPatternExtractorConfig returns sensible defaults.
func DefaultPatternExtractorConfig() PatternExtractorConfig {
	return PatternExtractorConfig{
		MinConfidence: 0.4,
		MaxPatterns:   200,
	}
}

// ---------------------------------------------------------------------------
// SkillSearcher abstracts skill lookup for pattern-to-skill mapping.
// ---------------------------------------------------------------------------

// SkillSearcher finds skills by keyword. Satisfied by *skill.Store.
type SkillSearcher interface {
	// SearchByKeyword returns skill IDs whose name or title matches the keyword.
	SearchByKeyword(ctx context.Context, keyword string, limit int) ([]uuid.UUID, error)
}

// ---------------------------------------------------------------------------
// PatternExtractor
// ---------------------------------------------------------------------------

// PatternExtractor analyzes CodeGraph index data and extracts skill-relevant
// patterns.
type PatternExtractor struct {
	client   *MCPClient
	index    *IndexManager
	searcher SkillSearcher
	cfg      PatternExtractorConfig
	logger   *zap.Logger
}

// NewPatternExtractor creates a new pattern extractor.
func NewPatternExtractor(
	client *MCPClient,
	index *IndexManager,
	searcher SkillSearcher,
	cfg PatternExtractorConfig,
	logger *zap.Logger,
) *PatternExtractor {
	if cfg.MinConfidence <= 0 {
		cfg.MinConfidence = 0.4
	}
	if cfg.MaxPatterns <= 0 {
		cfg.MaxPatterns = 200
	}
	return &PatternExtractor{
		client:   client,
		index:    index,
		searcher: searcher,
		cfg:      cfg,
		logger:   logger,
	}
}

// ExtractPatterns runs all pattern extractors against the CodeGraph index
// for the given project and returns the merged, deduplicated, filtered
// results.
func (pe *PatternExtractor) ExtractPatterns(ctx context.Context, projectPath string) ([]ExtractedPattern, error) {
	pe.logger.Info("extracting patterns from codegraph index", zap.String("project", projectPath))

	var all []ExtractedPattern

	// 1. API usage patterns
	apiPatterns, err := pe.extractAPIUsagePatterns(ctx, projectPath)
	if err != nil {
		pe.logger.Debug("api usage pattern extraction failed", zap.Error(err))
	} else {
		all = append(all, apiPatterns...)
	}

	// 2. Architecture patterns
	archPatterns, err := pe.extractArchitecturePatterns(ctx, projectPath)
	if err != nil {
		pe.logger.Debug("architecture pattern extraction failed", zap.Error(err))
	} else {
		all = append(all, archPatterns...)
	}

	// 3. Dependency patterns
	depPatterns, err := pe.extractDependencyPatterns(ctx, projectPath)
	if err != nil {
		pe.logger.Debug("dependency pattern extraction failed", zap.Error(err))
	} else {
		all = append(all, depPatterns...)
	}

	// 4. Configuration patterns
	cfgPatterns, err := pe.extractConfigurationPatterns(ctx, projectPath)
	if err != nil {
		pe.logger.Debug("configuration pattern extraction failed", zap.Error(err))
	} else {
		all = append(all, cfgPatterns...)
	}

	// Filter by confidence and cap.
	filtered := pe.filterAndCap(all)

	// Map to existing skills.
	if pe.searcher != nil {
		for i := range filtered {
			pe.mapToSkill(ctx, &filtered[i])
		}
	}

	pe.logger.Info("pattern extraction complete",
		zap.String("project", projectPath),
		zap.Int("total", len(all)),
		zap.Int("filtered", len(filtered)),
	)

	return filtered, nil
}

// ---------------------------------------------------------------------------
// API usage patterns
// ---------------------------------------------------------------------------

// extractAPIUsagePatterns discovers API usage patterns by querying for
// symbols that look like public API surfaces (exported functions, interface
// implementations, HTTP handlers).
func (pe *PatternExtractor) extractAPIUsagePatterns(ctx context.Context, projectPath string) ([]ExtractedPattern, error) {
	// Query for exported symbols that indicate API surfaces.
	symbols, err := pe.index.QuerySymbols(ctx, "kind:function exported:true")
	if err != nil {
		return nil, fmt.Errorf("query api symbols: %w", err)
	}

	// Group by file to find files with multiple API-related symbols.
	fileSymbols := make(map[string][]Symbol)
	for _, sym := range symbols {
		if isAPIRelated(sym) {
			fileSymbols[sym.File] = append(fileSymbols[sym.File], sym)
		}
	}

	var patterns []ExtractedPattern
	for file, syms := range fileSymbols {
		if len(syms) < 2 {
			continue // need at least 2 API symbols to form a pattern
		}

		names := make([]string, 0, len(syms))
		for _, s := range syms {
			names = append(names, s.Name)
		}

		patterns = append(patterns, ExtractedPattern{
			ID:          uuid.New(),
			Category:    PatternAPIUsage,
			Name:        fmt.Sprintf("api-surface:%s", filepath.Base(file)),
			Description: fmt.Sprintf("API surface with %d exported symbols: %s", len(syms), strings.Join(names, ", ")),
			Files:       []string{file},
			Confidence:  clampConfidence(float64(len(syms)) / 10.0),
		})
	}

	return patterns, nil
}

// isAPIRelated reports whether a symbol looks like part of a public API.
func isAPIRelated(sym Symbol) bool {
	switch sym.Kind {
	case "function", "method", "interface":
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Architecture patterns
// ---------------------------------------------------------------------------

// extractArchitecturePatterns discovers architectural patterns by analyzing
// symbol kinds and their relationships. It looks for well-known patterns
// like MVC, repository, dependency injection, etc.
func (pe *PatternExtractor) extractArchitecturePatterns(ctx context.Context, projectPath string) ([]ExtractedPattern, error) {
	symbols, err := pe.index.QuerySymbols(ctx, "*")
	if err != nil {
		return nil, fmt.Errorf("query all symbols: %w", err)
	}

	var patterns []ExtractedPattern

	// Detect repository pattern: interfaces named *Repository or *Repo.
	repoFiles := findSymbolFiles(symbols, func(s Symbol) bool {
		lower := strings.ToLower(s.Name)
		return s.Kind == "interface" && (strings.HasSuffix(lower, "repository") || strings.HasSuffix(lower, "repo"))
	})
	if len(repoFiles) > 0 {
		patterns = append(patterns, ExtractedPattern{
			ID:          uuid.New(),
			Category:    PatternArchitecture,
			Name:        "repository-pattern",
			Description: fmt.Sprintf("Repository pattern detected across %d files", len(repoFiles)),
			Files:       repoFiles,
			Confidence:  clampConfidence(0.7 + float64(len(repoFiles))*0.05),
		})
	}

	// Detect MVC/MVVM: presence of Controller + ViewModel/View + Model types.
	mvcFiles := findSymbolFiles(symbols, func(s Symbol) bool {
		lower := strings.ToLower(s.Name)
		return strings.Contains(lower, "controller") || strings.Contains(lower, "viewmodel") || strings.Contains(lower, "presenter")
	})
	if len(mvcFiles) >= 2 {
		patterns = append(patterns, ExtractedPattern{
			ID:          uuid.New(),
			Category:    PatternArchitecture,
			Name:        "mvc-mvvm-pattern",
			Description: fmt.Sprintf("MVC/MVVM architecture detected across %d files", len(mvcFiles)),
			Files:       mvcFiles,
			Confidence:  clampConfidence(0.6 + float64(len(mvcFiles))*0.05),
		})
	}

	// Detect dependency injection: Provide/Inject/Module/Bind keywords.
	diFiles := findSymbolFiles(symbols, func(s Symbol) bool {
		lower := strings.ToLower(s.Name)
		return strings.Contains(lower, "provide") || strings.Contains(lower, "inject") || strings.Contains(lower, "module") || strings.Contains(lower, "bind")
	})
	if len(diFiles) >= 2 {
		patterns = append(patterns, ExtractedPattern{
			ID:          uuid.New(),
			Category:    PatternArchitecture,
			Name:        "dependency-injection",
			Description: fmt.Sprintf("Dependency injection pattern detected across %d files", len(diFiles)),
			Files:       diFiles,
			Confidence:  clampConfidence(0.55 + float64(len(diFiles))*0.05),
		})
	}

	return patterns, nil
}

// findSymbolFiles returns the deduplicated set of files containing symbols
// for which the predicate returns true.
func findSymbolFiles(symbols []Symbol, pred func(Symbol) bool) []string {
	seen := make(map[string]bool)
	var files []string
	for _, s := range symbols {
		if pred(s) && !seen[s.File] {
			seen[s.File] = true
			files = append(files, s.File)
		}
	}
	return files
}

// ---------------------------------------------------------------------------
// Dependency patterns
// ---------------------------------------------------------------------------

// extractDependencyPatterns discovers dependency-related patterns by
// analyzing import/call graphs from CodeGraph. It groups dependencies by
// target package to find heavy dependency clusters.
func (pe *PatternExtractor) extractDependencyPatterns(ctx context.Context, projectPath string) ([]ExtractedPattern, error) {
	// Get all symbols to find source files.
	symbols, err := pe.index.QuerySymbols(ctx, "*")
	if err != nil {
		return nil, fmt.Errorf("query symbols for deps: %w", err)
	}

	// Collect unique source files.
	seen := make(map[string]bool)
	var files []string
	for _, s := range symbols {
		if !seen[s.File] {
			seen[s.File] = true
			files = append(files, s.File)
		}
	}

	// Analyze dependencies per file.
	depCounts := make(map[string]int)     // target package → count
	depFiles := make(map[string][]string) // target package → source files

	for _, file := range files {
		deps, err := pe.index.GetDependencies(ctx, file)
		if err != nil {
			continue
		}
		for _, d := range deps {
			pkg := normalizePackageName(d.Target)
			if pkg == "" {
				continue
			}
			depCounts[pkg]++
			if !contains(depFiles[pkg], file) {
				depFiles[pkg] = append(depFiles[pkg], file)
			}
		}
	}

	var patterns []ExtractedPattern
	for pkg, count := range depCounts {
		if count < 3 {
			continue // require at least 3 usages to form a pattern
		}

		confidence := clampConfidence(float64(count) / 20.0)
		patterns = append(patterns, ExtractedPattern{
			ID:          uuid.New(),
			Category:    PatternDependency,
			Name:        fmt.Sprintf("dep-cluster:%s", pkg),
			Description: fmt.Sprintf("Heavy dependency on %s (%d usages across %d files)", pkg, count, len(depFiles[pkg])),
			Files:       depFiles[pkg],
			Confidence:  confidence,
		})
	}

	return patterns, nil
}

// normalizePackageName extracts the top-level package name from a dependency
// target string (e.g. "github.com/foo/bar.Baz" → "bar").
func normalizePackageName(target string) string {
	// Strip function/type suffix.
	if idx := strings.LastIndex(target, "."); idx > 0 {
		target = target[:idx]
	}
	// Take last path segment.
	parts := strings.Split(target, "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

// contains reports whether slice contains s.
func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Configuration patterns
// ---------------------------------------------------------------------------

// extractConfigurationPatterns discovers configuration-related patterns by
// looking for config file symbols (TOML, YAML, JSON, env files) and
// symbols that reference configuration keys.
func (pe *PatternExtractor) extractConfigurationPatterns(ctx context.Context, projectPath string) ([]ExtractedPattern, error) {
	// Query for symbols in config-related files.
	configSymbols, err := pe.index.QuerySymbols(ctx, "file:*config*")
	if err != nil {
		return nil, fmt.Errorf("query config symbols: %w", err)
	}

	if len(configSymbols) == 0 {
		return nil, nil
	}

	// Group by language.
	langGroups := make(map[string][]Symbol)
	for _, s := range configSymbols {
		lang := s.Language
		if lang == "" {
			lang = "unknown"
		}
		langGroups[lang] = append(langGroups[lang], s)
	}

	var patterns []ExtractedPattern
	for lang, syms := range langGroups {
		files := make([]string, 0, len(syms))
		for _, s := range syms {
			if !contains(files, s.File) {
				files = append(files, s.File)
			}
		}

		patterns = append(patterns, ExtractedPattern{
			ID:          uuid.New(),
			Category:    PatternConfiguration,
			Name:        fmt.Sprintf("config:%s", lang),
			Description: fmt.Sprintf("Configuration pattern in %s with %d symbols across %d files", lang, len(syms), len(files)),
			Files:       files,
			Confidence:  clampConfidence(0.5 + float64(len(syms))*0.02),
		})
	}

	return patterns, nil
}

// ---------------------------------------------------------------------------
// Skill mapping
// ---------------------------------------------------------------------------

// mapToSkill attempts to match a pattern to an existing skill by searching
// for the pattern name as a keyword. Sets SkillID on success or
// SuggestedSkillName on failure.
func (pe *PatternExtractor) mapToSkill(ctx context.Context, pattern *ExtractedPattern) {
	if pe.searcher == nil {
		pattern.SuggestedSkillName = pattern.Name
		return
	}

	// Extract the core keyword from the pattern name (strip category prefix).
	keyword := pattern.Name
	if idx := strings.Index(keyword, ":"); idx >= 0 {
		keyword = keyword[idx+1:]
	}

	skillIDs, err := pe.searcher.SearchByKeyword(ctx, keyword, 3)
	if err != nil || len(skillIDs) == 0 {
		pattern.SuggestedSkillName = pattern.Name
		return
	}

	// Use the first (best) match.
	pattern.SkillID = &skillIDs[0]
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// filterAndCap removes patterns below the confidence threshold and caps the
// total count at MaxPatterns. Patterns are sorted by confidence descending
// before capping.
func (pe *PatternExtractor) filterAndCap(patterns []ExtractedPattern) []ExtractedPattern {
	var filtered []ExtractedPattern
	for _, p := range patterns {
		if p.Confidence >= pe.cfg.MinConfidence {
			filtered = append(filtered, p)
		}
	}

	// Simple selection sort by confidence descending (good enough for ≤200 items).
	for i := 0; i < len(filtered) && i < pe.cfg.MaxPatterns; i++ {
		maxIdx := i
		for j := i + 1; j < len(filtered); j++ {
			if filtered[j].Confidence > filtered[maxIdx].Confidence {
				maxIdx = j
			}
		}
		if maxIdx != i {
			filtered[i], filtered[maxIdx] = filtered[maxIdx], filtered[i]
		}
	}

	if len(filtered) > pe.cfg.MaxPatterns {
		filtered = filtered[:pe.cfg.MaxPatterns]
	}

	return filtered
}

// clampConfidence clamps a confidence value to [0.0, 1.0].
func clampConfidence(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1.0 {
		return 1.0
	}
	return v
}
