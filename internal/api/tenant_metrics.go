// Tenant request metrics middleware for the HelixKnowledge skill graph system.
//
// Increments the tenant_requests_total counter for every tenant-scoped API
// request, recording the tenant ID, HTTP method, path, and response status.
// Health, readiness, and metrics probes are excluded to match the audit
// middleware's skip set.
//
// §11.4.84 Tenant request metrics.
package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/HelixDevelopment/skills/internal/metrics"
)

// TenantRequestMetricsMiddleware returns a Gin middleware that increments the
// tenant_requests_total counter for each tenant-scoped request. The tm
// parameter is optional — when nil the middleware is a no-op.
//
// This middleware should run AFTER TenantMiddleware so that the tenant context
// is available. Requests without a resolved tenant are recorded with an empty
// tenant_id label.
//
// §11.4.84 Tenant request metrics middleware.
func TenantRequestMetricsMiddleware(tm *metrics.TenantMetrics) gin.HandlerFunc {
	return func(c *gin.Context) {
		if tm == nil || !tm.Enabled() {
			c.Next()
			return
		}

		path := c.Request.URL.Path

		// Skip health/ready/metrics probes (same set as audit middleware).
		if skipAuditPaths[path] {
			c.Next()
			return
		}

		start := time.Now()

		c.Next()

		var tenantID string
		if tc := TenantFromGinContext(c); tc != nil {
			tenantID = tc.TenantID.String()
		}

		status := http.StatusText(c.Writer.Status())
		tm.RecordRequest(tenantID, c.Request.Method, path, status)
		_ = start // duration available if needed in future
	}
}
