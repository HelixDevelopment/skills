package worker

// G03 remediation (the autoexpand+worker half; the MCP-side create-path
// validation half of §G03 was already wired -- see internal/mcp/tools.go +
// internal/api/skills_handler.go). Confirmed pre-fix FACT (§11.4.6, by
// direct inspection, not guessed): executeJob's JobTypeAutoExpand case
// dispatched to handleAutoExpand, which only unmarshaled the job payload,
// logged, and returned a stub JobResult -- it never referenced
// internal/autoexpand at all. `grep -rn "internal/autoexpand"` across the
// whole repo (excluding comments in unrelated files) turned up ZERO
// production import sites: autoexpand.NewPipeline/NewLLMClientFromConfig/
// WithLLMClient had no caller anywhere. The runAutoExpandCycle ticker cycle
// (autoExpandWorker's `case <-ticker.C:` arm) has the SAME gap and is
// intentionally left untouched here -- the task names "the worker job
// loop" (the job-queue dispatch executeJob/handleAutoExpand drives), not
// the separate periodic ticker cycle; see panicsafety_unit_test.go's own
// header, which independently documents "the real-cycle wiring
// (autoexpand/validation) is G03's in-flight territory".
//
// This unit test proves, at the handler-invoked-function level (no real
// database, no real LLM provider, mirrors registryreview_unit_test.go's
// spy-based convention -- §11.4.27 permits mocks/spies ONLY in unit tests),
// that Runner.handleAutoExpand genuinely dispatches through a
// `autoexpand autoExpander` seam with the args extracted from the job
// payload, and correctly threads the pipeline's *autoexpand.ExpansionResult
// (or error) into the returned JobResult -- instead of the pre-fix stub
// that never referenced the seam at all.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/HelixDevelopment/skills/internal/autoexpand"
	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// spyAutoExpander is a unit-test-only stand-in for the autoExpander seam. It
// never touches a real database or LLM provider. It records the exact
// (skillName, maxDepth) it was invoked with and returns a caller-configured
// canned result/error, so tests can assert both "was it called with the
// right args" and "did the handler thread the result through correctly".
type spyAutoExpander struct {
	called    int32
	gotSkill  string
	gotDepth  int
	retResult *autoexpand.ExpansionResult
	retErr    error
}

func (s *spyAutoExpander) Run(_ context.Context, skillName string, maxDepth int) (*autoexpand.ExpansionResult, error) {
	atomic.AddInt32(&s.called, 1)
	s.gotSkill = skillName
	s.gotDepth = maxDepth
	return s.retResult, s.retErr
}

// TestHandleAutoExpand_InvokesPipelineWithPayloadArgs proves the primary G03
// wiring: given an autoexpand job, the handler calls the pipeline seam with
// the skill_name/max_depth extracted from the job payload (not a stub that
// ignores them), and surfaces the pipeline's result as a successful
// JobResult carrying the marshaled ExpansionResult.
func TestHandleAutoExpand_InvokesPipelineWithPayloadArgs(t *testing.T) {
	jobID := uuid.New()
	spy := &spyAutoExpander{
		retResult: &autoexpand.ExpansionResult{
			JobID:         jobID,
			SkillsCreated: 2,
			SkillsUpdated: 0,
		},
	}

	r := &Runner{
		autoexpand: spy,
		logger:     zap.NewNop(),
		cfg:        config.Config{AutoExpand: config.AutoExpandConfig{MaxDepth: 2}},
	}

	job := Job{
		ID:      uuid.New(),
		Type:    JobTypeAutoExpand,
		Payload: json.RawMessage(`{"skill_name":"go-concurrency","max_depth":3}`),
	}

	result := r.handleAutoExpand(context.Background(), job)

	if got := atomic.LoadInt32(&spy.called); got != 1 {
		t.Fatalf("autoExpander.Run called %d times, want 1: handleAutoExpand must dispatch "+
			"through the autoexpand pipeline seam exactly once per job", got)
	}
	if spy.gotSkill != "go-concurrency" {
		t.Errorf("Run called with skillName = %q, want %q (from the job payload)", spy.gotSkill, "go-concurrency")
	}
	if spy.gotDepth != 3 {
		t.Errorf("Run called with maxDepth = %d, want 3 (from the job payload)", spy.gotDepth)
	}

	if !result.Success {
		t.Fatalf("JobResult.Success = false, want true (error=%q)", result.Error)
	}

	var got autoexpand.ExpansionResult
	if err := json.Unmarshal(result.Data, &got); err != nil {
		t.Fatalf("JobResult.Data is not a valid marshaled ExpansionResult: %v (data=%s)", err, result.Data)
	}
	if got.SkillsCreated != 2 {
		t.Errorf("JobResult.Data.SkillsCreated = %d, want 2 (the pipeline's real return value, "+
			"not a hand-rolled stub payload)", got.SkillsCreated)
	}
	if got.JobID != jobID {
		t.Errorf("JobResult.Data.JobID = %s, want %s (the pipeline's own job id must be preserved)", got.JobID, jobID)
	}
}

// TestHandleAutoExpand_DefaultsMaxDepthFromConfigWhenPayloadOmitsIt proves
// the on-demand-expansion path (a job submitted with no max_depth, e.g. the
// CLI's `expand trigger <name>` without --depth) falls back to the
// configured autoexpand.max_depth rather than silently calling Run with 0
// (which autoexpand.Pipeline.Run would otherwise treat as "expand nothing":
// `for depth := 0; depth < maxDepth ...` never executes when maxDepth <= 0).
func TestHandleAutoExpand_DefaultsMaxDepthFromConfigWhenPayloadOmitsIt(t *testing.T) {
	spy := &spyAutoExpander{retResult: &autoexpand.ExpansionResult{}}
	r := &Runner{
		autoexpand: spy,
		logger:     zap.NewNop(),
		cfg:        config.Config{AutoExpand: config.AutoExpandConfig{MaxDepth: 5}},
	}

	job := Job{
		ID:      uuid.New(),
		Type:    JobTypeAutoExpand,
		Payload: json.RawMessage(`{"skill_name":"kubernetes"}`),
	}

	result := r.handleAutoExpand(context.Background(), job)
	if !result.Success {
		t.Fatalf("JobResult.Success = false, want true (error=%q)", result.Error)
	}
	if spy.gotDepth != 5 {
		t.Errorf("Run called with maxDepth = %d, want 5 (the configured autoexpand.max_depth "+
			"default, since the payload omitted max_depth)", spy.gotDepth)
	}
}

// TestHandleAutoExpand_PropagatesPipelineError proves a pipeline-level error
// (e.g. a DB failure inside Pipeline.Run) surfaces as JobResult{Success:
// false}, which flows into the existing retry/recordFailure path
// (executeJobWithRetry) -- never silently swallowed into a fake success.
func TestHandleAutoExpand_PropagatesPipelineError(t *testing.T) {
	spy := &spyAutoExpander{retErr: errAutoExpandSentinel}
	r := &Runner{
		autoexpand: spy,
		logger:     zap.NewNop(),
		cfg:        config.Config{AutoExpand: config.AutoExpandConfig{MaxDepth: 2}},
	}

	job := Job{
		ID:      uuid.New(),
		Type:    JobTypeAutoExpand,
		Payload: json.RawMessage(`{"skill_name":"rust-ownership","max_depth":1}`),
	}

	result := r.handleAutoExpand(context.Background(), job)
	if result.Success {
		t.Fatal("JobResult.Success = true, want false when the pipeline returns an error")
	}
	if !strings.Contains(result.Error, "sentinel error, autoexpand run was invoked") {
		t.Errorf("JobResult.Error = %q, want it to contain the propagated pipeline error", result.Error)
	}
}

// TestHandleAutoExpand_MissingSkillName_FailsClosedWithoutCallingPipeline
// proves an empty/blank skill_name fails closed BEFORE ever reaching the
// pipeline seam (an empty skillName would otherwise make
// autoexpand.Pipeline.Run's GetTree/GetByName lookup fail deep inside the
// call, which is a worse, less actionable error than rejecting it at the
// job boundary).
func TestHandleAutoExpand_MissingSkillName_FailsClosedWithoutCallingPipeline(t *testing.T) {
	spy := &spyAutoExpander{retResult: &autoexpand.ExpansionResult{}}
	r := &Runner{
		autoexpand: spy,
		logger:     zap.NewNop(),
		cfg:        config.Config{AutoExpand: config.AutoExpandConfig{MaxDepth: 2}},
	}

	job := Job{
		ID:      uuid.New(),
		Type:    JobTypeAutoExpand,
		Payload: json.RawMessage(`{"skill_name":"  ","max_depth":1}`),
	}

	result := r.handleAutoExpand(context.Background(), job)
	if result.Success {
		t.Fatal("JobResult.Success = true, want false for a blank skill_name")
	}
	if atomic.LoadInt32(&spy.called) != 0 {
		t.Fatal("autoExpander.Run must NOT be called for a blank skill_name")
	}
}

// errAutoExpandSentinel mirrors registryreview_unit_test.go's
// errSpyReviewSentinel convention: a deliberate sentinel error so the
// handler's error-propagation path is exercised for real.
var errAutoExpandSentinel = errors.New("spyAutoExpander: sentinel error, autoexpand run was invoked")
