package skills

import (
	"os"
	"testing"
)

// TestMain dispatches the SIGKILL-chaos child: when re-executed with
// HELIX_SKILLS_CHAOS_CHILD=1 the process runs chaosChildMain instead of
// the test suite (the parent observes only the file the child leaves).
func TestMain(m *testing.M) {
	if os.Getenv("HELIX_SKILLS_CHAOS_CHILD") == "1" {
		chaosChildMain()
	}
	os.Exit(m.Run())
}
