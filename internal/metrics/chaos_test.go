// Package metrics provides Prometheus instrumentation for the HelixKnowledge
// skill graph system. This file contains chaos/resilience tests for metric
// overflow, disabled state, and nil-safety.
package metrics

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestChaos_MetricOverflow verifies that recording a very large number of
// distinct label combinations does not panic or cause unbounded memory
// growth. Prometheus counters with high-cardinality labels are a known
// anti-pattern; this test exercises the boundary.
func TestChaos_MetricOverflow(t *testing.T) {
	m := NewRegistry(true)

	// Record 500 distinct endpoint label combinations.
	for i := 0; i < 500; i++ {
		endpoint := fmt.Sprintf("/api/v1/skills/%d", i)
		m.ObserveAPIRequest(endpoint, "GET", "200", 10*time.Millisecond)
	}

	// Verify the registry is still functional after high-cardinality writes.
	m.ObserveAPIRequest("/api/v1/skills/test", "POST", "201", 5*time.Millisecond)
	m.ObserveSearch(25 * time.Millisecond)
	m.RecordCacheHit()

	// The handler should still serve metrics without panic.
	handler := m.Handler()
	if handler == nil {
		t.Fatal("Handler returned nil after overflow")
	}

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Metrics handler returned %d after overflow, want 200", w.Code)
	}
}

// TestChaos_DisabledMetrics verifies that all metric operations are safe
// no-ops when the registry is created with enabled=false.
func TestChaos_DisabledMetrics(t *testing.T) {
	m := NewRegistry(false)

	if m.Enabled() {
		t.Fatal("Disabled registry reports Enabled()=true")
	}

	// All observation methods must not panic.
	m.ObserveAPIRequest("/test", "GET", "200", time.Millisecond)
	m.ObserveSearch(time.Millisecond)
	m.ObserveWorkerJob("test", "completed")
	m.ObserveEmbedding(time.Millisecond)
	m.SetDBConnections("primary", 5)
	m.RecordCacheHit()
	m.RecordCacheMiss()

	// Handler should return nil for disabled metrics.
	if h := m.Handler(); h != nil {
		t.Error("Handler should return nil for disabled metrics")
	}
}

// TestChaos_NilMetrics verifies that calling Enabled() on a nil Metrics
// pointer does not panic.
func TestChaos_NilMetrics(t *testing.T) {
	var m *Metrics
	if m.Enabled() {
		t.Error("nil Metrics should report Enabled()=false")
	}
}

// TestChaos_MiddlewareDisabledMetrics verifies that the HTTP middleware
// passes through requests without recording when metrics are disabled.
func TestChaos_MiddlewareDisabledMetrics(t *testing.T) {
	m := NewRegistry(false)
	middleware := HTTPMiddleware(m)

	called := false
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Error("Handler was not called through disabled middleware")
	}
	if w.Code != http.StatusOK {
		t.Errorf("Status %d, want 200", w.Code)
	}
}
