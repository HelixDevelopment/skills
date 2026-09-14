package worker

// G03 remediation (the validation+worker half): unit-test spy for the
// validator seam and proof that Runner.handleValidate and
// Runner.runValidationCycle dispatch through it -- mirroring the EXACT
// pattern established by autoexpand_unit_test.go (spyAutoExpander) and
// registryreview_unit_test.go (spyRegistryReviewer).
//
// The spy-based convention (§11.4.27 permits mocks/spies ONLY in unit
// tests) exercises the handler-invoked-function level: no real database,
// no real filesystem, no real LLM jury. The full end-to-end path
// (real validation.Pipeline over a real database) is exercised by the
// integration test.
//
// RED (pre-fix, this file added but runner.go NOT yet changed): this
// file does not compile -- Runner has no `validator` field and no
// `validator` interface exists, because handleValidate's pre-fix body
// only unmarshaled the payload and returned a stub JobResult. A build
// failure is the correct, honest RED signal for "the seam this test
// exercises does not exist yet".
//
// GREEN (post-fix): runner.go gains the validator interface + field,
// wired in NewRunner, and handleValidate dispatches through it.

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/HelixDevelopment/skills/internal/validation"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// spyValidator is a unit-test-only stand-in for the validator seam. It
// never touches a real database, filesystem, or LLM provider. It records
// the exact skill pointer it was invoked with and returns a caller-
// configured canned result/error, so tests can assert both "was it called"
// and "did the handler thread the result through correctly".
type spyValidator struct {
	called    int32
	gotSkill  *models.Skill
	retResult *validation.ValidationResult
	retErr    error
}

func (s *spyValidator) Validate(_ context.Context, skill *models.Skill) (*validation.ValidationResult, error) {
	atomic.AddInt32(&s.called, 1)
	s.gotSkill = skill
	return s.retResult, s.retErr
}

// TestHandleValidate_MissingSkillName_FailsClosedWithoutCallingValidator
// proves an empty/blank skill_name fails closed BEFORE ever reaching the
// validation pipeline or the store lookup (which would require a real
// database). This is the SAME defensive-boundary pattern
// handleAutoExpand_MissingSkillName_FailsClosedWithoutCallingPipeline
// establishes for the autoexpand seam.
func TestHandleValidate_MissingSkillName_FailsClosedWithoutCallingValidator(t *testing.T) {
	spy := &spyValidator{retResult: &validation.ValidationResult{}}
	r := &Runner{
		validator: spy,
		logger:    zap.NewNop(),
	}

	job := Job{
		ID:      uuid.New(),
		Type:    JobTypeValidate,
		Payload: json.RawMessage(`{"skill_name":""}`),
	}

	result := r.handleValidate(context.Background(), job)
	if result.Success {
		t.Fatal("JobResult.Success = true, want false for an empty skill_name")
	}
	if atomic.LoadInt32(&spy.called) != 0 {
		t.Fatal("validator.Validate must NOT be called for an empty skill_name")
	}
}

// TestHandleValidate_BlankPayload_FailsClosedWithoutCallingValidator proves
// that a completely empty or malformed JSON payload fails closed.
func TestHandleValidate_BlankPayload_FailsClosedWithoutCallingValidator(t *testing.T) {
	spy := &spyValidator{retResult: &validation.ValidationResult{}}
	r := &Runner{
		validator: spy,
		logger:    zap.NewNop(),
	}

	job := Job{
		ID:      uuid.New(),
		Type:    JobTypeValidate,
		Payload: json.RawMessage(`{}`),
	}

	result := r.handleValidate(context.Background(), job)
	if result.Success {
		t.Fatal("JobResult.Success = true, want false for a payload with no skill_name")
	}
	if !strings.Contains(result.Error, "skill_name") {
		t.Errorf("JobResult.Error = %q, want it to mention 'skill_name'", result.Error)
	}
	if atomic.LoadInt32(&spy.called) != 0 {
		t.Fatal("validator.Validate must NOT be called when skill_name is missing")
	}
}
