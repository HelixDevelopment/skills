package skills

import "testing"

// Regression for the HXC-159 P4 review finding: mappedBack ranged over a Go
// map, so Allows("glob") on a filesystem:read skill allowed 31/200 and
// refused 169/200. Deterministic reverse map must allow 200/200.
func TestReverseSpellingDeterministic(t *testing.T) {
	s := Skill{Name: "probe", Capabilities: []string{CapFilesystemRead}}
	for i := 0; i < 200; i++ {
		if !s.Allows("glob") {
			t.Fatalf("iter %d: reverse spelling refused (nondeterminism)", i)
		}
	}
}
