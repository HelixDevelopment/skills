package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// TenantRateLimiter — construction and basic Allow
// ---------------------------------------------------------------------------

func TestNewTenantRateLimiter_Defaults(t *testing.T) {
	cfg := TenantRateLimitConfig{
		RequestsPerMinute: 60,
		BurstSize:         10,
	}
	rl := NewTenantRateLimiter(cfg)
	defer rl.Stop()

	if rl.cfg.ReaperInterval != 5*time.Minute {
		t.Errorf("expected default ReaperInterval 5m, got %v", rl.cfg.ReaperInterval)
	}
	if rl.cfg.ReaperTTL != 1*time.Hour {
		t.Errorf("expected default ReaperTTL 1h, got %v", rl.cfg.ReaperTTL)
	}
}

func TestTenantRateLimiter_Allow_SingleTenant(t *testing.T) {
	cfg := TenantRateLimitConfig{
		RequestsPerMinute: 60,
		BurstSize:         5,
	}
	rl := NewTenantRateLimiter(cfg)
	defer rl.Stop()

	tenantID := uuid.New()

	// First 5 requests (burst) should succeed.
	for i := 0; i < 5; i++ {
		if !rl.Allow(tenantID) {
			t.Fatalf("request %d should be allowed (within burst)", i)
		}
	}

	// 6th request should be throttled.
	if rl.Allow(tenantID) {
		t.Error("6th request should be throttled (burst exhausted)")
	}
}

func TestTenantRateLimiter_Allow_IndependentTenants(t *testing.T) {
	cfg := TenantRateLimitConfig{
		RequestsPerMinute: 60,
		BurstSize:         2,
	}
	rl := NewTenantRateLimiter(cfg)
	defer rl.Stop()

	tenantA := uuid.New()
	tenantB := uuid.New()

	// Exhaust tenant A.
	if !rl.Allow(tenantA) {
		t.Fatal("tenant A first request should be allowed")
	}
	if !rl.Allow(tenantA) {
		t.Fatal("tenant A second request should be allowed")
	}
	if rl.Allow(tenantA) {
		t.Error("tenant A third request should be throttled")
	}

	// Tenant B should still be allowed (independent bucket).
	if !rl.Allow(tenantB) {
		t.Error("tenant B first request should be allowed (independent)")
	}
}

func TestTenantRateLimiter_Len(t *testing.T) {
	cfg := TenantRateLimitConfig{
		RequestsPerMinute: 60,
		BurstSize:         1,
	}
	rl := NewTenantRateLimiter(cfg)
	defer rl.Stop()

	if rl.Len() != 0 {
		t.Errorf("expected 0 tracked tenants, got %d", rl.Len())
	}

	rl.Allow(uuid.New())
	if rl.Len() != 1 {
		t.Errorf("expected 1 tracked tenant, got %d", rl.Len())
	}

	rl.Allow(uuid.New())
	if rl.Len() != 2 {
		t.Errorf("expected 2 tracked tenants, got %d", rl.Len())
	}
}

// ---------------------------------------------------------------------------
// TenantRateLimiter — concurrent access stress test
// ---------------------------------------------------------------------------

func TestTenantRateLimiter_ConcurrentAccess(t *testing.T) {
	cfg := TenantRateLimitConfig{
		RequestsPerMinute: 6000,
		BurstSize:         1000,
	}
	rl := NewTenantRateLimiter(cfg)
	defer rl.Stop()

	tenantID := uuid.New()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rl.Allow(tenantID)
		}()
	}
	wg.Wait()

	// All 100 should have been allowed (within the 1000 burst).
	if rl.Len() != 1 {
		t.Errorf("expected 1 tracked tenant after concurrent access, got %d", rl.Len())
	}
}

// ---------------------------------------------------------------------------
// TenantRateLimiter — reaper
// ---------------------------------------------------------------------------

func TestTenantRateLimiter_ReaperEvictsIdle(t *testing.T) {
	cfg := TenantRateLimitConfig{
		RequestsPerMinute: 60,
		BurstSize:         1,
		ReaperInterval:    50 * time.Millisecond,
		ReaperTTL:         50 * time.Millisecond,
	}
	rl := NewTenantRateLimiter(cfg)
	defer rl.Stop()

	tenantID := uuid.New()
	rl.Allow(tenantID)

	if rl.Len() != 1 {
		t.Fatalf("expected 1 tenant, got %d", rl.Len())
	}

	// Wait for the reaper to run.
	time.Sleep(150 * time.Millisecond)

	if rl.Len() != 0 {
		t.Errorf("expected 0 tenants after reaping, got %d", rl.Len())
	}
}

func TestTenantRateLimiter_ReaperKeepsActive(t *testing.T) {
	cfg := TenantRateLimitConfig{
		RequestsPerMinute: 60,
		BurstSize:         1,
		ReaperInterval:    50 * time.Millisecond,
		ReaperTTL:         1 * time.Hour, // long TTL
	}
	rl := NewTenantRateLimiter(cfg)
	defer rl.Stop()

	tenantID := uuid.New()
	rl.Allow(tenantID)

	time.Sleep(150 * time.Millisecond)

	// Tenant should still be tracked (TTL is 1 hour).
	if rl.Len() != 1 {
		t.Errorf("expected 1 tenant (active, not reaped), got %d", rl.Len())
	}
}

// ---------------------------------------------------------------------------
// TenantRateLimitMiddleware — integration tests
// ---------------------------------------------------------------------------

func rateLimitTestMiddlewareRouter(rl *TenantRateLimiter) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		// Simulate TenantMiddleware placing a tenant context.
		tc := &TenantContext{
			TenantID:   uuid.MustParse("00000000-0000-0000-0000-000000000001"),
			TenantName: "test-tenant",
		}
		c.Set(ginTenantKey, tc)
		c.Request = c.Request.WithContext(WithTenant(c.Request.Context(), tc))
		c.Next()
	})
	r.Use(TenantRateLimitMiddleware(rl, nil))
	r.GET("/api/v1/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})
	return r
}

func TestTenantRateLimitMiddleware_AllowsWithinBurst(t *testing.T) {
	cfg := TenantRateLimitConfig{
		RequestsPerMinute: 60,
		BurstSize:         5,
	}
	rl := NewTenantRateLimiter(cfg)
	defer rl.Stop()
	r := rateLimitTestMiddlewareRouter(rl)

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i, w.Code)
		}
	}
}

func TestTenantRateLimitMiddleware_Returns429WhenExceeded(t *testing.T) {
	cfg := TenantRateLimitConfig{
		RequestsPerMinute: 60,
		BurstSize:         2,
	}
	rl := NewTenantRateLimiter(cfg)
	defer rl.Stop()
	r := rateLimitTestMiddlewareRouter(rl)

	// Exhaust the burst.
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i, w.Code)
		}
	}

	// Next request should be 429.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("expected Retry-After header on 429 response")
	}
	if w.Header().Get("X-RateLimit-Remaining") != "0" {
		t.Errorf("expected X-RateLimit-Remaining=0, got %q", w.Header().Get("X-RateLimit-Remaining"))
	}
}

func TestTenantRateLimitMiddleware_PassesThroughWithoutTenant(t *testing.T) {
	cfg := TenantRateLimitConfig{
		RequestsPerMinute: 1,
		BurstSize:         0,
	}
	rl := NewTenantRateLimiter(cfg)
	defer rl.Stop()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(TenantRateLimitMiddleware(rl, nil))
	r.GET("/api/v1/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	// Without tenant context, all requests should pass through.
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200 (no tenant = pass through), got %d", i, w.Code)
		}
	}
}

func TestTenantRateLimitMiddleware_MultipleTenantPaths(t *testing.T) {
	cfg := TenantRateLimitConfig{
		RequestsPerMinute: 60,
		BurstSize:         1,
	}
	rl := NewTenantRateLimiter(cfg)
	defer rl.Stop()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		tenantStr := c.GetHeader("X-Tenant-ID")
		if tenantStr == "" {
			c.Next()
			return
		}
		id, err := uuid.Parse(tenantStr)
		if err != nil {
			c.Next()
			return
		}
		tc := &TenantContext{TenantID: id, TenantName: "t-" + id.String()[:8]}
		c.Set(ginTenantKey, tc)
		c.Request = c.Request.WithContext(WithTenant(c.Request.Context(), tc))
		c.Next()
	})
	r.Use(TenantRateLimitMiddleware(rl, nil))
	r.GET("/api/v1/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	tenantA := "00000000-0000-0000-0000-000000000001"
	tenantB := "00000000-0000-0000-0000-000000000002"

	// Exhaust tenant A.
	reqA := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	reqA.Header.Set("X-Tenant-ID", tenantA)
	wA := httptest.NewRecorder()
	r.ServeHTTP(wA, reqA)
	if wA.Code != http.StatusOK {
		t.Fatalf("tenant A first: expected 200, got %d", wA.Code)
	}

	reqA2 := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	reqA2.Header.Set("X-Tenant-ID", tenantA)
	wA2 := httptest.NewRecorder()
	r.ServeHTTP(wA2, reqA2)
	if wA2.Code != http.StatusTooManyRequests {
		t.Fatalf("tenant A second: expected 429, got %d", wA2.Code)
	}

	// Tenant B should still be allowed.
	reqB := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	reqB.Header.Set("X-Tenant-ID", tenantB)
	wB := httptest.NewRecorder()
	r.ServeHTTP(wB, reqB)
	if wB.Code != http.StatusOK {
		t.Fatalf("tenant B first: expected 200, got %d", wB.Code)
	}
}

// ---------------------------------------------------------------------------
// Stress: many distinct tenants
// ---------------------------------------------------------------------------

func TestTenantRateLimiter_ManyTenants(t *testing.T) {
	cfg := TenantRateLimitConfig{
		RequestsPerMinute: 60,
		BurstSize:         1,
	}
	rl := NewTenantRateLimiter(cfg)
	defer rl.Stop()

	for i := 0; i < 1000; i++ {
		rl.Allow(uuid.New())
	}

	if rl.Len() != 1000 {
		t.Errorf("expected 1000 tracked tenants, got %d", rl.Len())
	}
}

// Ensure uuid is used (imported for tenant IDs in tests).
var _ = fmt.Sprintf
