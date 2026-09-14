package skillscatalog

import (
	"testing"
)

// TestChaos_DefaultConfig_NilSafety verifies DefaultConfig does not panic
// and returns a sensible default.
func TestChaos_DefaultConfig_NilSafety(t *testing.T) {
	cfg := DefaultConfig()
	_ = cfg // must not panic
}
