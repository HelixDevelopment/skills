// Package codeanalysis provides high-level code analysis capabilities for the
// HelixKnowledge system. It scans project directories, extracts patterns and
// imports using tree-sitter (or regex fallback), and maps discovered patterns
// to existing skills in the knowledge graph.
package codeanalysis

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/HelixDevelopment/skills/internal/skill"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// Import represents a single import/include statement found in source code.
type Import struct {
	Path     string `json:"path"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Language string `json:"language"`
}

// Pattern represents an architectural pattern detected in source code.
type Pattern struct {
	Type       string  `json:"type"` // e.g., "mvvm", "repository", "dependency-injection"
	File       string  `json:"file"`
	Line       int     `json:"line"`
	Snippet    string  `json:"snippet"`
	Confidence float64 `json:"confidence"` // 0.0 to 1.0
}

// SkillMapping links a detected pattern to an existing skill in the graph.
type SkillMapping struct {
	Pattern   Pattern   `json:"pattern"`
	SkillID   uuid.UUID `json:"skill_id"`
	SkillName string    `json:"skill_name"`
	Score     float64   `json:"score"` // similarity score
}

// AnalysisResult captures the complete analysis of a project.
type AnalysisResult struct {
	ProjectPath string         `json:"project_path"`
	Languages   map[string]int `json:"languages"` // language -> file count
	Imports     []Import       `json:"imports"`
	Patterns    []Pattern      `json:"patterns"`
	Mappings    []SkillMapping `json:"mappings"`
	NewPatterns []Pattern      `json:"new_patterns"` // patterns not matching any existing skill
}

// fileInfo holds information about a discovered source file.
type fileInfo struct {
	path     string
	language string
	content  []byte
}

// ---------------------------------------------------------------------------
// Analyzer
// ---------------------------------------------------------------------------

// Analyzer parses codebases and extracts architectural patterns and imports.
type Analyzer struct {
	cfg    config.CodeAnalysisConfig
	logger *zap.Logger
	parser *TreeSitterParser
}

// NewAnalyzer creates a new code analyzer.
func NewAnalyzer(cfg config.CodeAnalysisConfig, logger *zap.Logger) *Analyzer {
	parser, err := NewTreeSitterParser()
	if err != nil {
		logger.Warn("failed to initialize tree-sitter parser, using regex fallback", zap.Error(err))
	}

	return &Analyzer{
		cfg:    cfg,
		logger: logger,
		parser: parser,
	}
}

// ---------------------------------------------------------------------------
// Project analysis
// ---------------------------------------------------------------------------

// AnalyzeProject scans a project directory and extracts patterns, imports,
// and language statistics. It maps discovered patterns to existing skills
// and identifies new patterns that don't match any known skill.
//
// projectPath is validated against a.cfg.AllowedRoot (§G31 path-traversal /
// LFI guard) BEFORE any filesystem walk starts -- fail-closed, so an
// unconfigured allowed root or an escaping path is rejected here rather than
// walked. This is defense-in-depth alongside the caller-side guard in
// internal/mcp's learn_from_project handler: whatever future caller reaches
// this method, the walk itself can never be pointed outside the allowlisted
// root.
func (a *Analyzer) AnalyzeProject(ctx context.Context, projectPath string) (*AnalysisResult, error) {
	canonPath, err := ValidateProjectPath(projectPath, a.cfg.AllowedRoot)
	if err != nil {
		a.logger.Warn("rejected project_path", zap.String("path", projectPath), zap.Error(err))
		return nil, fmt.Errorf("analyze project: %w", err)
	}
	projectPath = canonPath

	a.logger.Info("analyzing project", zap.String("path", projectPath))

	result := &AnalysisResult{
		ProjectPath: projectPath,
		Languages:   make(map[string]int),
	}

	// Step 1: Discover source files
	files, err := a.discoverFiles(ctx, projectPath)
	if err != nil {
		return nil, fmt.Errorf("discover files: %w", err)
	}

	a.logger.Info("discovered source files", zap.Int("count", len(files)))

	// Step 2: Analyze files concurrently
	var allImports []Import
	var allPatterns []Pattern
	var mu sync.Mutex

	const maxConcurrency = 8
	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup

	for _, f := range files {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil, ctx.Err()
		default:
		}

		wg.Add(1)
		sem <- struct{}{}

		go func(file fileInfo) {
			defer wg.Done()
			defer func() { <-sem }()

			// Parse the file
			tree, err := a.parser.Parse(file.content, file.language)
			if err != nil {
				a.logger.Debug("failed to parse file",
					zap.String("file", file.path),
					zap.Error(err),
				)
				return
			}

			// Extract imports
			imports, err := a.parser.ExtractImports(tree, file.language)
			if err == nil {
				for i := range imports {
					imports[i].File = file.path
				}
				mu.Lock()
				allImports = append(allImports, imports...)
				mu.Unlock()
			}

			// Detect patterns
			patterns := a.detectPatternsInFile(file.path, file.content, file.language)

			mu.Lock()
			result.Languages[file.language]++
			allPatterns = append(allPatterns, patterns...)
			mu.Unlock()
		}(f)
	}

	wg.Wait()

	result.Imports = allImports
	result.Patterns = allPatterns

	// Step 3: Map patterns to skills (requires store - done separately if needed)
	// Mappings are populated by MapToSkills

	a.logger.Info("project analysis complete",
		zap.String("path", projectPath),
		zap.Int("languages", len(result.Languages)),
		zap.Int("imports", len(result.Imports)),
		zap.Int("patterns", len(result.Patterns)),
	)

	return result, nil
}

// ---------------------------------------------------------------------------
// File discovery
// ---------------------------------------------------------------------------

// discoverFiles walks the project directory and finds source files matching
// the configured languages while respecting exclusion patterns.
func (a *Analyzer) discoverFiles(ctx context.Context, projectPath string) ([]fileInfo, error) {
	var files []fileInfo

	err := filepath.Walk(projectPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip files we can't read
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Skip directories
		if info.IsDir() {
			// Check exclusion patterns
			for _, pattern := range a.cfg.ExcludePatterns {
				matched, _ := filepath.Match(pattern, info.Name())
				if matched || strings.Contains(path, pattern) {
					return filepath.SkipDir
				}
			}
			return nil
		}

		// Check file size limit
		if info.Size() > int64(a.cfg.MaxFileSizeKB)*1024 {
			return nil // skip large files
		}

		// Detect language from extension
		language := detectLanguage(path)
		if language == "" {
			return nil // not a recognized source file
		}

		// Check if language is in our configured list
		if !a.isLanguageEnabled(language) {
			return nil
		}

		// Read file content
		content, err := os.ReadFile(path)
		if err != nil {
			return nil // skip unreadable files
		}

		files = append(files, fileInfo{
			path:     path,
			language: language,
			content:  content,
		})

		return nil
	})

	return files, err
}

// isLanguageEnabled checks if a language is in the configured analysis list.
func (a *Analyzer) isLanguageEnabled(language string) bool {
	if len(a.cfg.Languages) == 0 {
		return true // all languages enabled if none specified
	}
	for _, l := range a.cfg.Languages {
		if normalizeLanguage(l) == normalizeLanguage(language) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Import extraction
// ---------------------------------------------------------------------------

// ExtractImports finds all imports in a source file. This is a convenience
// method that parses the file and extracts imports in one step.
func (a *Analyzer) ExtractImports(ctx context.Context, filePath string, content []byte) ([]Import, error) {
	language := detectLanguage(filePath)
	if language == "" {
		return nil, fmt.Errorf("cannot detect language for %s", filePath)
	}

	tree, err := a.parser.Parse(content, language)
	if err != nil {
		return nil, fmt.Errorf("parse file: %w", err)
	}

	imports, err := a.parser.ExtractImports(tree, language)
	if err != nil {
		return nil, fmt.Errorf("extract imports: %w", err)
	}

	// Set file path on all imports
	for i := range imports {
		imports[i].File = filePath
	}

	return imports, nil
}

// ---------------------------------------------------------------------------
// Pattern detection
// ---------------------------------------------------------------------------

// DetectPatterns identifies architectural patterns in a source file.
// It combines tree-sitter AST analysis with regex-based heuristic detection.
func (a *Analyzer) DetectPatterns(ctx context.Context, filePath string, content []byte, language string) ([]Pattern, error) {
	return a.detectPatternsInFile(filePath, content, language), nil
}

// detectPatternsInFile performs pattern detection on a single file.
func (a *Analyzer) detectPatternsInFile(filePath string, content []byte, language string) []Pattern {
	var patterns []Pattern
	contentStr := string(content)

	// Pattern: MVC/MVVM architecture
	if patterns = append(patterns, detectMVCPatterns(contentStr, filePath, language)...); len(patterns) > 0 {
		// MVC patterns found
	}

	// Pattern: Repository pattern
	if p := detectRepositoryPattern(contentStr, filePath, language); p != nil {
		patterns = append(patterns, *p)
	}

	// Pattern: Dependency Injection
	if p := detectDIPattern(contentStr, filePath, language); p != nil {
		patterns = append(patterns, *p)
	}

	// Pattern: Factory pattern
	if p := detectFactoryPattern(contentStr, filePath, language); p != nil {
		patterns = append(patterns, *p)
	}

	// Pattern: Singleton
	if p := detectSingletonPattern(contentStr, filePath, language); p != nil {
		patterns = append(patterns, *p)
	}

	// Pattern: Observer/Pub-Sub
	if p := detectObserverPattern(contentStr, filePath, language); p != nil {
		patterns = append(patterns, *p)
	}

	// Pattern: Middleware/Chain of Responsibility
	if p := detectMiddlewarePattern(contentStr, filePath, language); p != nil {
		patterns = append(patterns, *p)
	}

	// Pattern: REST API / HTTP handlers
	if p := detectRESTPattern(contentStr, filePath, language); p != nil {
		patterns = append(patterns, *p)
	}

	// Pattern: Error handling strategy
	if p := detectErrorHandlingPattern(contentStr, filePath, language); p != nil {
		patterns = append(patterns, *p)
	}

	// Pattern: Testing patterns
	if p := detectTestingPattern(contentStr, filePath, language); p != nil {
		patterns = append(patterns, *p)
	}

	// Pattern: Concurrency / Goroutines
	if language == "go" {
		if p := detectConcurrencyPattern(contentStr, filePath); p != nil {
			patterns = append(patterns, *p)
		}
	}

	return patterns
}

// ---------------------------------------------------------------------------
// Individual pattern detectors
// ---------------------------------------------------------------------------

func detectMVCPatterns(content, filePath, language string) []Pattern {
	var patterns []Pattern

	mvcIndicators := []struct {
		keyword string
		ctype   string
	}{
		{"Controller", "mvc"},
		{"ViewModel", "mvvm"},
		{"Presenter", "mvp"},
		{"ModelView", "mvvm"},
	}

	for _, indicator := range mvcIndicators {
		if strings.Contains(content, indicator.keyword) {
			// Find line number
			line := 1
			if idx := strings.Index(content, indicator.keyword); idx >= 0 {
				line = strings.Count(content[:idx], "\n") + 1
			}

			// Get snippet
			snippet := extractSnippet(content, indicator.keyword, 80)

			patterns = append(patterns, Pattern{
				Type:       indicator.ctype,
				File:       filePath,
				Line:       line,
				Snippet:    snippet,
				Confidence: 0.7,
			})
		}
	}

	return patterns
}

func detectRepositoryPattern(content, filePath, language string) *Pattern {
	if !strings.Contains(content, "Repository") && !strings.Contains(content, "repository") {
		return nil
	}

	repoTerms := []string{"interface", "struct", "class", "type"}
	for _, term := range repoTerms {
		if strings.Contains(content, term) {
			snippet := extractSnippet(content, "Repository", 80)
			line := 1
			if idx := strings.Index(content, "Repository"); idx >= 0 {
				line = strings.Count(content[:idx], "\n") + 1
			}

			return &Pattern{
				Type:       "repository",
				File:       filePath,
				Line:       line,
				Snippet:    snippet,
				Confidence: 0.65,
			}
		}
	}

	return nil
}

func detectDIPattern(content, filePath, language string) *Pattern {
	diIndicators := []string{
		"inject", "Inject", "wire", "Wire",
		"Provide", "provide", "Module",
	}

	for _, indicator := range diIndicators {
		if strings.Contains(content, indicator) {
			snippet := extractSnippet(content, indicator, 80)
			line := 1
			if idx := strings.Index(content, indicator); idx >= 0 {
				line = strings.Count(content[:idx], "\n") + 1
			}

			return &Pattern{
				Type:       "dependency-injection",
				File:       filePath,
				Line:       line,
				Snippet:    snippet,
				Confidence: 0.6,
			}
		}
	}

	return nil
}

func detectFactoryPattern(content, filePath, language string) *Pattern {
	if !strings.Contains(content, "Factory") && !strings.Contains(content, "factory") {
		return nil
	}

	factoryTerms := []string{"New", "Create", "Make", "Build"}
	for _, term := range factoryTerms {
		if strings.Contains(content, term) {
			snippet := extractSnippet(content, "Factory", 80)
			line := 1
			if idx := strings.Index(content, "Factory"); idx >= 0 {
				line = strings.Count(content[:idx], "\n") + 1
			}

			return &Pattern{
				Type:       "factory",
				File:       filePath,
				Line:       line,
				Snippet:    snippet,
				Confidence: 0.55,
			}
		}
	}

	return nil
}

func detectSingletonPattern(content, filePath, language string) *Pattern {
	if !strings.Contains(content, "singleton") && !strings.Contains(content, "Singleton") {
		return nil
	}

	snippet := extractSnippet(content, "Singleton", 80)
	line := 1
	if idx := strings.Index(content, "Singleton"); idx >= 0 {
		line = strings.Count(content[:idx], "\n") + 1
	}

	return &Pattern{
		Type:       "singleton",
		File:       filePath,
		Line:       line,
		Snippet:    snippet,
		Confidence: 0.8,
	}
}

func detectObserverPattern(content, filePath, language string) *Pattern {
	observerTerms := []string{"Observer", "Subject", "Publisher", "Subscriber", "EventBus", "PubSub", "pub/sub"}
	for _, term := range observerTerms {
		if strings.Contains(content, term) {
			snippet := extractSnippet(content, term, 80)
			line := 1
			if idx := strings.Index(content, term); idx >= 0 {
				line = strings.Count(content[:idx], "\n") + 1
			}

			return &Pattern{
				Type:       "observer",
				File:       filePath,
				Line:       line,
				Snippet:    snippet,
				Confidence: 0.7,
			}
		}
	}

	return nil
}

func detectMiddlewarePattern(content, filePath, language string) *Pattern {
	middlewareTerms := []string{"Middleware", "middleware", "Chain", "chain", "Handler", "handler"}
	foundCount := 0
	for _, term := range middlewareTerms {
		if strings.Contains(content, term) {
			foundCount++
		}
	}

	if foundCount >= 2 {
		snippet := extractSnippet(content, "Middleware", 80)
		line := 1
		if idx := strings.Index(content, "Middleware"); idx >= 0 {
			line = strings.Count(content[:idx], "\n") + 1
		}

		return &Pattern{
			Type:       "middleware",
			File:       filePath,
			Line:       line,
			Snippet:    snippet,
			Confidence: 0.6,
		}
	}

	return nil
}

func detectRESTPattern(content, filePath, language string) *Pattern {
	restTerms := []string{"GET", "POST", "PUT", "DELETE", "PATCH", "http.Handle", "gin.", "echo.", "mux.", "router"}
	for _, term := range restTerms {
		if strings.Contains(content, term) {
			snippet := extractSnippet(content, term, 80)
			line := 1
			if idx := strings.Index(content, term); idx >= 0 {
				line = strings.Count(content[:idx], "\n") + 1
			}

			return &Pattern{
				Type:       "rest-api",
				File:       filePath,
				Line:       line,
				Snippet:    snippet,
				Confidence: 0.75,
			}
		}
	}

	return nil
}

func detectErrorHandlingPattern(content, filePath, language string) *Pattern {
	errorTerms := []string{"error", "Error", "try{", "try {", "catch", "except", "recover", "Result<"}
	foundCount := 0
	for _, term := range errorTerms {
		if strings.Contains(content, term) {
			foundCount++
		}
	}

	if foundCount >= 3 {
		return &Pattern{
			Type:       "error-handling",
			File:       filePath,
			Line:       1,
			Snippet:    extractSnippet(content, "error", 60),
			Confidence: 0.5,
		}
	}

	return nil
}

func detectTestingPattern(content, filePath, language string) *Pattern {
	testTerms := []string{"Test", "test", "describe(", "it(", "expect(", "assert.", "mock", "Mock"}
	foundCount := 0
	for _, term := range testTerms {
		if strings.Contains(content, term) {
			foundCount++
		}
	}

	if foundCount >= 2 {
		snippet := extractSnippet(content, "Test", 80)
		line := 1
		if idx := strings.Index(content, "Test"); idx >= 0 {
			line = strings.Count(content[:idx], "\n") + 1
		}

		return &Pattern{
			Type:       "testing",
			File:       filePath,
			Line:       line,
			Snippet:    snippet,
			Confidence: 0.65,
		}
	}

	return nil
}

func detectConcurrencyPattern(content, filePath string) *Pattern {
	concurrencyTerms := []string{"goroutine", "go func", "chan ", "sync.", "Mutex", "WaitGroup", "context.Context"}
	foundCount := 0
	for _, term := range concurrencyTerms {
		if strings.Contains(content, term) {
			foundCount++
		}
	}

	if foundCount >= 2 {
		snippet := extractSnippet(content, "goroutine", 80)
		line := 1
		if idx := strings.Index(content, "goroutine"); idx >= 0 {
			line = strings.Count(content[:idx], "\n") + 1
		}

		return &Pattern{
			Type:       "concurrency",
			File:       filePath,
			Line:       line,
			Snippet:    snippet,
			Confidence: 0.75,
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// Skill mapping
// ---------------------------------------------------------------------------

// MapToSkills maps detected patterns and imports to existing skills in the
// knowledge graph. It returns a list of mappings linking patterns to skills.
func (a *Analyzer) MapToSkills(ctx context.Context, patterns []Pattern, store *skill.Store) ([]SkillMapping, error) {
	if len(patterns) == 0 {
		return nil, nil
	}

	a.logger.Info("mapping patterns to skills", zap.Int("patterns", len(patterns)))

	var mappings []SkillMapping
	var mu sync.Mutex

	// Query skills for each unique pattern type
	patternTypes := make(map[string]bool)
	for _, p := range patterns {
		patternTypes[p.Type] = true
	}

	for pType := range patternTypes {
		// Search for skills matching this pattern type
		results, err := store.Search(ctx, pType, 5)
		if err != nil {
			a.logger.Debug("skill search failed",
				zap.String("pattern", pType),
				zap.Error(err),
			)
			continue
		}

		for _, r := range results {
			// Find matching patterns
			for _, pat := range patterns {
				if pat.Type != pType {
					continue
				}

				score := calculateMappingScore(pat, r.Skill)
				if score > 0.5 {
					mu.Lock()
					mappings = append(mappings, SkillMapping{
						Pattern:   pat,
						SkillID:   r.Skill.ID,
						SkillName: r.Skill.Name,
						Score:     score,
					})
					mu.Unlock()
				}
			}
		}
	}

	a.logger.Info("pattern mapping complete", zap.Int("mappings", len(mappings)))

	return mappings, nil
}

// calculateMappingScore computes a relevance score between a pattern and a skill.
func calculateMappingScore(pattern Pattern, skill models.Skill) float64 {
	score := 0.0

	// Check if pattern type appears in skill name or title
	lowerType := strings.ToLower(pattern.Type)
	lowerName := strings.ToLower(skill.Name)
	lowerTitle := strings.ToLower(skill.Title)

	if strings.Contains(lowerName, lowerType) || strings.Contains(lowerTitle, lowerType) {
		score += 0.5
	}

	// Check if pattern type appears in description
	if strings.Contains(strings.ToLower(skill.Description), lowerType) {
		score += 0.3
	}

	// Boost by pattern confidence
	score += pattern.Confidence * 0.2

	return min(score, 1.0)
}

// ---------------------------------------------------------------------------
// Utilities
// ---------------------------------------------------------------------------

// detectLanguage determines the programming language from a file path.
func detectLanguage(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".java":
		return "java"
	case ".kt":
		return "kotlin"
	case ".c":
		return "c"
	case ".cpp", ".cxx", ".cc":
		return "cpp"
	case ".js":
		return "javascript"
	case ".ts":
		return "typescript"
	case ".rs":
		return "rust"
	case ".rb":
		return "ruby"
	case ".php":
		return "php"
	case ".swift":
		return "swift"
	case ".scala":
		return "scala"
	case ".r":
		return "r"
	case ".m", ".mm":
		return "objective-c"
	case ".cs":
		return "csharp"
	case ".sh", ".bash":
		return "bash"
	case ".dart":
		return "dart"
	default:
		return ""
	}
}

// extractSnippet extracts a text snippet around a keyword for display.
func extractSnippet(content, keyword string, maxLen int) string {
	idx := strings.Index(content, keyword)
	if idx < 0 {
		return ""
	}

	start := idx - maxLen/2
	if start < 0 {
		start = 0
	}
	end := idx + len(keyword) + maxLen/2
	if end > len(content) {
		end = len(content)
	}

	snippet := content[start:end]

	// Clean up snippet
	snippet = strings.TrimSpace(snippet)
	snippet = strings.Join(strings.Fields(snippet), " ")

	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(content) {
		snippet = snippet + "..."
	}

	return snippet
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// Evidence creation
// ---------------------------------------------------------------------------

// PatternsToEvidence converts detected patterns to evidence records that can
// be attached to skills in the knowledge graph.
func (a *Analyzer) PatternsToEvidence(patterns []Pattern, skillID uuid.UUID, projectPath string) []models.Evidence {
	var evidence []models.Evidence
	for _, p := range patterns {
		evidence = append(evidence, models.Evidence{
			ID:            uuid.New(),
			SkillID:       skillID,
			SourceProject: projectPath,
			SourceFile:    p.File,
			CodeSnippet:   p.Snippet,
			Pattern:       p.Type,
			Language:      detectLanguage(p.File),
			Validated:     false, // evidence needs validation before use
		})
	}
	return evidence
}

// MarshalJSON implements custom JSON marshaling for AnalysisResult.
func (r *AnalysisResult) MarshalJSON() ([]byte, error) {
	type Alias AnalysisResult
	return json.Marshal((*Alias)(r))
}
