package codegraph

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Mock implementations
// ---------------------------------------------------------------------------

// mockSkillSearcher implements SkillSearcher for testing.
type mockSkillSearcher struct {
	results map[string][]uuid.UUID
}

func newMockSkillSearcher() *mockSkillSearcher {
	return &mockSkillSearcher{
		results: make(map[string][]uuid.UUID),
	}
}

func (m *mockSkillSearcher) SearchByKeyword(_ context.Context, keyword string, limit int) ([]uuid.UUID, error) {
	ids, ok := m.results[keyword]
	if !ok {
		return nil, nil
	}
	if len(ids) > limit {
		return ids[:limit], nil
	}
	return ids, nil
}

// mockMCPClientForPatterns creates a minimal MCPClient that reports as
// unavailable, so pattern extraction methods that call through the client
// gracefully degrade. For unit tests we inject symbols/deps directly via
// the IndexManager.
func mockMCPClientForPatterns() *MCPClient {
	return &MCPClient{
		logger: zap.NewNop(),
		avail:  false,
	}
}

// ---------------------------------------------------------------------------
// PatternExtractor tests
// ---------------------------------------------------------------------------

func TestPatternExtractor_ExtractPatterns_Unavailable(t *testing.T) {
	client := mockMCPClientForPatterns()
	index := NewIndexManager(client, zap.NewNop())
	searcher := newMockSkillSearcher()
	cfg := DefaultPatternExtractorConfig()

	pe := NewPatternExtractor(client, index, searcher, cfg, zap.NewNop())

	patterns, err := pe.ExtractPatterns(context.Background(), "/tmp/test")
	if err != nil {
		t.Fatalf("ExtractPatterns should not error when unavailable, got: %v", err)
	}
	// Should return empty (no symbols available).
	if len(patterns) != 0 {
		t.Errorf("expected 0 patterns when unavailable, got %d", len(patterns))
	}
}

func TestClampConfidence(t *testing.T) {
	tests := []struct {
		in   float64
		want float64
	}{
		{0.0, 0.0},
		{0.5, 0.5},
		{1.0, 1.0},
		{-0.1, 0.0},
		{1.5, 1.0},
		{0.999, 0.999},
	}

	for _, tt := range tests {
		got := clampConfidence(tt.in)
		if got != tt.want {
			t.Errorf("clampConfidence(%f) = %f, want %f", tt.in, got, tt.want)
		}
	}
}

func TestFindSymbolFiles(t *testing.T) {
	symbols := []Symbol{
		{Name: "Repo", Kind: "interface", File: "repo.go"},
		{Name: "UserRepo", Kind: "interface", File: "user_repo.go"},
		{Name: "main", Kind: "function", File: "main.go"},
		{Name: "RepoImpl", Kind: "struct", File: "repo.go"}, // duplicate file
	}

	files := findSymbolFiles(symbols, func(s Symbol) bool {
		return s.Kind == "interface"
	})

	if len(files) != 2 {
		t.Errorf("got %d files, want 2", len(files))
	}

	// Verify deduplication.
	seen := make(map[string]bool)
	for _, f := range files {
		if seen[f] {
			t.Errorf("duplicate file in result: %s", f)
		}
		seen[f] = true
	}
}

func TestNormalizePackageName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"github.com/foo/bar.Baz", "bar"},
		{"net/http.Handler", "http"},
		{"fmt.Println", "fmt"},
		{"github.com/HelixDevelopment/skills/internal/models", "github"},
		{"simple", "simple"},
		{"", ""},
	}

	for _, tt := range tests {
		got := normalizePackageName(tt.in)
		if got != tt.want {
			t.Errorf("normalizePackageName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestContains(t *testing.T) {
	slice := []string{"a", "b", "c"}
	if !contains(slice, "b") {
		t.Error("expected contains to find 'b'")
	}
	if contains(slice, "d") {
		t.Error("expected contains to NOT find 'd'")
	}
	if contains(nil, "a") {
		t.Error("expected contains(nil, ...) to be false")
	}
}

func TestIsAPIRelated(t *testing.T) {
	tests := []struct {
		kind string
		want bool
	}{
		{"function", true},
		{"method", true},
		{"interface", true},
		{"struct", false},
		{"variable", false},
		{"constant", false},
		{"", false},
	}

	for _, tt := range tests {
		got := isAPIRelated(Symbol{Kind: tt.kind})
		if got != tt.want {
			t.Errorf("isAPIRelated(%q) = %v, want %v", tt.kind, got, tt.want)
		}
	}
}

func TestMapToSkill_Found(t *testing.T) {
	client := mockMCPClientForPatterns()
	index := NewIndexManager(client, zap.NewNop())

	skillID := uuid.New()
	searcher := newMockSkillSearcher()
	searcher.results["repository-pattern"] = []uuid.UUID{skillID}

	pe := NewPatternExtractor(client, index, searcher, DefaultPatternExtractorConfig(), zap.NewNop())

	pattern := &ExtractedPattern{
		ID:   uuid.New(),
		Name: "repository-pattern",
	}

	pe.mapToSkill(context.Background(), pattern)

	if pattern.SkillID == nil {
		t.Fatal("expected SkillID to be set")
	}
	if *pattern.SkillID != skillID {
		t.Errorf("SkillID = %v, want %v", *pattern.SkillID, skillID)
	}
	if pattern.SuggestedSkillName != "" {
		t.Errorf("SuggestedSkillName should be empty when skill found, got %q", pattern.SuggestedSkillName)
	}
}

func TestMapToSkill_NotFound(t *testing.T) {
	client := mockMCPClientForPatterns()
	index := NewIndexManager(client, zap.NewNop())
	searcher := newMockSkillSearcher()

	pe := NewPatternExtractor(client, index, searcher, DefaultPatternExtractorConfig(), zap.NewNop())

	pattern := &ExtractedPattern{
		ID:   uuid.New(),
		Name: "novel-pattern:foo",
	}

	pe.mapToSkill(context.Background(), pattern)

	if pattern.SkillID != nil {
		t.Error("expected SkillID to be nil when no match")
	}
	if pattern.SuggestedSkillName != "novel-pattern:foo" {
		t.Errorf("SuggestedSkillName = %q, want %q", pattern.SuggestedSkillName, "novel-pattern:foo")
	}
}

func TestMapToSkill_NilSearcher(t *testing.T) {
	client := mockMCPClientForPatterns()
	index := NewIndexManager(client, zap.NewNop())

	pe := NewPatternExtractor(client, index, nil, DefaultPatternExtractorConfig(), zap.NewNop())

	pattern := &ExtractedPattern{
		ID:   uuid.New(),
		Name: "some-pattern",
	}

	pe.mapToSkill(context.Background(), pattern)

	if pattern.SkillID != nil {
		t.Error("expected SkillID to be nil with nil searcher")
	}
	if pattern.SuggestedSkillName != "some-pattern" {
		t.Errorf("SuggestedSkillName = %q, want %q", pattern.SuggestedSkillName, "some-pattern")
	}
}

func TestFilterAndCap(t *testing.T) {
	client := mockMCPClientForPatterns()
	index := NewIndexManager(client, zap.NewNop())

	cfg := PatternExtractorConfig{
		MinConfidence: 0.5,
		MaxPatterns:   3,
	}
	pe := NewPatternExtractor(client, index, nil, cfg, zap.NewNop())

	input := []ExtractedPattern{
		{ID: uuid.New(), Confidence: 0.9},
		{ID: uuid.New(), Confidence: 0.3}, // below threshold
		{ID: uuid.New(), Confidence: 0.7},
		{ID: uuid.New(), Confidence: 0.6},
		{ID: uuid.New(), Confidence: 0.8},
		{ID: uuid.New(), Confidence: 0.1}, // below threshold
	}

	result := pe.filterAndCap(input)

	if len(result) != 3 {
		t.Fatalf("got %d results, want 3", len(result))
	}

	// Should be sorted by confidence descending.
	if result[0].Confidence < result[1].Confidence {
		t.Error("results not sorted by confidence descending")
	}
	if result[1].Confidence < result[2].Confidence {
		t.Error("results not sorted by confidence descending")
	}

	// All should be >= 0.5.
	for _, p := range result {
		if p.Confidence < 0.5 {
			t.Errorf("pattern with confidence %f should have been filtered", p.Confidence)
		}
	}
}

func TestDefaultPatternExtractorConfig(t *testing.T) {
	cfg := DefaultPatternExtractorConfig()
	if cfg.MinConfidence != 0.4 {
		t.Errorf("MinConfidence = %f, want 0.4", cfg.MinConfidence)
	}
	if cfg.MaxPatterns != 200 {
		t.Errorf("MaxPatterns = %d, want 200", cfg.MaxPatterns)
	}
}

func TestPatternCategory_Constants(t *testing.T) {
	// Verify category constants are distinct.
	cats := []PatternCategory{
		PatternAPIUsage,
		PatternArchitecture,
		PatternDependency,
		PatternConfiguration,
	}
	seen := make(map[PatternCategory]bool)
	for _, c := range cats {
		if seen[c] {
			t.Errorf("duplicate category: %q", c)
		}
		seen[c] = true
	}
}
