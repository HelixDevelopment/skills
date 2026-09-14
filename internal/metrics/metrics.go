// Package metrics provides Prometheus instrumentation for the HelixKnowledge
// skill graph system. All metrics are registered with a custom registry so
// they can be served via promhttp.HandlerFor without colliding with Go
// runtime defaults.
//
// Usage:
//
//	reg := metrics.NewRegistry()
//	// Register HTTP middleware.
//	router.Use(metrics.HTTPMiddleware(reg))
//	// Serve /metrics.
//	http.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ---------------------------------------------------------------------------
// Metric names and help text
// ---------------------------------------------------------------------------

const (
	Namespace = "skill"

	// API metrics.
	apiRequestsTotal  = "api_requests_total"
	apiLatencySeconds = "api_latency_seconds"
	searchLatency     = "search_latency_seconds"

	// Worker metrics.
	workerJobsTotal = "worker_jobs_total"

	// Embedding metrics.
	embeddingLatency = "embedding_latency_seconds"

	// Database metrics.
	dbConnectionsActive = "db_connections_active"

	// Cache metrics.
	cacheHitsTotal = "cache_hits_total"

	// Tenant metrics.
	tenantRequestsTotal          = "tenant_requests_total"
	tenantRateLimitRejectedTotal = "tenant_rate_limit_rejected_total"
	tenantAuditEntriesTotal      = "tenant_audit_entries_total"
)

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

// Metrics holds all Prometheus metric collectors for the skill system.
type Metrics struct {
	// API
	APIRequestsTotal  *prometheus.CounterVec
	APILatencySeconds *prometheus.HistogramVec
	SearchLatency     prometheus.Histogram

	// Worker
	WorkerJobsTotal *prometheus.CounterVec

	// Embedding
	EmbeddingLatency prometheus.Histogram

	// Database
	DBConnectionsActive *prometheus.GaugeVec

	// Cache
	CacheHitsTotal *prometheus.CounterVec

	// Tenant
	TenantRequestsTotal          *prometheus.CounterVec
	TenantRateLimitRejectedTotal *prometheus.CounterVec
	TenantAuditEntriesTotal      *prometheus.CounterVec

	// Registry holds the custom prometheus.Registry that all metrics are
	// registered with. Pass this to promhttp.HandlerFor to serve /metrics.
	Registry *prometheus.Registry

	// enabled tracks whether metrics collection is active. When false,
	// Observe/Inc/Dec calls are no-ops (the metric collectors still exist
	// but are never updated).
	enabled bool
}

// NewRegistry creates and registers all Prometheus metrics with a fresh
// registry. If enabled is false, a Metrics struct is still returned (so
// callers don't need nil-checks) but the registry is not served and
// observation methods become no-ops.
func NewRegistry(enabled bool) *Metrics {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		APIRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: Namespace,
				Name:      apiRequestsTotal,
				Help:      "Total number of API requests by endpoint, method, and status.",
			},
			[]string{"endpoint", "method", "status"},
		),
		APILatencySeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: Namespace,
				Name:      apiLatencySeconds,
				Help:      "API request latency in seconds by endpoint.",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"endpoint"},
		),
		SearchLatency: prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Namespace: Namespace,
				Name:      searchLatency,
				Help:      "Search query latency in seconds.",
				Buckets:   []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
			},
		),
		WorkerJobsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: Namespace,
				Name:      workerJobsTotal,
				Help:      "Total worker jobs processed by type and status.",
			},
			[]string{"type", "status"},
		),
		EmbeddingLatency: prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Namespace: Namespace,
				Name:      embeddingLatency,
				Help:      "Embedding generation latency in seconds.",
				Buckets:   []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
			},
		),
		DBConnectionsActive: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: Namespace,
				Name:      dbConnectionsActive,
				Help:      "Number of active database connections by pool type.",
			},
			[]string{"pool"},
		),
		CacheHitsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: Namespace,
				Name:      cacheHitsTotal,
				Help:      "Total cache operations by result (hit or miss).",
			},
			[]string{"result"},
		),
		TenantRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: Namespace,
				Name:      tenantRequestsTotal,
				Help:      "Total tenant-scoped API requests by tenant, method, path, and status.",
			},
			[]string{"tenant_id", "method", "path", "status"},
		),
		TenantRateLimitRejectedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: Namespace,
				Name:      tenantRateLimitRejectedTotal,
				Help:      "Total requests rejected by per-tenant rate limiting.",
			},
			[]string{"tenant_id"},
		),
		TenantAuditEntriesTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: Namespace,
				Name:      tenantAuditEntriesTotal,
				Help:      "Total audit log entries recorded per tenant and action.",
			},
			[]string{"tenant_id", "action"},
		),
		Registry: reg,
		enabled:  enabled,
	}

	if enabled {
		reg.MustRegister(
			m.APIRequestsTotal,
			m.APILatencySeconds,
			m.SearchLatency,
			m.WorkerJobsTotal,
			m.EmbeddingLatency,
			m.DBConnectionsActive,
			m.CacheHitsTotal,
			m.TenantRequestsTotal,
			m.TenantRateLimitRejectedTotal,
			m.TenantAuditEntriesTotal,
		)
	}

	return m
}

// Enabled reports whether metrics collection is active.
func (m *Metrics) Enabled() bool {
	return m != nil && m.enabled
}

// ---------------------------------------------------------------------------
// Convenience methods (no-ops when disabled)
// ---------------------------------------------------------------------------

// ObserveAPIRequest records an API request.
func (m *Metrics) ObserveAPIRequest(endpoint, method, status string, duration time.Duration) {
	if !m.Enabled() {
		return
	}
	m.APIRequestsTotal.WithLabelValues(endpoint, method, status).Inc()
	m.APILatencySeconds.WithLabelValues(endpoint).Observe(duration.Seconds())
}

// ObserveSearch records a search query latency.
func (m *Metrics) ObserveSearch(duration time.Duration) {
	if !m.Enabled() {
		return
	}
	m.SearchLatency.Observe(duration.Seconds())
}

// ObserveWorkerJob records a completed worker job.
func (m *Metrics) ObserveWorkerJob(jobType, status string) {
	if !m.Enabled() {
		return
	}
	m.WorkerJobsTotal.WithLabelValues(jobType, status).Inc()
}

// ObserveEmbedding records an embedding generation latency.
func (m *Metrics) ObserveEmbedding(duration time.Duration) {
	if !m.Enabled() {
		return
	}
	m.EmbeddingLatency.Observe(duration.Seconds())
}

// SetDBConnections sets the active connection count for a pool type.
func (m *Metrics) SetDBConnections(pool string, count int) {
	if !m.Enabled() {
		return
	}
	m.DBConnectionsActive.WithLabelValues(pool).Set(float64(count))
}

// RecordCacheHit increments the cache hit counter.
func (m *Metrics) RecordCacheHit() {
	if !m.Enabled() {
		return
	}
	m.CacheHitsTotal.WithLabelValues("hit").Inc()
}

// RecordCacheMiss increments the cache miss counter.
func (m *Metrics) RecordCacheMiss() {
	if !m.Enabled() {
		return
	}
	m.CacheHitsTotal.WithLabelValues("miss").Inc()
}

// ---------------------------------------------------------------------------
// Tenant convenience methods (no-ops when disabled)
// ---------------------------------------------------------------------------

// RecordTenantRequest increments the per-tenant request counter.
func (m *Metrics) RecordTenantRequest(tenantID, method, path, status string) {
	if !m.Enabled() {
		return
	}
	m.TenantRequestsTotal.WithLabelValues(tenantID, method, path, status).Inc()
}

// RecordTenantRateLimitRejection increments the per-tenant rate limit
// rejection counter.
func (m *Metrics) RecordTenantRateLimitRejection(tenantID string) {
	if !m.Enabled() {
		return
	}
	m.TenantRateLimitRejectedTotal.WithLabelValues(tenantID).Inc()
}

// RecordTenantAuditEntry increments the per-tenant audit entry counter.
func (m *Metrics) RecordTenantAuditEntry(tenantID, action string) {
	if !m.Enabled() {
		return
	}
	m.TenantAuditEntriesTotal.WithLabelValues(tenantID, action).Inc()
}

// ---------------------------------------------------------------------------
// TenantMetrics — focused tenant-scoped metrics helper
// ---------------------------------------------------------------------------

// TenantMetrics provides a tenant-scoped view of the metrics system. It
// wraps the three tenant counters and a parent enabled-flag so that
// callers (middleware, hooks) can record tenant events without touching
// the full Metrics struct.
type TenantMetrics struct {
	requestsTotal          *prometheus.CounterVec
	rateLimitRejectedTotal *prometheus.CounterVec
	auditEntriesTotal      *prometheus.CounterVec
	enabled                bool
}

// NewTenantMetrics returns a TenantMetrics backed by the counters in the
// parent Metrics. Returns nil when m is nil (callers must nil-check).
func NewTenantMetrics(m *Metrics) *TenantMetrics {
	if m == nil {
		return nil
	}
	return &TenantMetrics{
		requestsTotal:          m.TenantRequestsTotal,
		rateLimitRejectedTotal: m.TenantRateLimitRejectedTotal,
		auditEntriesTotal:      m.TenantAuditEntriesTotal,
		enabled:                m.enabled,
	}
}

// Enabled reports whether metrics collection is active.
func (tm *TenantMetrics) Enabled() bool {
	return tm != nil && tm.enabled
}

// RecordRequest increments the per-tenant request counter.
func (tm *TenantMetrics) RecordRequest(tenantID, method, path, status string) {
	if !tm.Enabled() {
		return
	}
	tm.requestsTotal.WithLabelValues(tenantID, method, path, status).Inc()
}

// RecordRateLimitRejection increments the per-tenant rate limit rejection
// counter.
func (tm *TenantMetrics) RecordRateLimitRejection(tenantID string) {
	if !tm.Enabled() {
		return
	}
	tm.rateLimitRejectedTotal.WithLabelValues(tenantID).Inc()
}

// RecordAuditEntry increments the per-tenant audit entry counter.
func (tm *TenantMetrics) RecordAuditEntry(tenantID, action string) {
	if !tm.Enabled() {
		return
	}
	tm.auditEntriesTotal.WithLabelValues(tenantID, action).Inc()
}

// ---------------------------------------------------------------------------
// HTTP middleware
// ---------------------------------------------------------------------------

// HTTPMiddleware returns an http.Handler middleware that instruments every
// request with the API request counter and latency histogram.
func HTTPMiddleware(m *Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !m.Enabled() {
				next.ServeHTTP(w, r)
				return
			}

			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)

			duration := time.Since(start)
			endpoint := canonicalEndpoint(r.URL.Path)
			status := http.StatusText(sw.status)

			m.ObserveAPIRequest(endpoint, r.Method, status, duration)
		})
	}
}

// statusWriter wraps http.ResponseWriter to capture the status code.
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(b)
}

// canonicalEndpoint normalises URL paths to reduce cardinality. Dynamic
// path segments (UUIDs, names) are replaced with placeholders.
func canonicalEndpoint(path string) string {
	// Keep cardinality low: /api/v1/skills/:name -> /api/v1/skills/:name
	// For now, use the path as-is. Callers can override with route patterns.
	if path == "" {
		return "/"
	}
	return path
}

// Handler returns an http.Handler that serves Prometheus metrics.
// Returns nil if metrics are not enabled.
func (m *Metrics) Handler() http.Handler {
	if !m.Enabled() {
		return nil
	}
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})
}
