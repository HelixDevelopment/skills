package worker

// G32 remediation (research/p05_high_defect_fix_designs.md §4): the
// registry-review ticker (registryReviewWorker -> runRegistryReview,
// runner.go) previously called ONLY r.store.GetCoverage(ctx, "") on every
// tick -- a read-only report that never marks stale skills, never
// recalculates coverage, and never recalculates missing dependencies.
// The full review logic (registry.Registry.RunReviewOnce) existed but had
// ZERO callers anywhere in the codebase (a never-completed wiring,
// §11.4.124).
//
// This unit test proves, at the tick-invoked-function level (no real
// database, no real time.Ticker -- the ticker's own hardcoded >=1-minute
// interval floor at runner.go:439-442 makes waiting on a real tick
// impractical for a unit test; the fake-clock/spy substitute this task
// explicitly permits is applied to the FUNCTION the ticker's `case
// <-ticker.C:` arm invokes, not to wall-clock scheduling itself), that
// Runner.runRegistryReview genuinely invokes the encapsulated review logic
// through a `registry` field on every call.
//
// RED (pre-fix, this test file added but runner.go NOT yet changed): this
// file does not compile -- Runner has no `registry` field and no
// `registryReviewer` interface exists, because runRegistryReview's pre-fix
// body called only r.store.GetCoverage and never referenced any review
// abstraction at all. A build failure is the correct, honest RED signal
// for "the seam this test exercises does not exist yet" (captured in
// qa-results/g32_reviewscheduler/01_red_unit_build.log).
//
// GREEN (post-fix): runner.go gains a `registry registryReviewer` field
// (satisfied in production by *registry.Registry via registry.NewRegistry,
// wired inside NewRunner) and runRegistryReview's body is retargeted to
// call r.registry.RunReviewOnce(ctx) first, before anything else. This
// test substitutes a spy for that field and asserts the spy was invoked.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"
)

// spyRegistryReviewer is a unit-test-only stand-in for the registryReviewer
// seam. It never touches a real database. It deliberately returns a
// sentinel error so that, post-fix, Runner.runRegistryReview's error path
// (log + return) is exercised and the function never reaches its
// success-path audit-log write (which needs a real *db.Pool) -- keeping
// this test a genuine, DB-free unit test per §11.4.27 (mocks/spies are
// permitted ONLY in unit tests; this is one).
type spyRegistryReviewer struct {
	called int32
}

var errSpyReviewSentinel = errors.New("spyRegistryReviewer: sentinel error, review logic was invoked")

func (s *spyRegistryReviewer) RunReviewOnce(ctx context.Context) error {
	atomic.AddInt32(&s.called, 1)
	return errSpyReviewSentinel
}

// TestRunRegistryReview_InvokesReviewLogicOnEveryTick proves the tick path
// (the function registryReviewWorker's `case <-ticker.C:` arm calls) really
// calls into the encapsulated review logic instead of the pre-fix
// read-only r.store.GetCoverage report.
func TestRunRegistryReview_InvokesReviewLogicOnEveryTick(t *testing.T) {
	spy := &spyRegistryReviewer{}

	r := &Runner{
		registry: spy,
		logger:   zap.NewNop(),
	}

	ctx := context.Background()

	// Simulate three ticks, exactly as registryReviewWorker's `case
	// <-ticker.C: r.runRegistryReview(ctx)` would invoke it -- three times
	// in a row, deterministically (§11.4.50), with no real clock involved.
	for i := 0; i < 3; i++ {
		r.runRegistryReview(ctx)
	}

	got := atomic.LoadInt32(&spy.called)
	if got != 3 {
		t.Fatalf("registry review logic invoked %d times, want 3 (one per simulated tick); "+
			"the registry-review ticker cycle must call the encapsulated review logic "+
			"(RunReviewOnce) on every tick, not a read-only report", got)
	}
}
