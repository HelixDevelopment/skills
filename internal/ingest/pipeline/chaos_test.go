package pipeline

import (
	"testing"

	"github.com/HelixDevelopment/skills/internal/ingest/source"
)

// TestChaos_SlugFromPath_EmptyPath verifies SlugFromPath handles empty input.
func TestChaos_SlugFromPath_EmptyPath(t *testing.T) {
	slug := SlugFromPath("")
	// Empty path should return empty slug (not panic).
	_ = slug
}

// TestChaos_SlugFromPath_SpecialChars verifies SlugFromPath handles paths
// with special characters.
func TestChaos_SlugFromPath_SpecialChars(t *testing.T) {
	paths := []string{
		"",
		"/",
		"///",
		"path/to/file with spaces.md",
		"path/to/文件.md",
		"path/to/file\x00name.md",
	}
	for _, p := range paths {
		slug := SlugFromPath(p)
		_ = slug // must not panic
	}
}

// TestChaos_NormalizeContent_Empty verifies NormalizeContent handles empty input.
func TestChaos_NormalizeContent_Empty(t *testing.T) {
	result := NormalizeContent("", source.ItemRef{})
	_ = result // must not panic
}

// TestChaos_NormalizeContent_Binary verifies NormalizeContent handles binary
// garbage without panicking.
func TestChaos_NormalizeContent_Binary(t *testing.T) {
	result := NormalizeContent("\x00\xFF\xDE\xAD", source.ItemRef{})
	_ = result // must not panic
}

// TestChaos_DescriptionFrom_Empty verifies descriptionFrom handles empty input.
func TestChaos_DescriptionFrom_Empty(t *testing.T) {
	desc := descriptionFrom("")
	_ = desc // must not panic
}

// TestChaos_DescriptionFrom_Binary verifies descriptionFrom handles binary
// garbage without panicking.
func TestChaos_DescriptionFrom_Binary(t *testing.T) {
	desc := descriptionFrom("\x00\xFF\xDE\xAD")
	_ = desc // must not panic
}
