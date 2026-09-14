package codeanalysis

import (
	"strings"
	"testing"

	"github.com/HelixDevelopment/skills/internal/models"
)

// ---------------------------------------------------------------------------
// detectLanguage: pure file-extension -> language mapping, no I/O.
// ---------------------------------------------------------------------------

func TestDetectLanguage(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"main.go", "go"},
		{"script.py", "python"},
		{"App.java", "java"},
		{"Main.kt", "kotlin"},
		{"lib.c", "c"},
		{"lib.cpp", "cpp"},
		{"lib.cxx", "cpp"},
		{"lib.cc", "cpp"},
		{"index.js", "javascript"},
		{"index.ts", "typescript"},
		{"main.rs", "rust"},
		{"app.rb", "ruby"},
		{"index.php", "php"},
		{"App.swift", "swift"},
		{"App.scala", "scala"},
		{"script.r", "r"},
		{"AppDelegate.m", "objective-c"},
		{"AppDelegate.mm", "objective-c"},
		{"Program.cs", "csharp"},
		{"run.sh", "bash"},
		{"run.bash", "bash"},
		{"main.dart", "dart"},
		{"README.md", ""},
		{"noextension", ""},
		{"nested/path/to/file.GO", "go"}, // extension matching is case-insensitive
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := detectLanguage(tt.path); got != tt.want {
				t.Errorf("detectLanguage(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// extractSnippet: pure string windowing around a keyword.
// ---------------------------------------------------------------------------

func TestExtractSnippet(t *testing.T) {
	tests := []struct {
		name    string
		content string
		keyword string
		maxLen  int
		want    string
	}{
		{
			name:    "keyword not present returns empty",
			content: "some content without the term",
			keyword: "Repository",
			maxLen:  80,
			want:    "",
		},
		{
			name:    "keyword at start, no leading ellipsis",
			content: "Controller does the routing",
			keyword: "Controller",
			maxLen:  40,
			want:    "Controller does the routing",
		},
		{
			// content = 40 'a's + "KEYWORD" (idx 40..46) + 86 'b's (len 133 total).
			// maxLen=10 -> start = 40 - 5 = 35, end = 40 + 7 + 5 = 52.
			// content[35:52] = 5 'a's + "KEYWORD" + 5 'b's, both truncated
			// (start>0 and end<len(content)) so both ellipses are added.
			name:    "keyword in the middle gets truncated with ellipsis on both sides",
			content: strings.Repeat("a", 40) + "KEYWORD" + strings.Repeat("b", 86),
			keyword: "KEYWORD",
			maxLen:  10,
			want:    "..." + strings.Repeat("a", 5) + "KEYWORD" + strings.Repeat("b", 5) + "...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSnippet(tt.content, tt.keyword, tt.maxLen)
			if got != tt.want {
				t.Errorf("extractSnippet(content, %q, %d) = %q, want %q", tt.keyword, tt.maxLen, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// calculateMappingScore: pure scoring of a detected pattern against a skill.
// ---------------------------------------------------------------------------

func TestCalculateMappingScore(t *testing.T) {
	tests := []struct {
		name    string
		pattern Pattern
		skill   models.Skill
		want    float64
	}{
		{
			name:    "no match anywhere yields only the confidence contribution",
			pattern: Pattern{Type: "singleton", Confidence: 0.5},
			skill:   models.Skill{Name: "unrelated", Title: "Unrelated Skill", Description: "does something else"},
			want:    0.5 * 0.2, // 0.1
		},
		{
			name:    "match in name contributes 0.5",
			pattern: Pattern{Type: "repository", Confidence: 0.0},
			skill:   models.Skill{Name: "repository-pattern", Title: "x", Description: "y"},
			want:    0.5,
		},
		{
			name:    "match in description contributes 0.3",
			pattern: Pattern{Type: "factory", Confidence: 0.0},
			skill:   models.Skill{Name: "x", Title: "y", Description: "uses the factory approach"},
			want:    0.3,
		},
		{
			name:    "match in name AND description AND high confidence caps at 1.0",
			pattern: Pattern{Type: "observer", Confidence: 1.0},
			skill:   models.Skill{Name: "observer-skill", Title: "x", Description: "implements the observer pattern"},
			want:    1.0, // 0.5 + 0.3 + 0.2 = 1.0, capped by min(_, 1.0)
		},
		{
			name:    "case-insensitive matching",
			pattern: Pattern{Type: "Singleton", Confidence: 0.0},
			skill:   models.Skill{Name: "SINGLETON-service", Title: "x", Description: "y"},
			want:    0.5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateMappingScore(tt.pattern, tt.skill)
			if !floatsAlmostEqual(got, tt.want, 1e-9) {
				t.Errorf("calculateMappingScore(%+v, %+v) = %v, want %v", tt.pattern, tt.skill, got, tt.want)
			}
		})
	}
}

func floatsAlmostEqual(a, b, epsilon float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff <= epsilon
}

// ---------------------------------------------------------------------------
// min: trivial pure helper, included for completeness of the pure-logic sweep.
// ---------------------------------------------------------------------------

func TestMin(t *testing.T) {
	tests := []struct {
		a, b, want float64
	}{
		{1.0, 2.0, 1.0},
		{2.0, 1.0, 1.0},
		{-1.0, 1.0, -1.0},
		{0.5, 0.5, 0.5},
	}
	for _, tt := range tests {
		if got := min(tt.a, tt.b); got != tt.want {
			t.Errorf("min(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// detectPatternsInFile / individual pattern detectors: pure regex/substring
// based heuristics over source text. These do not touch the Analyzer's cfg,
// logger, or parser fields, so a zero-value &Analyzer{} is safe to use.
// ---------------------------------------------------------------------------

func TestDetectPatternsInFile(t *testing.T) {
	a := &Analyzer{}

	tests := []struct {
		name       string
		content    string
		language   string
		wantType   string // a pattern type we expect to find among the results
		wantAbsent string // a pattern type we expect NOT to find
	}{
		{
			name:     "repository pattern detected",
			content:  "type UserRepository interface { FindByID(id string) (*User, error) }",
			language: "go",
			wantType: "repository",
		},
		{
			name:     "singleton pattern detected",
			content:  "var instance *Singleton\nfunc GetInstance() *Singleton { return instance }",
			language: "go",
			wantType: "singleton",
		},
		{
			name:     "dependency injection pattern detected",
			content:  "func NewService(repo Repository) *Service { return &Service{repo: repo} } // wire.Provide",
			language: "go",
			wantType: "dependency-injection",
		},
		{
			name:       "plain content matches nothing",
			content:    "package main\n\nfunc main() { println(\"hello\") }",
			language:   "go",
			wantAbsent: "singleton",
		},
		{
			name:     "concurrency pattern only detected for go",
			content:  "go func() { defer wg.Done() }()\nvar mu sync.Mutex\n_ = mu",
			language: "go",
			wantType: "concurrency",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patterns := a.detectPatternsInFile("file.go", []byte(tt.content), tt.language)

			if tt.wantType != "" && !patternTypesContain(patterns, tt.wantType) {
				t.Errorf("detectPatternsInFile() = %+v, want it to contain pattern type %q", patterns, tt.wantType)
			}
			if tt.wantAbsent != "" && patternTypesContain(patterns, tt.wantAbsent) {
				t.Errorf("detectPatternsInFile() = %+v, want it NOT to contain pattern type %q", patterns, tt.wantAbsent)
			}
		})
	}
}

func TestDetectPatternsInFile_ConcurrencyOnlyForGo(t *testing.T) {
	a := &Analyzer{}
	// Needs >= 2 distinct concurrency terms to cross detectConcurrencyPattern's
	// foundCount >= 2 threshold ("go func" + "sync.").
	content := "go func() { defer wg.Done() }()\nvar mu sync.Mutex"

	goPatterns := a.detectPatternsInFile("file.go", []byte(content), "go")
	if !patternTypesContain(goPatterns, "concurrency") {
		t.Errorf("expected concurrency pattern for language=go, got %+v", goPatterns)
	}

	pyPatterns := a.detectPatternsInFile("file.py", []byte(content), "python")
	if patternTypesContain(pyPatterns, "concurrency") {
		t.Errorf("concurrency pattern must only be detected for language=go, got %+v for python", pyPatterns)
	}
}

func patternTypesContain(patterns []Pattern, typ string) bool {
	for _, p := range patterns {
		if p.Type == typ {
			return true
		}
	}
	return false
}
