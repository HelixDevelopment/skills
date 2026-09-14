package codeanalysis

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// ValidateProjectPath: §G31 path-traversal / LFI guard.
//
// These tests exercise the REAL ValidateProjectPath function the
// learn_from_project MCP handler (internal/mcp/tools.go) and
// Analyzer.AnalyzeProject (analyzer.go) both call -- not a re-implementation
// of its logic. Every rejection case MUST return an error and MUST NOT walk
// any filesystem beyond what canonicalization itself touches (Abs/Clean/
// EvalSymlinks); acceptance MUST return the canonicalized in-root path.
// ---------------------------------------------------------------------------

// setupRoot creates a temp directory tree:
//
//	<tmp>/root/            -- the allowed root
//	<tmp>/root/child/       -- a legitimate in-root subdirectory
//	<tmp>/outside/          -- a sibling directory OUTSIDE the root
//	<tmp>/rootEVIL/         -- a sibling whose NAME shares the root as a
//	                           string prefix, but is NOT a descendant --
//	                           the exact case a raw strings.HasPrefix check
//	                           would wrongly accept.
//
// It returns the absolute, symlink-resolved root path (what
// ValidateProjectPath itself would canonicalize allowedRoot to), so tests
// can assert against it directly.
func setupRoot(t *testing.T) (root, child, outside, rootEvil string) {
	t.Helper()
	tmp := t.TempDir()

	root = filepath.Join(tmp, "root")
	child = filepath.Join(root, "child")
	outside = filepath.Join(tmp, "outside")
	rootEvil = filepath.Join(tmp, "rootEVIL")

	for _, dir := range []string{child, outside, rootEvil} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, err)
		}
	}

	canonRoot, err := canonicalize(root)
	if err != nil {
		t.Fatalf("canonicalize(root) setup failed: %v", err)
	}
	return canonRoot, child, outside, rootEvil
}

// ---------------------------------------------------------------------------
// SECURITY: traversal, symlink-escape, absolute-outside-root -- all rejected.
// ---------------------------------------------------------------------------

func TestValidateProjectPath_Security_RelativeTraversalRejected(t *testing.T) {
	root, _, _, _ := setupRoot(t)

	// "../outside" relative to root lexically escapes after Clean, and
	// resolves to a real, existing sibling directory (so EvalSymlinks
	// succeeds and the rejection MUST come from the boundary check, not
	// merely from a non-existence error).
	traversal := filepath.Join(root, "..", "outside")

	got, err := ValidateProjectPath(traversal, root)
	if err == nil {
		t.Fatalf("ValidateProjectPath(%q, %q) = (%q, nil), want a rejection error", traversal, root, got)
	}
	if got != "" {
		t.Errorf("ValidateProjectPath returned non-empty path %q on rejection, want empty", got)
	}
	if !strings.Contains(err.Error(), "escapes the allowed root") {
		t.Errorf("error = %q, want it to mention the escape", err.Error())
	}
}

func TestValidateProjectPath_Security_DeepRelativeTraversalRejected(t *testing.T) {
	root, child, _, _ := setupRoot(t)

	// From an in-root child, "../../outside" still escapes -- proves the
	// guard collapses MULTIPLE ".." segments, not just a single hop.
	traversal := filepath.Join(child, "..", "..", "outside")

	if _, err := ValidateProjectPath(traversal, root); err == nil {
		t.Fatalf("ValidateProjectPath(%q, %q) = nil error, want a rejection for a multi-segment traversal", traversal, root)
	}
}

func TestValidateProjectPath_Security_SymlinkEscapeRejected(t *testing.T) {
	root, _, outside, _ := setupRoot(t)

	// A symlink PLANTED INSIDE the allowed root that resolves OUTSIDE it --
	// the case a purely lexical (non-EvalSymlinks) check would miss.
	link := filepath.Join(root, "escape-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink creation unsupported in this environment: %v", err)
	}

	got, err := ValidateProjectPath(link, root)
	if err == nil {
		t.Fatalf("ValidateProjectPath(%q, %q) = (%q, nil), want a rejection: symlink resolves outside root", link, root, got)
	}
	if !strings.Contains(err.Error(), "escapes the allowed root") {
		t.Errorf("error = %q, want it to mention the escape", err.Error())
	}
}

func TestValidateProjectPath_Security_AbsoluteOutsideRootRejected(t *testing.T) {
	root, _, outside, _ := setupRoot(t)

	got, err := ValidateProjectPath(outside, root)
	if err == nil {
		t.Fatalf("ValidateProjectPath(%q, %q) = (%q, nil), want a rejection for an absolute path outside root", outside, root, got)
	}
}

func TestValidateProjectPath_Security_SiblingSharingRootAsStringPrefixRejected(t *testing.T) {
	root, _, _, rootEvil := setupRoot(t)

	// The pitfall the task explicitly warns against: a raw
	// strings.HasPrefix(candidate, root) would wrongly ACCEPT rootEvil
	// (e.g. "/tmp/xxx/rootEVIL" for root "/tmp/xxx/root") because the
	// string "/tmp/xxx/root" is a byte-prefix of "/tmp/xxx/rootEVIL" even
	// though rootEvil is NOT a descendant of root on the filesystem.
	got, err := ValidateProjectPath(rootEvil, root)
	if err == nil {
		t.Fatalf("ValidateProjectPath(%q, %q) = (%q, nil), want a rejection: %q merely shares root as a STRING prefix, it is not a descendant",
			rootEvil, root, got, rootEvil)
	}
}

func TestValidateProjectPath_Security_NonExistentPathRejected(t *testing.T) {
	root, _, _, _ := setupRoot(t)
	doesNotExist := filepath.Join(root, "no-such-subdir", "deeper")

	if _, err := ValidateProjectPath(doesNotExist, root); err == nil {
		t.Fatalf("ValidateProjectPath(%q, %q) = nil error, want a rejection for a non-existent path (fail-closed)", doesNotExist, root)
	}
}

func TestValidateProjectPath_Security_UnconfiguredAllowedRootRejectsEverything(t *testing.T) {
	root, _, _, _ := setupRoot(t)

	// Even a perfectly legitimate in-root path MUST be rejected when the
	// operator has not configured an allowed root at all -- fail-closed,
	// never "allow-list the whole filesystem" by default.
	if _, err := ValidateProjectPath(root, ""); err == nil {
		t.Fatal("ValidateProjectPath(root, \"\") = nil error, want a rejection: an unset allowed root must reject everything")
	}
}

func TestValidateProjectPath_Security_EmptyProjectPathRejected(t *testing.T) {
	root, _, _, _ := setupRoot(t)
	if _, err := ValidateProjectPath("", root); err == nil {
		t.Fatal(`ValidateProjectPath("", root) = nil error, want a rejection for an empty project_path`)
	}
}

// ---------------------------------------------------------------------------
// UNIT: a legitimate in-root path is accepted and canonicalized correctly.
// ---------------------------------------------------------------------------

func TestValidateProjectPath_Unit_InRootPathAccepted(t *testing.T) {
	root, child, _, _ := setupRoot(t)

	got, err := ValidateProjectPath(child, root)
	if err != nil {
		t.Fatalf("ValidateProjectPath(%q, %q) returned unexpected error: %v", child, root, err)
	}

	wantCanon, err := canonicalize(child)
	if err != nil {
		t.Fatalf("canonicalize(child) failed: %v", err)
	}
	if got != wantCanon {
		t.Errorf("ValidateProjectPath(%q, %q) = %q, want canonicalized %q", child, root, got, wantCanon)
	}
}

func TestValidateProjectPath_Unit_RootItselfAccepted(t *testing.T) {
	root, _, _, _ := setupRoot(t)

	got, err := ValidateProjectPath(root, root)
	if err != nil {
		t.Fatalf("ValidateProjectPath(root, root) returned unexpected error: %v", err)
	}
	if got != root {
		t.Errorf("ValidateProjectPath(root, root) = %q, want %q (the root itself must be accepted)", got, root)
	}
}

func TestValidateProjectPath_Unit_RelativeInRootPathIsCanonicalized(t *testing.T) {
	root, child, _, _ := setupRoot(t)

	// A path expressed with a redundant "./" + no traversal must still
	// resolve to the same canonical child, proving Clean/Abs normalization
	// runs even on the accept path.
	redundant := filepath.Join(root, ".", "child")

	got, err := ValidateProjectPath(redundant, root)
	if err != nil {
		t.Fatalf("ValidateProjectPath(%q, %q) returned unexpected error: %v", redundant, root, err)
	}
	wantCanon, err := canonicalize(child)
	if err != nil {
		t.Fatalf("canonicalize(child) failed: %v", err)
	}
	if got != wantCanon {
		t.Errorf("ValidateProjectPath(%q, %q) = %q, want %q", redundant, root, got, wantCanon)
	}
}

// ---------------------------------------------------------------------------
// isWithinRoot: direct unit coverage of the boundary comparison itself.
// ---------------------------------------------------------------------------

func TestIsWithinRoot(t *testing.T) {
	sep := string(os.PathSeparator)
	tests := []struct {
		name      string
		candidate string
		root      string
		want      bool
	}{
		{"exact match", "/a/b", "/a/b", true},
		{"descendant", "/a/b/c", "/a/b", true},
		{"deep descendant", "/a/b/c/d/e", "/a/b", true},
		{"sibling sharing string prefix (the HasPrefix pitfall)", "/a/bEVIL", "/a/b", false},
		{"unrelated path", "/x/y", "/a/b", false},
		{"parent of root is NOT within root", "/a", "/a/b", false},
		{"root already has trailing separator", "/a/b/c", "/a/b" + sep, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isWithinRoot(tt.candidate, tt.root); got != tt.want {
				t.Errorf("isWithinRoot(%q, %q) = %v, want %v", tt.candidate, tt.root, got, tt.want)
			}
		})
	}
}
