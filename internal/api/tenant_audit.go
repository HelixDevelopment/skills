// Tenant audit logging middleware for the HelixKnowledge skill graph system.
//
// Records every tenant-scoped API request into the tenant_audit_log table,
// providing a durable, queryable trail of who did what and when. Health and
// readiness probes are excluded to avoid noise.
//
// Audit writes are asynchronous: the middleware enqueues entries into a
// buffered channel and a dedicated goroutine batches them to the database,
// ensuring the request path is never blocked by I/O. When the channel is full
// (back-pressure), entries are dropped with a warning log rather than causing
// handler latency.
//
// §11.4.84 Tenant audit logging.
package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/metrics"
)

// ---------------------------------------------------------------------------
// AuditEntry
// ---------------------------------------------------------------------------

// AuditEntry represents a single auditable request event.
type AuditEntry struct {
	// TenantID is the UUID of the tenant that made the request.
	TenantID uuid.UUID
	// Action is a high-level verb inferred from the HTTP method
	// (CREATE, READ, UPDATE, DELETE, LIST, UNKNOWN).
	Action string
	// Resource is the primary resource path segment (e.g. "skills").
	Resource string
	// Method is the raw HTTP method (GET, POST, ...).
	Method string
	// Path is the full request path.
	Path string
	// StatusCode is the HTTP response status code.
	StatusCode int
	// Timestamp is when the request started.
	Timestamp time.Time
	// RequestID is the unique request identifier from the RequestID middleware.
	RequestID string
	// Duration is the wall-clock time the handler took to respond.
	Duration time.Duration
}

// ---------------------------------------------------------------------------
// AuditLogger interface
// ---------------------------------------------------------------------------

// AuditLogger defines the contract for writing audit entries. Implementations
// may write to a database, a file, or an external system.
type AuditLogger interface {
	// Log enqueues an audit entry for asynchronous persistence.
	Log(entry AuditEntry)
	// Stop flushes any buffered entries and shuts down the writer goroutine.
	Stop()
}

// ---------------------------------------------------------------------------
// DBAuditLogger
// ---------------------------------------------------------------------------

// dbAuditBufferSize is the capacity of the async write channel. Entries that
// arrive when the channel is full are dropped with a warning log.
const dbAuditBufferSize = 4096

// dbAuditBatchSize is the maximum number of entries flushed in a single
// INSERT statement. Larger batches amortise round-trip overhead.
const dbAuditBatchSize = 100

// dbAuditFlushInterval controls how often buffered entries are flushed even
// when the batch is not full.
const dbAuditFlushInterval = 5 * time.Second

// DBAuditLogger writes audit entries asynchronously to the tenant_audit_log
// table. It is safe for concurrent use.
type DBAuditLogger struct {
	pool   *db.Pool
	logger *zap.Logger
	ch     chan AuditEntry
	stopCh chan struct{}
}

// NewDBAuditLogger creates and starts a new database-backed audit logger.
// The returned logger's writer goroutine runs until Stop() is called.
func NewDBAuditLogger(pool *db.Pool, logger *zap.Logger) *DBAuditLogger {
	if logger == nil {
		logger = zap.NewNop()
	}

	dal := &DBAuditLogger{
		pool:   pool,
		logger: logger.Named("audit"),
		ch:     make(chan AuditEntry, dbAuditBufferSize),
		stopCh: make(chan struct{}),
	}

	go dal.run()

	return dal
}

// Log enqueues an audit entry for asynchronous persistence. If the buffer is
// full the entry is dropped with a warning — this is a deliberate trade-off
// to avoid blocking the request path.
func (dal *DBAuditLogger) Log(entry AuditEntry) {
	select {
	case dal.ch <- entry:
	default:
		dal.logger.Warn("audit buffer full, dropping entry",
			zap.String("tenant_id", entry.TenantID.String()),
			zap.String("path", entry.Path),
		)
	}
}

// Stop signals the writer goroutine to flush remaining entries and exit. It
// blocks until the goroutine finishes.
func (dal *DBAuditLogger) Stop() {
	close(dal.stopCh)
}

// run is the background goroutine that batches and flushes audit entries.
func (dal *DBAuditLogger) run() {
	batch := make([]AuditEntry, 0, dbAuditBatchSize)
	ticker := time.NewTicker(dbAuditFlushInterval)
	defer ticker.Stop()

	for {
		select {
		case entry := <-dal.ch:
			batch = append(batch, entry)
			if len(batch) >= dbAuditBatchSize {
				dal.flush(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				dal.flush(batch)
				batch = batch[:0]
			}
		case <-dal.stopCh:
			// Drain remaining entries.
			for {
				select {
				case entry := <-dal.ch:
					batch = append(batch, entry)
				default:
					if len(batch) > 0 {
						dal.flush(batch)
					}
					return
				}
			}
		}
	}
}

// flush persists a batch of audit entries to the database in a single INSERT.
func (dal *DBAuditLogger) flush(batch []AuditEntry) {
	if len(batch) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Build a multi-row INSERT.
	const numCols = 9
	var sb strings.Builder
	sb.WriteString(`INSERT INTO tenant_audit_log
		(tenant_id, action, resource, method, path, status_code, request_id, duration_ms, created_at)
		VALUES `)

	args := make([]interface{}, 0, len(batch)*numCols)
	argIdx := 0

	for i, entry := range batch {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("(")
		for j := 0; j < numCols; j++ {
			if j > 0 {
				sb.WriteString(", ")
			}
			argIdx++
			sb.WriteString(fmt.Sprintf("$%d", argIdx))
		}
		sb.WriteString(")")

		args = append(args,
			entry.TenantID,
			entry.Action,
			entry.Resource,
			entry.Method,
			entry.Path,
			entry.StatusCode,
			entry.RequestID,
			entry.Duration.Milliseconds(),
			entry.Timestamp,
		)
	}

	_, err := dal.pool.Exec(ctx, sb.String(), args...)
	if err != nil {
		dal.logger.Error("failed to flush audit entries",
			zap.Int("batch_size", len(batch)),
			zap.Error(err),
		)
	}
}

// ---------------------------------------------------------------------------
// Action inference
// ---------------------------------------------------------------------------

// actionFromMethod maps HTTP methods to high-level audit action verbs.
func actionFromMethod(method string) string {
	switch method {
	case "GET":
		return "READ"
	case "POST":
		return "CREATE"
	case "PUT", "PATCH":
		return "UPDATE"
	case "DELETE":
		return "DELETE"
	default:
		return "UNKNOWN"
	}
}

// resourceFromPath extracts the primary resource segment from a request path.
// For /api/v1/skills/... returns "skills"; for /health returns "health".
func resourceFromPath(path string) string {
	// Strip leading slash.
	if len(path) > 0 && path[0] == '/' {
		path = path[1:]
	}

	// Walk segments: skip "api", "v1", "v2" prefixes.
	segments := splitPath(path)
	for _, seg := range segments {
		if seg == "api" || seg == "v1" || seg == "v2" {
			continue
		}
		return seg
	}

	if len(segments) > 0 {
		return segments[len(segments)-1]
	}
	return "unknown"
}

// splitPath splits a path by '/' without allocating a slice for empty segments.
func splitPath(path string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(path); i++ {
		if path[i] == '/' {
			if i > start {
				parts = append(parts, path[start:i])
			}
			start = i + 1
		}
	}
	if start < len(path) {
		parts = append(parts, path[start:])
	}
	return parts
}

// ---------------------------------------------------------------------------
// Middleware
// ---------------------------------------------------------------------------

// skipAuditPaths contains path prefixes that are excluded from audit logging.
var skipAuditPaths = map[string]bool{
	"/health":  true,
	"/ready":   true,
	"/metrics": true,
}

// TenantAuditMiddleware returns a Gin middleware that logs every tenant-scoped
// request via the provided AuditLogger. Health, readiness, and metrics
// endpoints are skipped.
//
// The tm parameter is optional — pass nil to skip tenant metrics recording.
//
// The middleware MUST run AFTER TenantMiddleware so that the tenant context is
// available. Requests without a resolved tenant are logged with uuid.Nil.
//
// §11.4.84 Tenant audit logging middleware.
func TenantAuditMiddleware(logger AuditLogger, tm *metrics.TenantMetrics) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path

		// Skip health/ready/metrics probes.
		if skipAuditPaths[path] {
			c.Next()
			return
		}

		start := time.Now()

		c.Next()

		// Build the audit entry after the handler completes so we have the
		// response status code and duration.
		var tenantID uuid.UUID
		if tc := TenantFromGinContext(c); tc != nil {
			tenantID = tc.TenantID
		}

		entry := AuditEntry{
			TenantID:   tenantID,
			Action:     actionFromMethod(c.Request.Method),
			Resource:   resourceFromPath(path),
			Method:     c.Request.Method,
			Path:       path,
			StatusCode: c.Writer.Status(),
			Timestamp:  start,
			RequestID:  requestIDFromContext(c),
			Duration:   time.Since(start),
		}

		logger.Log(entry)

		// Record tenant audit metric.
		if tm != nil && tm.Enabled() {
			tm.RecordAuditEntry(tenantID.String(), entry.Action)
		}
	}
}
