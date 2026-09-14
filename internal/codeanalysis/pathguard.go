package codeanalysis

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ---------------------------------------------------------------------------
// Path-traversal / LFI guard (§G31, GAPS_AND_RISKS_REGISTER.md)
//
// learn_from_project (internal/mcp/tools.go) accepts an operator/agent
// supplied project_path and, once G03 wires the analyzer in, that path is
// walked by discoverFiles below with no boundary check of its own. On the
// (currently unauthenticated) MCP surface an attacker-supplied project_path
// ("/etc", "../../", an absolute host path) would become a local-file-
// inclusion / directory-traversal read primitive the moment that wiring
// lands. ValidateProjectPath closes that hole at the source: it MUST be
// called, and MUST reject, BEFORE any filesystem walk starts.
// ---------------------------------------------------------------------------

// ValidateProjectPath canonicalizes projectPath and verifies it resolves
// strictly inside allowedRoot. It is FAIL-CLOSED: an unset allowedRoot, an
// empty projectPath, any resolution error (including a path that does not
// exist), or a canonical path that escapes allowedRoot's boundary all result
// in a rejection -- never a walk.
//
// On success it returns the fully canonicalized (absolute, symlink-resolved,
// cleaned) form of projectPath, which callers should use in place of the
// raw, attacker-influenced input for any subsequent filesystem operation.
//
// Canonicalization order:
//  1. filepath.Abs + filepath.Clean on both allowedRoot and projectPath, so
//     relative traversal segments ("../..") are collapsed before comparison.
//  2. filepath.EvalSymlinks on both, so a symlink planted INSIDE allowedRoot
//     that resolves OUTSIDE it (or vice versa) cannot be used to smuggle an
//     escape past a purely lexical check.
//  3. A path-BOUNDARY comparison (isWithinRoot) of the two canonical forms
//     -- never a raw strings.HasPrefix, which would incorrectly accept a
//     sibling directory such as "/rootEVIL" for an allowedRoot of "/root".
func ValidateProjectPath(projectPath, allowedRoot string) (string, error) {
	if strings.TrimSpace(allowedRoot) == "" {
		return "", fmt.Errorf(
			"project_path rejected: no allowlisted root is configured " +
				"(codeanalysis.allowed_root / HELIX_CODEANALYSIS_ALLOWED_ROOT); " +
				"learn_from_project refuses every submission until an allowed root is set",
		)
	}
	if strings.TrimSpace(projectPath) == "" {
		return "", fmt.Errorf("project_path rejected: empty path")
	}

	canonRoot, err := canonicalize(allowedRoot)
	if err != nil {
		return "", fmt.Errorf("project_path rejected: allowed root %q does not resolve: %w", allowedRoot, err)
	}

	canonPath, err := canonicalize(projectPath)
	if err != nil {
		return "", fmt.Errorf("project_path rejected: %q does not resolve: %w", projectPath, err)
	}

	if !isWithinRoot(canonPath, canonRoot) {
		return "", fmt.Errorf("project_path rejected: %q escapes the allowed root %q", projectPath, allowedRoot)
	}

	return canonPath, nil
}

// canonicalize resolves p to an absolute, symlink-free, cleaned path.
// filepath.EvalSymlinks requires every path component to exist, so a
// non-existent path (typo, not-yet-created directory, attacker probe)
// deterministically fails here rather than being accepted lexically.
func canonicalize(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path: %w", err)
	}
	// filepath.Abs already calls Clean, but Clean is idempotent and cheap;
	// spelling it out documents the traversal-collapse step explicitly.
	abs = filepath.Clean(abs)

	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("evaluate symlinks: %w", err)
	}
	return resolved, nil
}

// isWithinRoot reports whether candidate IS root, or is a descendant of
// root, comparing at a path-SEPARATOR boundary. Both arguments MUST already
// be canonicalized (absolute, symlink-resolved, cleaned) by the caller.
//
// A raw strings.HasPrefix(candidate, root) would wrongly accept a sibling
// path that merely shares root as a string prefix without the trailing
// separator -- e.g. root="/data/projects" would incorrectly admit the
// sibling directory "/data/projectsEVIL". Appending the separator to root
// before the prefix check (and short-circuiting the candidate==root case,
// since root+separator is never a prefix of itself) closes that gap.
func isWithinRoot(candidate, root string) bool {
	if candidate == root {
		return true
	}
	sep := string(os.PathSeparator)
	rootBoundary := root
	if !strings.HasSuffix(rootBoundary, sep) {
		rootBoundary += sep
	}
	return strings.HasPrefix(candidate, rootBoundary)
}
