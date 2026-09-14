package codeanalysis

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HelixDevelopment/skills/internal/config"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// REGRESSION: Analyzer.AnalyzeProject enforces the §G31 guard itself
// (defense-in-depth), independent of whatever caller reaches it. This
// matters because the register's DECISION explicitly requires the guard to
// land "WITH or BEFORE G03" -- i.e. before/alongside the (currently
// unwired) analyzer ever gets invoked in production. Gating here means a
// future G03 wiring change that calls AnalyzeProject directly, without
// remembering to re-check the caller-side guard, still cannot walk outside
// the allowed root.
// ---------------------------------------------------------------------------

func TestAnalyzeProject_RejectsPathOutsideAllowedRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	a := NewAnalyzer(config.CodeAnalysisConfig{
		Enabled:     true,
		AllowedRoot: root,
	}, zap.NewNop())

	result, err := a.AnalyzeProject(context.Background(), outside)
	if err == nil {
		t.Fatalf("AnalyzeProject(%q) with AllowedRoot=%q = (result, nil), want a rejection error", outside, root)
	}
	if result != nil {
		t.Errorf("AnalyzeProject returned a non-nil result on rejection: %+v", result)
	}
	if !strings.Contains(err.Error(), "project_path rejected") {
		t.Errorf("error = %q, want it to surface the project_path rejection", err.Error())
	}
}

func TestAnalyzeProject_RejectsTraversalOutsideAllowedRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	a := NewAnalyzer(config.CodeAnalysisConfig{
		Enabled:     true,
		AllowedRoot: root,
	}, zap.NewNop())

	traversal := filepath.Join(root, "..", filepath.Base(outside))
	if _, err := a.AnalyzeProject(context.Background(), traversal); err == nil {
		t.Fatalf("AnalyzeProject(%q) = nil error, want a rejection for a path traversing outside AllowedRoot", traversal)
	}
}

func TestAnalyzeProject_RejectsWhenNoAllowedRootConfigured(t *testing.T) {
	root := t.TempDir() // would be legitimate content, but no root is configured

	a := NewAnalyzer(config.CodeAnalysisConfig{Enabled: true}, zap.NewNop())

	if _, err := a.AnalyzeProject(context.Background(), root); err == nil {
		t.Fatal("AnalyzeProject with an unconfigured AllowedRoot = nil error, want fail-closed rejection")
	}
}

func TestAnalyzeProject_AcceptsLegitimateInRootPath(t *testing.T) {
	root := t.TempDir()
	srcFile := filepath.Join(root, "main.go")
	if err := os.WriteFile(srcFile, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write fixture source file: %v", err)
	}

	a := NewAnalyzer(config.CodeAnalysisConfig{
		Enabled:       true,
		Languages:     []string{"go"},
		MaxFileSizeKB: 500, // discoverFiles skips any file above this; zero-value would skip everything
		AllowedRoot:   root,
	}, zap.NewNop())

	result, err := a.AnalyzeProject(context.Background(), root)
	if err != nil {
		t.Fatalf("AnalyzeProject(%q) with AllowedRoot=%q returned unexpected error: %v", root, root, err)
	}
	if result == nil {
		t.Fatal("AnalyzeProject returned a nil result for a legitimate in-root path")
	}
	if result.Languages["go"] != 1 {
		t.Errorf("Languages[\"go\"] = %d, want 1 (the walk must actually run for an accepted path)", result.Languages["go"])
	}
}
