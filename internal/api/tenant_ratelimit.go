// Per-tenant rate limiting middleware for the HelixKnowledge skill graph system.
//
// Applies a token-bucket rate limiter per tenant, preventing any single tenant
// from monopolising shared API capacity. Each tenant identified by
// TenantMiddleware receives an independent bucket whose parameters are
// configured via TenantRateLimitConfig.
//
// When a request exceeds the tenant's allowance the middleware responds with
// 429 Too Many Requests and a Retry-After header, giving the client a
// deterministic back-off signal.
//
// A background goroutine reaps idle tenant limiters after the configured TTL
// to bound memory usage in long-running deployments.
//
// §11.4.84 Tenant-scoped rate limiting.
package api

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/time/rate"

	"github.com/HelixDevelopment/skills/internal/metrics"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// TenantRateLimitConfig controls the per-tenant token-bucket rate limiter.
type TenantRateLimitConfig struct {
	// RequestsPerMinute is the steady-state refill rate converted to tokens
	// per second (= RequestsPerMinute / 60).
	RequestsPerMinute int
	// BurstSize is the maximum instantaneous number of requests a tenant may
	// make before being throttled (the token-bucket depth).
	BurstSize int
	// ReaperInterval controls how often the background goroutine scans for
	// idle tenants. Zero defaults to 5 minutes.
	ReaperInterval time.Duration
	// ReaperTTL is the idle window after which an unused per-tenant limiter
	// entry is evicted. Zero defaults to 1 hour.
	ReaperTTL time.Duration
}

// ---------------------------------------------------------------------------
// TenantRateLimiter
// ---------------------------------------------------------------------------

// tenantLimiter tracks a single tenant's rate limiter and last-access time.
type tenantLimiter struct {
	limiter    *rate.Limiter
	lastAccess time.Time
}

// TenantRateLimiter manages per-tenant rate limiters backed by token buckets.
// It is safe for concurrent use from multiple goroutines.
type TenantRateLimiter struct {
	cfg      TenantRateLimitConfig
	mu       sync.RWMutex
	limiters map[uuid.UUID]*tenantLimiter
	stopCh   chan struct{}
}

// NewTenantRateLimiter creates and starts a new per-tenant rate limiter.
// The returned limiter's background reaper goroutine runs until Stop() is
// called.
func NewTenantRateLimiter(cfg TenantRateLimitConfig) *TenantRateLimiter {
	if cfg.ReaperInterval == 0 {
		cfg.ReaperInterval = 5 * time.Minute
	}
	if cfg.ReaperTTL == 0 {
		cfg.ReaperTTL = 1 * time.Hour
	}

	trl := &TenantRateLimiter{
		cfg:      cfg,
		limiters: make(map[uuid.UUID]*tenantLimiter),
		stopCh:   make(chan struct{}),
	}

	go trl.reap()

	return trl
}

// Allow checks whether a request from the given tenant is within the rate
// limit. Returns true if the request should proceed, false if it should be
// throttled.
func (trl *TenantRateLimiter) Allow(tenantID uuid.UUID) bool {
	trl.getOrCreate(tenantID)
	trl.mu.RLock()
	tl := trl.limiters[tenantID]
	trl.mu.RUnlock()
	return tl.limiter.Allow()
}

// Stop terminates the background reaper goroutine. The limiter itself remains
// usable — Stop is only needed for graceful shutdown.
func (trl *TenantRateLimiter) Stop() {
	close(trl.stopCh)
}

// Len returns the number of tracked tenant limiters (useful for metrics and
// testing).
func (trl *TenantRateLimiter) Len() int {
	trl.mu.RLock()
	defer trl.mu.RUnlock()
	return len(trl.limiters)
}

// getOrCreate retrieves or lazily creates a rate limiter for the given tenant.
func (trl *TenantRateLimiter) getOrCreate(tenantID uuid.UUID) {
	// Fast path: read lock.
	trl.mu.RLock()
	tl, exists := trl.limiters[tenantID]
	trl.mu.RUnlock()

	if exists {
		trl.mu.Lock()
		tl.lastAccess = time.Now()
		trl.mu.Unlock()
		return
	}

	// Slow path: write lock to create.
	trl.mu.Lock()
	defer trl.mu.Unlock()

	// Double-check after acquiring write lock.
	if tl, exists = trl.limiters[tenantID]; exists {
		tl.lastAccess = time.Now()
		return
	}

	rps := rate.Limit(float64(trl.cfg.RequestsPerMinute) / 60.0)
	tl = &tenantLimiter{
		limiter:    rate.NewLimiter(rps, trl.cfg.BurstSize),
		lastAccess: time.Now(),
	}
	trl.limiters[tenantID] = tl
}

// reap periodically removes tenant limiters that have been idle beyond the
// configured TTL.
func (trl *TenantRateLimiter) reap() {
	ticker := time.NewTicker(trl.cfg.ReaperInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			trl.reapOnce()
		case <-trl.stopCh:
			return
		}
	}
}

// reapOnce performs a single reaping pass over the limiter map.
func (trl *TenantRateLimiter) reapOnce() {
	cutoff := time.Now().Add(-trl.cfg.ReaperTTL)

	trl.mu.Lock()
	defer trl.mu.Unlock()

	for id, tl := range trl.limiters {
		if tl.lastAccess.Before(cutoff) {
			delete(trl.limiters, id)
		}
	}
}

// ---------------------------------------------------------------------------
// Middleware
// ---------------------------------------------------------------------------

// TenantRateLimitMiddleware returns a Gin middleware that enforces per-tenant
// rate limits. The middleware MUST run AFTER TenantMiddleware so that the
// tenant context is available.
//
// The tm parameter is optional — pass nil to skip tenant metrics recording.
//
// When the rate limit is exceeded the request is aborted with 429 and a
// Retry-After header indicating when the client may retry.
//
// Requests without a resolved tenant (single-tenant mode) pass through
// unthrottled.
//
// §11.4.84 Tenant rate limiting middleware.
func TenantRateLimitMiddleware(rl *TenantRateLimiter, tm *metrics.TenantMetrics) gin.HandlerFunc {
	logger := zap.L().Named("tenant_ratelimit")

	return func(c *gin.Context) {
		tc := TenantFromGinContext(c)
		if tc == nil {
			// No tenant resolved — pass through (single-tenant mode).
			c.Next()
			return
		}

		if !rl.Allow(tc.TenantID) {
			retryAfter := 1 // seconds — minimal advisory
			c.Header("Retry-After", fmt.Sprintf("%d", retryAfter))
			c.Header("X-RateLimit-Limit", fmt.Sprintf("%d", rl.cfg.RequestsPerMinute))
			c.Header("X-RateLimit-Remaining", "0")

			logger.Warn("tenant rate limit exceeded",
				zap.String("tenant_id", tc.TenantID.String()),
				zap.String("tenant_name", tc.TenantName),
				zap.String("request_id", requestIDFromContext(c)),
				zap.String("path", c.Request.URL.Path),
			)

			// Record rate limit rejection metric.
			if tm != nil && tm.Enabled() {
				tm.RecordRateLimitRejection(tc.TenantID.String())
			}

			RespondErrorWithCode(c, http.StatusTooManyRequests, "rate_limit_exceeded",
				"Tenant rate limit exceeded. Retry after the Retry-After interval.")
			c.Abort()
			return
		}

		c.Next()
	}
}
