package mapper

import (
	"testing"

	"github.com/HelixDevelopment/skills/internal/source/skillmd"
)

// TestChaos_NilParsedSkill_NoPanic verifies that Map handles nil input
// without panicking.
func TestChaos_NilParsedSkill_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Map(nil) panicked: %v", r)
		}
	}()
	_, _ = Map(nil, "test", nil, "github.com/test/repo")
}

// TestChaos_EmptyParsedSkill_NoPanic verifies that Map handles an empty
// ParsedSkill without panicking.
func TestChaos_EmptyParsedSkill_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Map(empty) panicked: %v", r)
		}
	}()
	_, _ = Map(&skillmd.ParsedSkill{}, "", nil, "")
}

// TestChaos_EmptyFields_NoPanic verifies that Map handles a ParsedSkill
// with empty string fields without panicking.
func TestChaos_EmptyFields_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Map(empty fields) panicked: %v", r)
		}
	}()
	parsed := &skillmd.ParsedSkill{
		Name:        "",
		Description: "",
	}
	_, _ = Map(parsed, "", nil, "")
}
