// Package worker -- source_rescan_unit_test.go proves, at the
// handler-invoked-function level (no real database, no real network,
// mirrors autoexpand_unit_test.go's spy-based convention -- section 11.4.27
// permits mocks/spies ONLY in unit tests), that Runner.handleSourceRescan
// genuinely dispatches through a `sourceSyncer sourceSyncer` seam with the
// source_id extracted from the job payload, and correctly threads the
// orchestrator's *skillsource.SyncResult (or error) into the returned
// JobResult (G83).
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/HelixDevelopment/skills/internal/skillsource"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// spySourceSyncer is a unit-test-only stand-in for the sourceSyncer seam. It
// never touches a real database or network. It records the exact sourceID it
// was invoked with and returns a caller-configured canned result/error, so
// tests can assert both "was it called with the right args" and "did the
// handler thread the result through correctly".
type spySourceSyncer struct {
	called    int32
	gotID     uuid.UUID
	retResult *skillsource.SyncResult
	retErr    error
}

func (s *spySourceSyncer) SyncSource(_ context.Context, sourceID uuid.UUID) (*skillsource.SyncResult, error) {
	atomic.AddInt32(&s.called, 1)
	s.gotID = sourceID
	return s.retResult, s.retErr
}

// errSourceSyncSentinel is a deliberate sentinel error so the handler's
// error-propagation path is exercised for real.
var errSourceSyncSentinel = errors.New("spySourceSyncer: sentinel error, sync was invoked")

// TestHandleSourceRescan_InvokesOrchestratorWithPayloadSourceID proves the
// primary G83 wiring: given a source_rescan job, the handler calls the
// orchestrator seam with the source_id UUID extracted from the job payload,
// and surfaces the orchestrator's SyncResult as a successful JobResult
// carrying the marshaled result.
func TestHandleSourceRescan_InvokesOrchestratorWithPayloadSourceID(t *testing.T) {
	sourceID := uuid.New()
	spy := &spySourceSyncer{
		retResult: &skillsource.SyncResult{
			SourceID: sourceID,
			Fetched:  5,
			Parsed:   4,
			Imported: 3,
		},
	}

	r := &Runner{
		sourceSyncer: spy,
		logger:       zap.NewNop(),
	}

	job := Job{
		ID:      uuid.New(),
		Type:    JobTypeSourceRescan,
		Payload: json.RawMessage(`{"source_id":"` + sourceID.String() + `"}`),
	}

	result := r.handleSourceRescan(context.Background(), job)

	if got := atomic.LoadInt32(&spy.called); got != 1 {
		t.Fatalf("sourceSyncer.SyncSource called %d times, want 1: handleSourceRescan must dispatch "+
			"through the source sync seam exactly once per job", got)
	}
	if spy.gotID != sourceID {
		t.Errorf("SyncSource called with sourceID = %s, want %s (from the job payload)", spy.gotID, sourceID)
	}

	if !result.Success {
		t.Fatalf("JobResult.Success = false, want true (error=%q)", result.Error)
	}

	var got skillsource.SyncResult
	if err := json.Unmarshal(result.Data, &got); err != nil {
		t.Fatalf("JobResult.Data is not a valid marshaled SyncResult: %v (data=%s)", err, result.Data)
	}
	if got.Fetched != 5 {
		t.Errorf("JobResult.Data.Fetched = %d, want 5 (the orchestrator's real return value)", got.Fetched)
	}
	if got.Imported != 3 {
		t.Errorf("JobResult.Data.Imported = %d, want 3", got.Imported)
	}
	if got.SourceID != sourceID {
		t.Errorf("JobResult.Data.SourceID = %s, want %s", got.SourceID, sourceID)
	}
}

// TestHandleSourceRescan_PropagatesOrchestratorError proves an
// orchestrator-level error (e.g. a DB failure inside SyncSource) surfaces as
// JobResult{Success: false}, which flows into the existing retry/
// recordFailure path (executeJobWithRetry) -- never silently swallowed into a
// fake success.
func TestHandleSourceRescan_PropagatesOrchestratorError(t *testing.T) {
	spy := &spySourceSyncer{retErr: errSourceSyncSentinel}
	r := &Runner{
		sourceSyncer: spy,
		logger:       zap.NewNop(),
	}

	job := Job{
		ID:      uuid.New(),
		Type:    JobTypeSourceRescan,
		Payload: json.RawMessage(`{"source_id":"` + uuid.New().String() + `"}`),
	}

	result := r.handleSourceRescan(context.Background(), job)
	if result.Success {
		t.Fatal("JobResult.Success = true, want false when the orchestrator returns an error")
	}
	if !strings.Contains(result.Error, "sentinel error, sync was invoked") {
		t.Errorf("JobResult.Error = %q, want it to contain the propagated orchestrator error", result.Error)
	}
}

// TestHandleSourceRescan_MissingSourceID_FailsClosedWithoutCallingOrchestrator
// proves an empty/blank source_id fails closed BEFORE ever reaching the
// orchestrator seam.
func TestHandleSourceRescan_MissingSourceID_FailsClosedWithoutCallingOrchestrator(t *testing.T) {
	spy := &spySourceSyncer{retResult: &skillsource.SyncResult{}}
	r := &Runner{
		sourceSyncer: spy,
		logger:       zap.NewNop(),
	}

	job := Job{
		ID:      uuid.New(),
		Type:    JobTypeSourceRescan,
		Payload: json.RawMessage(`{"source_id":"  "}`),
	}

	result := r.handleSourceRescan(context.Background(), job)
	if result.Success {
		t.Fatal("JobResult.Success = true, want false for a blank source_id")
	}
	if atomic.LoadInt32(&spy.called) != 0 {
		t.Fatal("sourceSyncer.SyncSource must NOT be called for a blank source_id")
	}
}

// TestHandleSourceRescan_InvalidUUID_FailsClosed proves a malformed UUID in
// source_id fails closed with a clear error before reaching the orchestrator.
func TestHandleSourceRescan_InvalidUUID_FailsClosed(t *testing.T) {
	spy := &spySourceSyncer{retResult: &skillsource.SyncResult{}}
	r := &Runner{
		sourceSyncer: spy,
		logger:       zap.NewNop(),
	}

	job := Job{
		ID:      uuid.New(),
		Type:    JobTypeSourceRescan,
		Payload: json.RawMessage(`{"source_id":"not-a-uuid"}`),
	}

	result := r.handleSourceRescan(context.Background(), job)
	if result.Success {
		t.Fatal("JobResult.Success = true, want false for an invalid UUID")
	}
	if !strings.Contains(result.Error, "invalid source_id") {
		t.Errorf("JobResult.Error = %q, want it to mention invalid source_id", result.Error)
	}
	if atomic.LoadInt32(&spy.called) != 0 {
		t.Fatal("sourceSyncer.SyncSource must NOT be called for an invalid UUID")
	}
}

// TestHandleSourceRescan_MissingPayload_FailsClosed proves a completely
// empty/missing source_id field in the payload fails closed.
func TestHandleSourceRescan_MissingPayload_FailsClosed(t *testing.T) {
	spy := &spySourceSyncer{retResult: &skillsource.SyncResult{}}
	r := &Runner{
		sourceSyncer: spy,
		logger:       zap.NewNop(),
	}

	job := Job{
		ID:      uuid.New(),
		Type:    JobTypeSourceRescan,
		Payload: json.RawMessage(`{}`),
	}

	result := r.handleSourceRescan(context.Background(), job)
	if result.Success {
		t.Fatal("JobResult.Success = true, want false for a missing source_id")
	}
	if atomic.LoadInt32(&spy.called) != 0 {
		t.Fatal("sourceSyncer.SyncSource must NOT be called for a missing source_id")
	}
}
