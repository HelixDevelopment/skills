package worker

// G03 remediation (the codeanalysis+worker half): unit-test spy for the
// codeAnalyzer seam and proof that Runner.handleCodeAnalysis dispatches
// through it -- mirroring the EXACT pattern established by
// autoexpand_unit_test.go (spyAutoExpander) and
// registryreview_unit_test.go (spyRegistryReviewer).
//
// The spy-based convention (§11.4.27 permits mocks/spies ONLY in unit
// tests) exercises the handler-invoked-function level: no real database,
// no real filesystem. The full end-to-end path (real codeanalysis.Analyzer
// over a real filesystem) is exercised by the integration test.
//
// RED (pre-fix, this file added but runner.go NOT yet changed): this
// file does not compile -- Runner has no `codeAnalyzer` field and no
// `codeAnalyzer` interface exists, because handleCodeAnalysis's pre-fix
// body only unmarshaled the payload and returned a stub JobResult. A build
// failure is the correct, honest RED signal for "the seam this test
// exercises does not exist yet".
//
// GREEN (post-fix): runner.go gains the codeAnalyzer interface + field,
// wired in NewRunner, and handleCodeAnalysis dispatches through it.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/HelixDevelopment/skills/internal/codeanalysis"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// spyCodeAnalyzer is a unit-test-only stand-in for the codeAnalyzer seam.
// It never touches a real filesystem. It records the exact project path it
// was invoked with and returns a caller-configured canned result/error, so
// tests can assert both "was it called with the right project path" and
// "did the handler thread the result through correctly".
type spyCodeAnalyzer struct {
	called     int32
	gotProject string
	retResult  *codeanalysis.AnalysisResult
	retErr     error
}

func (s *spyCodeAnalyzer) AnalyzeProject(_ context.Context, projectPath string) (*codeanalysis.AnalysisResult, error) {
	atomic.AddInt32(&s.called, 1)
	s.gotProject = projectPath
	return s.retResult, s.retErr
}

// errCodeAnalysisSentinel mirrors registryreview_unit_test.go's
// errSpyReviewSentinel convention: a deliberate sentinel error so the
// handler's error-propagation path is exercised for real.
var errCodeAnalysisSentinel = errors.New("spyCodeAnalyzer: sentinel error, analyze was invoked")

// TestHandleCodeAnalysis_InvokesAnalyzerWithProjectPath proves the primary
// G03 wiring: given a code-analysis job, the handler calls the analyzer
// seam with the project_path extracted from the job payload (not a stub
// that ignores it), and surfaces the analyzer's result as a successful
// JobResult carrying the marshaled AnalysisResult.
func TestHandleCodeAnalysis_InvokesAnalyzerWithProjectPath(t *testing.T) {
	spy := &spyCodeAnalyzer{
		retResult: &codeanalysis.AnalysisResult{
			ProjectPath: "/test/project",
			Languages:   map[string]int{"go": 5, "python": 3},
			Imports: []codeanalysis.Import{
				{Path: "fmt", File: "/test/project/main.go", Line: 1, Language: "go"},
			},
		},
	}

	r := &Runner{
		codeAnalyzer: spy,
		logger:       zap.NewNop(),
	}

	job := Job{
		ID:      uuid.New(),
		Type:    JobTypeCodeAnalysis,
		Payload: json.RawMessage(`{"project_path":"/test/project"}`),
	}

	result := r.handleCodeAnalysis(context.Background(), job)

	if got := atomic.LoadInt32(&spy.called); got != 1 {
		t.Fatalf("codeAnalyzer.AnalyzeProject called %d times, want 1: handleCodeAnalysis must dispatch "+
			"through the code analyzer seam exactly once per job", got)
	}
	if spy.gotProject != "/test/project" {
		t.Errorf("AnalyzeProject called with projectPath = %q, want %q (from the job payload)", spy.gotProject, "/test/project")
	}

	if !result.Success {
		t.Fatalf("JobResult.Success = false, want true (error=%q)", result.Error)
	}

	var got codeanalysis.AnalysisResult
	if err := json.Unmarshal(result.Data, &got); err != nil {
		t.Fatalf("JobResult.Data is not a valid marshaled AnalysisResult: %v (data=%s)", err, result.Data)
	}
	if got.ProjectPath != "/test/project" {
		t.Errorf("JobResult.Data.ProjectPath = %q, want %q (the analyzer's real return value, "+
			"not a hand-rolled stub payload)", got.ProjectPath, "/test/project")
	}
	if len(got.Imports) != 1 {
		t.Errorf("JobResult.Data.Imports count = %d, want 1 (the analyzer's real return value)", len(got.Imports))
	}
	if got.Languages["go"] != 5 {
		t.Errorf("JobResult.Data.Languages[go] = %d, want 5 (the analyzer's real return value)", got.Languages["go"])
	}
}

// TestHandleCodeAnalysis_MissingProjectPath_FailsClosedWithoutCallingAnalyzer
// proves an empty/blank project_path fails closed BEFORE ever reaching the
// analyzer seam (an empty path would otherwise make
// codeanalysis.Analyzer.AnalyzeProject's ValidateProjectPath check fail deep
// inside the call, which is a less actionable error than rejecting it at the
// job boundary).
func TestHandleCodeAnalysis_MissingProjectPath_FailsClosedWithoutCallingAnalyzer(t *testing.T) {
	spy := &spyCodeAnalyzer{retResult: &codeanalysis.AnalysisResult{}}
	r := &Runner{
		codeAnalyzer: spy,
		logger:       zap.NewNop(),
	}

	job := Job{
		ID:      uuid.New(),
		Type:    JobTypeCodeAnalysis,
		Payload: json.RawMessage(`{"project_path":"  "}`),
	}

	result := r.handleCodeAnalysis(context.Background(), job)
	if result.Success {
		t.Fatal("JobResult.Success = true, want false for a blank project_path")
	}
	if atomic.LoadInt32(&spy.called) != 0 {
		t.Fatal("codeAnalyzer.AnalyzeProject must NOT be called for a blank project_path")
	}
}

// TestHandleCodeAnalysis_MissingPayloadField_FailsClosedWithoutCallingAnalyzer
// proves a payload that omits project_path (valid JSON, missing field) fails
// closed before reaching the analyzer.
func TestHandleCodeAnalysis_MissingPayloadField_FailsClosedWithoutCallingAnalyzer(t *testing.T) {
	spy := &spyCodeAnalyzer{retResult: &codeanalysis.AnalysisResult{}}
	r := &Runner{
		codeAnalyzer: spy,
		logger:       zap.NewNop(),
	}

	job := Job{
		ID:      uuid.New(),
		Type:    JobTypeCodeAnalysis,
		Payload: json.RawMessage(`{}`),
	}

	result := r.handleCodeAnalysis(context.Background(), job)
	if result.Success {
		t.Fatal("JobResult.Success = true, want false for a payload with no project_path")
	}
	if !strings.Contains(result.Error, "project_path") {
		t.Errorf("JobResult.Error = %q, want it to mention 'project_path'", result.Error)
	}
	if atomic.LoadInt32(&spy.called) != 0 {
		t.Fatal("codeAnalyzer.AnalyzeProject must NOT be called when project_path is missing")
	}
}

// TestHandleCodeAnalysis_PropagatesAnalyzerError proves an analyzer-level
// error surfaces as JobResult{Success: false}, which flows into the existing
// retry/recordFailure path (executeJobWithRetry) -- never silently swallowed
// into a fake success.
func TestHandleCodeAnalysis_PropagatesAnalyzerError(t *testing.T) {
	spy := &spyCodeAnalyzer{retErr: errCodeAnalysisSentinel}
	r := &Runner{
		codeAnalyzer: spy,
		logger:       zap.NewNop(),
	}

	job := Job{
		ID:      uuid.New(),
		Type:    JobTypeCodeAnalysis,
		Payload: json.RawMessage(`{"project_path":"/test"}`),
	}

	result := r.handleCodeAnalysis(context.Background(), job)
	if result.Success {
		t.Fatal("JobResult.Success = true, want false when the analyzer returns an error")
	}
	if !strings.Contains(result.Error, "sentinel error, analyze was invoked") {
		t.Errorf("JobResult.Error = %q, want it to contain the propagated analyzer error", result.Error)
	}
}
