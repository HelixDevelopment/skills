package worker

// G11 remediation (GAPS_AND_RISKS_REGISTER.md §"G11 -- Worker does no real
// work and can panic the process (unchecked type assertions in a recover-less
// goroutine)"; research/g11_worker_design.md §2.2 "Wrap EVERY worker goroutine
// (and job) in a recover() + restart supervisor").
//
// SCOPE OF THIS FILE (the G03-independent, in-scope half of G11): the panic
// firewall. Every worker loop is a bare `go func` with NO recover()
// (processJobQueue/autoExpandWorker/validationWorker/registryReviewWorker in
// runner.go), and executeJob dispatches handlers with no per-job firewall. In
// Go, an UNRECOVERED PANIC IN ANY GOROUTINE TERMINATES THE WHOLE PROCESS
// (runtime exit(2)); unlike the server's gin.Recovery() (cmd/server/main.go),
// the separate worker binary (cmd/worker/main.go) has no recovery net. A
// single runtime error in any cycle or handler is therefore a process-killing
// outage / systemd crash-loop -- exactly the G11 evidence line
// (GAPS_AND_RISKS_REGISTER.md:158 "worker goroutines have no recover(), so the
// process dies").
//
// (The sibling half of G11 -- the unchecked coverage type assertions the
// design cites at the old runner.go:518-519 -- was already removed by G32:
// runRegistryReview now calls registry.RunReviewOnce and holds zero
// interface{} assertions. The real-cycle wiring (autoexpand/validation) is
// G03's in-flight territory and is intentionally NOT touched here.)
//
// These are unit tests: no real database, no wall-clock scheduling. They
// exercise the REAL production firewall functions (supervise / recoverJob),
// not reimplementations of them (§11.4.199).
//
// RED (pre-fix, this file added but runner.go NOT yet changed): this file does
// not compile -- Runner has no supervise / recoverJob method and no
// restartBackoffBase field, and Metrics has no PanicsRecovered field. A build
// failure is the honest RED signal for "the firewall seam does not exist yet"
// (the same convention this package's registryreview_unit_test.go documents),
// captured in qa-results/g11/01_red_build.log.
//
// GREEN (post-fix): runner.go gains supervise + runGuarded + recoverJob + the
// PanicsRecovered metric; both tests pass with the worker SURVIVING every
// injected panic. Captured in qa-results/g11/02_green_unit.log.
//
// The §1.1 paired mutations (remove the recover() from runGuarded -> T1 dies;
// remove it from recoverJob -> T2 dies) prove each firewall is load-bearing;
// captured in qa-results/g11/04_mutation_*_red.log.

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// TestSupervise_RecoversPanicAndRestarts_WorkerSurvives proves the
// per-goroutine firewall: a worker loop that panics is recovered, counted, and
// restarted, and the restarted loop still honors ctx cancellation. Without the
// recover() this test's first fn invocation would panic-kill the whole test
// process (exit 2), aborting the suite -- which is precisely the G11 defect the
// firewall closes.
func TestSupervise_RecoversPanicAndRestarts_WorkerSurvives(t *testing.T) {
	r := &Runner{
		logger:             zap.NewNop(),
		restartBackoffBase: time.Millisecond, // fast + deterministic restart (§11.4.50)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	restarted := make(chan struct{}, 1)
	var calls int32
	fn := func(fnCtx context.Context) {
		if atomic.AddInt32(&calls, 1) == 1 {
			// First invocation: panic. In a recover-less goroutine (the pre-fix
			// code) this kills the entire process.
			panic("g11-test: simulated worker-goroutine panic")
		}
		// Second invocation == the restart: proves supervise recovered the
		// panic and relaunched the loop. Signal it, then behave like a real
		// loop that returns on ctx cancellation.
		select {
		case restarted <- struct{}{}:
		default:
		}
		<-fnCtx.Done()
	}

	r.wg.Add(1)
	go r.supervise(ctx, "g11_test_worker", fn)

	select {
	case <-restarted:
		// Reaching here proves the process did NOT crash and the loop restarted.
	case <-time.After(5 * time.Second):
		t.Fatal("supervise did not restart the worker within 5s after a panic; " +
			"the recover()+restart firewall (G11 design §2.2) is not working")
	}

	if got := r.GetMetrics().PanicsRecovered; got < 1 {
		t.Fatalf("PanicsRecovered = %d, want >= 1: a recovered panic MUST be counted, "+
			"never silently swallowed (§11.4.201 a recovered panic is a tracked signal)", got)
	}

	// Clean shutdown: the restarted loop must observe ctx.Done and return, so
	// supervise returns and wg.Done fires within the timeout.
	cancel()
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("supervise did not return after ctx cancellation; graceful shutdown is broken")
	}
}

// TestRecoverJob_HandlerPanic_RecordedNotFatal proves the per-job firewall: a
// panicking job handler becomes a RECORDED failed JobResult (which flows into
// the existing retry/recordFailure path in executeJobWithRetry) instead of a
// process death, and a normal handler return is passed through unchanged.
func TestRecoverJob_HandlerPanic_RecordedNotFatal(t *testing.T) {
	r := &Runner{logger: zap.NewNop()}
	job := Job{ID: uuid.New(), Type: JobTypeAutoExpand}

	// Panic path: recorded failure, worker alive, panic counted.
	res := r.recoverJob(job, func() JobResult {
		panic("g11-test: simulated job-handler panic")
	})
	if res.Success {
		t.Fatal("a panicking job handler must yield JobResult{Success:false}, not success")
	}
	if !strings.Contains(res.Error, "handler panic") {
		t.Fatalf("recovered-panic JobResult.Error = %q, want it to mention 'handler panic'", res.Error)
	}
	if got := r.GetMetrics().PanicsRecovered; got < 1 {
		t.Fatalf("PanicsRecovered = %d, want >= 1 after a recovered handler panic", got)
	}

	// Transparent path: a non-panicking handler's result passes through unchanged.
	ok := r.recoverJob(job, func() JobResult {
		return JobResult{Success: true}
	})
	if !ok.Success {
		t.Fatal("recoverJob must pass a non-panicking handler's result through unchanged")
	}
}
