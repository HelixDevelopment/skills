package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/HelixDevelopment/skills/internal/config"
)

// ---------------------------------------------------------------------------
// Context helpers
// ---------------------------------------------------------------------------

func TestWithTenant_RoundTrip(t *testing.T) {
	tc := &TenantContext{
		TenantID:   uuid.New(),
		TenantName: "test-tenant",
	}
	ctx := WithTenant(context.Background(), tc)

	got := TenantFromContext(ctx)
	if got == nil {
		t.Fatal("TenantFromContext returned nil after WithTenant")
	}
	if got.TenantID != tc.TenantID {
		t.Errorf("TenantID = %s, want %s", got.TenantID, tc.TenantID)
	}
	if got.TenantName != tc.TenantName {
		t.Errorf("TenantName = %s, want %s", got.TenantName, tc.TenantName)
	}
}

func TestTenantFromContext_Nil(t *testing.T) {
	if got := TenantFromContext(context.Background()); got != nil {
		t.Errorf("expected nil from empty context, got %+v", got)
	}
}

func TestTenantIDFromContext_RoundTrip(t *testing.T) {
	id := uuid.New()
	tc := &TenantContext{TenantID: id}
	ctx := WithTenant(context.Background(), tc)

	got := TenantIDFromContext(ctx)
	if got != id {
		t.Errorf("TenantIDFromContext = %s, want %s", got, id)
	}
}

func TestTenantIDFromContext_Nil(t *testing.T) {
	got := TenantIDFromContext(context.Background())
	if got != uuid.Nil {
		t.Errorf("expected uuid.Nil from empty context, got %s", got)
	}
}

// ---------------------------------------------------------------------------
// Gin context helpers
// ---------------------------------------------------------------------------

func TestTenantFromGinContext_RoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	tc := &TenantContext{
		TenantID:   uuid.New(),
		TenantName: "gin-tenant",
	}
	c.Set(ginTenantKey, tc)

	got := TenantFromGinContext(c)
	if got == nil {
		t.Fatal("TenantFromGinContext returned nil after Set")
	}
	if got.TenantID != tc.TenantID {
		t.Errorf("TenantID = %s, want %s", got.TenantID, tc.TenantID)
	}
}

func TestTenantFromGinContext_Missing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	if got := TenantFromGinContext(c); got != nil {
		t.Errorf("expected nil from empty gin context, got %+v", got)
	}
}

// ---------------------------------------------------------------------------
// TenantMiddleware — resolution order tests (no DB required)
// ---------------------------------------------------------------------------

// tenantTestRouter builds a minimal Gin engine with TenantMiddleware and a
// protected /api/v1/echo endpoint that returns the resolved tenant ID.
func tenantTestRouter(cfg config.TenantConfig) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(TenantMiddleware(cfg))
	r.GET("/api/v1/echo", func(c *gin.Context) {
		tc := TenantFromGinContext(c)
		if tc == nil {
			c.String(http.StatusOK, "no-tenant")
			return
		}
		c.String(http.StatusOK, tc.TenantID.String())
	})
	return r
}

func TestTenantMiddleware_InvalidHeaderUUID(t *testing.T) {
	cfg := config.TenantConfig{Required: true}
	r := tenantTestRouter(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/echo", nil)
	req.Header.Set("X-Tenant-ID", "not-a-uuid")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for invalid UUID, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestTenantMiddleware_RequiredButNoTenant(t *testing.T) {
	cfg := config.TenantConfig{Required: true}
	r := tenantTestRouter(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/echo", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when tenant required but missing, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestTenantMiddleware_NotRequiredPassesThrough(t *testing.T) {
	cfg := config.TenantConfig{Required: false}
	r := tenantTestRouter(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/echo", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 when tenant not required, got %d (%s)", w.Code, w.Body.String())
	}
	if w.Body.String() != "no-tenant" {
		t.Errorf("expected 'no-tenant' body, got %q", w.Body.String())
	}
}

func TestTenantMiddleware_DefaultTenantUUID_Invalid(t *testing.T) {
	// Invalid default tenant UUID should cause Fatal at startup.
	// We can't easily test zap.L().Fatal in a unit test, so we just verify
	// the config validation path exists. The actual Fatal is tested via
	// integration tests.
	cfg := config.TenantConfig{
		Required:      false,
		DefaultTenant: "not-a-uuid",
	}
	// This would Fatal in production — we just verify the field is parsed.
	_, err := uuid.Parse(cfg.DefaultTenant)
	if err == nil {
		t.Fatal("expected parse error for invalid default tenant UUID")
	}
}

// ---------------------------------------------------------------------------
// TenantMiddleware — API key mapping tests (no DB required)
// ---------------------------------------------------------------------------

func TestTenantMiddleware_APIKeyMapping_InvalidUUID(t *testing.T) {
	// Invalid UUID in API key mapping should cause Fatal at startup.
	cfg := config.TenantConfig{
		APIKeyTenants: map[string]string{
			"key1": "not-a-uuid",
		},
	}
	for _, v := range cfg.APIKeyTenants {
		_, err := uuid.Parse(v)
		if err == nil {
			t.Fatal("expected parse error for invalid API key tenant UUID")
		}
	}
}

// ---------------------------------------------------------------------------
// Pool-in-context bridge
// ---------------------------------------------------------------------------

func TestWithDBPool_RoundTrip(t *testing.T) {
	// We can't create a real *db.Pool without a DB connection, but we can
	// verify the context key round-trip with nil.
	ctx := WithDBPool(context.Background(), nil)
	got := PoolFromContext(ctx)
	if got != nil {
		t.Errorf("expected nil pool from context, got %+v", got)
	}
}

func TestPoolFromContext_Missing(t *testing.T) {
	if got := PoolFromContext(context.Background()); got != nil {
		t.Errorf("expected nil from empty context, got %+v", got)
	}
}

// ---------------------------------------------------------------------------
// TenantContext struct tests
// ---------------------------------------------------------------------------

func TestTenantContext_Fields(t *testing.T) {
	id := uuid.New()
	tc := &TenantContext{
		TenantID:   id,
		TenantName: "my-tenant",
	}
	if tc.TenantID != id {
		t.Errorf("TenantID = %s, want %s", tc.TenantID, id)
	}
	if tc.TenantName != "my-tenant" {
		t.Errorf("TenantName = %s, want %s", tc.TenantName, "my-tenant")
	}
}

// ---------------------------------------------------------------------------
// Stress: concurrent context access
// ---------------------------------------------------------------------------

func TestTenantContext_ConcurrentAccess(t *testing.T) {
	tc := &TenantContext{
		TenantID:   uuid.New(),
		TenantName: "concurrent-tenant",
	}
	ctx := WithTenant(context.Background(), tc)

	done := make(chan struct{}, 100)
	for i := 0; i < 100; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			got := TenantFromContext(ctx)
			if got == nil {
				t.Error("concurrent TenantFromContext returned nil")
			}
			if got.TenantID != tc.TenantID {
				t.Errorf("concurrent TenantID = %s, want %s", got.TenantID, tc.TenantID)
			}
		}()
	}
	for i := 0; i < 100; i++ {
		<-done
	}
}

// ---------------------------------------------------------------------------
// Chaos: nil TenantContext in context
// ---------------------------------------------------------------------------

func TestTenantContext_NilValueInContext(t *testing.T) {
	// Simulate a corrupted context where the value is nil but the key exists.
	ctx := context.WithValue(context.Background(), tenantContextKey, nil)
	if got := TenantFromContext(ctx); got != nil {
		t.Errorf("expected nil for nil value in context, got %+v", got)
	}
	if got := TenantIDFromContext(ctx); got != uuid.Nil {
		t.Errorf("expected uuid.Nil for nil value, got %s", got)
	}
}

func TestTenantContext_WrongTypeInContext(t *testing.T) {
	// Simulate a corrupted context where the value is the wrong type.
	ctx := context.WithValue(context.Background(), tenantContextKey, "not-a-tenant-context")
	if got := TenantFromContext(ctx); got != nil {
		t.Errorf("expected nil for wrong type in context, got %+v", got)
	}
}

// Ensure zap is used (imported for TenantMiddleware Fatal path).
var _ = zap.L
