package metrics

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Registry creation tests
// ---------------------------------------------------------------------------

func TestNewRegistry_Enabled(t *testing.T) {
	m := NewRegistry(true)

	if m == nil {
		t.Fatal("NewRegistry returned nil")
	}
	if !m.Enabled() {
		t.Error("expected Enabled()=true for enabled registry")
	}
	if m.Registry == nil {
		t.Fatal("expected non-nil Registry")
	}
	if m.Handler() == nil {
		t.Fatal("expected non-nil Handler for enabled registry")
	}
}

func TestNewRegistry_Disabled(t *testing.T) {
	m := NewRegistry(false)

	if m == nil {
		t.Fatal("NewRegistry returned nil")
	}
	if m.Enabled() {
		t.Error("expected Enabled()=false for disabled registry")
	}
	if m.Handler() != nil {
		t.Fatal("expected nil Handler for disabled registry")
	}
}

// ---------------------------------------------------------------------------
// Metric collectors exist
// ---------------------------------------------------------------------------

func TestNewRegistry_AllCollectorsExist(t *testing.T) {
	m := NewRegistry(true)

	if m.APIRequestsTotal == nil {
		t.Error("APIRequestsTotal is nil")
	}
	if m.APILatencySeconds == nil {
		t.Error("APILatencySeconds is nil")
	}
	if m.SearchLatency == nil {
		t.Error("SearchLatency is nil")
	}
	if m.WorkerJobsTotal == nil {
		t.Error("WorkerJobsTotal is nil")
	}
	if m.EmbeddingLatency == nil {
		t.Error("EmbeddingLatency is nil")
	}
	if m.DBConnectionsActive == nil {
		t.Error("DBConnectionsActive is nil")
	}
	if m.CacheHitsTotal == nil {
		t.Error("CacheHitsTotal is nil")
	}
}

// ---------------------------------------------------------------------------
// Convenience method no-ops (disabled)
// ---------------------------------------------------------------------------

func TestDisabledMetrics_NoPanic(t *testing.T) {
	m := NewRegistry(false)

	// All convenience methods should be no-ops when disabled.
	m.ObserveAPIRequest("/api/v1/skills", "GET", "200 OK", 100*time.Millisecond)
	m.ObserveSearch(50 * time.Millisecond)
	m.ObserveWorkerJob("expand", "completed")
	m.ObserveEmbedding(200 * time.Millisecond)
	m.SetDBConnections("read", 5)
	m.RecordCacheHit()
	m.RecordCacheMiss()
}

// ---------------------------------------------------------------------------
// Convenience methods (enabled)
// ---------------------------------------------------------------------------

func TestEnabledMetrics_ObserveAPIRequest(t *testing.T) {
	m := NewRegistry(true)

	// Observe an API request.
	m.ObserveAPIRequest("/api/v1/skills", "GET", "200 OK", 100*time.Millisecond)

	// Verify the counter was incremented.
	families, err := m.Registry.Gather()
	if err != nil {
		t.Fatalf("Gather() failed: %v", err)
	}

	var foundCounter, foundHistogram bool
	for _, fam := range families {
		switch fam.GetName() {
		case Namespace + "_" + apiRequestsTotal:
			foundCounter = true
			if len(fam.GetMetric()) == 0 {
				t.Error("expected at least one metric sample for api_requests_total")
			}
		case Namespace + "_" + apiLatencySeconds:
			foundHistogram = true
			if len(fam.GetMetric()) == 0 {
				t.Error("expected at least one metric sample for api_latency_seconds")
			}
		}
	}
	if !foundCounter {
		t.Error("api_requests_total metric not found in registry")
	}
	if !foundHistogram {
		t.Error("api_latency_seconds metric not found in registry")
	}
}

func TestEnabledMetrics_CacheHitMiss(t *testing.T) {
	m := NewRegistry(true)

	m.RecordCacheHit()
	m.RecordCacheHit()
	m.RecordCacheMiss()

	families, err := m.Registry.Gather()
	if err != nil {
		t.Fatalf("Gather() failed: %v", err)
	}

	for _, fam := range families {
		if fam.GetName() == Namespace+"_"+cacheHitsTotal {
			for _, metric := range fam.GetMetric() {
				labels := metric.GetLabel()
				counter := metric.GetCounter()
				for _, l := range labels {
					if l.GetValue() == "hit" && counter.GetValue() != 2 {
						t.Errorf("cache hit count = %g, want 2", counter.GetValue())
					}
					if l.GetValue() == "miss" && counter.GetValue() != 1 {
						t.Errorf("cache miss count = %g, want 1", counter.GetValue())
					}
				}
			}
		}
	}
}

func TestEnabledMetrics_WorkerJob(t *testing.T) {
	m := NewRegistry(true)

	m.ObserveWorkerJob("expand", "completed")
	m.ObserveWorkerJob("expand", "failed")

	families, err := m.Registry.Gather()
	if err != nil {
		t.Fatalf("Gather() failed: %v", err)
	}

	for _, fam := range families {
		if fam.GetName() == Namespace+"_"+workerJobsTotal {
			if len(fam.GetMetric()) < 2 {
				t.Errorf("expected at least 2 metric samples for worker_jobs_total, got %d",
					len(fam.GetMetric()))
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Nil safety
// ---------------------------------------------------------------------------

func TestNilMetrics_NoPanic(t *testing.T) {
	// Calling methods on a nil Metrics should not panic.
	var m *Metrics
	m.ObserveAPIRequest("/test", "GET", "200", time.Millisecond)
	m.ObserveSearch(time.Millisecond)
	m.ObserveWorkerJob("test", "ok")
	m.ObserveEmbedding(time.Millisecond)
	m.SetDBConnections("read", 0)
	m.RecordCacheHit()
	m.RecordCacheMiss()
}
