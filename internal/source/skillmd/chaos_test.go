package skillmd

import (
	"strings"
	"testing"
)

// TestChaos_MalformedInput_NoPanic verifies that Parse does not panic on
// various forms of malformed skill markdown.
func TestChaos_MalformedInput_NoPanic(t *testing.T) {
	corruptInputs := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"binary garbage", "\x00\xFF\xDE\xAD"},
		{"unclosed frontmatter", "---\nname: test\nversion: 1.0"},
		{"only frontmatter delimiters", "---\n---"},
		{"broken YAML in frontmatter", "---\nname: [broken\n---"},
		{"very long line", "# Title\n\n" + strings.Repeat("a", 100000)},
		{"unicode gibberish", "💥🔥🚀\x00\x01"},
		{"null bytes in content", "name: test\x00\nversion: 1.0"},
		{"no body after frontmatter", "---\nname: test\nversion: 1.0\n---"},
	}

	for _, tc := range corruptInputs {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Parse panicked: %v", r)
				}
			}()
			_, _ = Parse([]byte(tc.raw), "chaos.md")
		})
	}
}

// TestChaos_RecoveryAfterBadInput verifies that a valid parse succeeds after
// a barrage of malformed inputs (parser not poisoned).
func TestChaos_RecoveryAfterBadInput(t *testing.T) {
	// Feed corrupt inputs.
	for _, raw := range []string{"", "\x00", "---\n---", "💥"} {
		_, _ = Parse([]byte(raw), "bad.md")
	}

	// Valid parse must still work.
	valid := []byte(`---
name: recovery-skill
version: "1.0.0"
title: Recovery
description: Tests parser recovery
kind: atomic
status: active
---

# Recovery

Parser should work fine after bad inputs.
`)
	parsed, err := Parse(valid, "recovery.md")
	if err != nil {
		t.Fatalf("recovery parse failed: %v", err)
	}
	if parsed.Name != "recovery-skill" {
		t.Errorf("recovery name: want recovery-skill, got %s", parsed.Name)
	}
}
