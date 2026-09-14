package toon

import (
	"testing"
)

// TestChaos_CorruptInput_NoPanic verifies that Unmarshal does not panic on
// various forms of corrupt or malformed TOON input.
func TestChaos_CorruptInput_NoPanic(t *testing.T) {
	corruptInputs := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"binary garbage", "\x00\xFF\xDE\xAD"},
		{"truncated key", "id: 1\nnam"},
		{"unclosed array", "items[3: a, b"},
		{"only whitespace", "   \n\n  \t  "},
		{"single newline", "\n"},
		{"very long key", string(make([]byte, 50000)) + ": value"},
		{"nested garbage", "a:\n  b:\n    c: \x00"},
	}

	for _, tc := range corruptInputs {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Unmarshal panicked: %v", r)
				}
			}()
			var out interface{}
			_ = Unmarshal([]byte(tc.input), &out)
		})
	}
}

// TestChaos_Marshal_NilInput verifies that Marshal handles nil gracefully.
func TestChaos_Marshal_NilInput(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Marshal(nil) panicked: %v", r)
		}
	}()
	out, err := Marshal(nil)
	if err != nil {
		t.Logf("Marshal(nil) returned error: %v", err)
	}
	_ = out
}

// TestChaos_Marshal_EmptyMap verifies that Marshal handles empty input.
func TestChaos_Marshal_EmptyMap(t *testing.T) {
	out, err := Marshal(map[string]interface{}{})
	if err != nil {
		t.Logf("Marshal(empty) returned error: %v", err)
	}
	_ = out
}
