package api

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// MockAuditLogger
// ---------------------------------------------------------------------------

// MockAuditLogger is an in-memory AuditLogger for testing. It records all
// entries synchronously (no buffering) so tests can assert immediately.
type MockAuditLogger struct {
	mu      sync.Mutex
	entries []AuditEntry
	stopped bool
}

func NewMockAuditLogger() *MockAuditLogger {
	return &MockAuditLogger{}
}

func (m *MockAuditLogger) Log(entry AuditEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entry)
}

func (m *MockAuditLogger) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopped = true
}

func (m *MockAuditLogger) Entries() []AuditEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]AuditEntry, len(m.entries))
	copy(cp, m.entries)
	return cp
}

func (m *MockAuditLogger) Stopped() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopped
}

func (m *MockAuditLogger) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// ---------------------------------------------------------------------------
// actionFromMethod
// ---------------------------------------------------------------------------

func TestActionFromMethod(t *testing.T) {
	tests := []struct {
		method string
		want   string
	}{
		{"GET", "READ"},
		{"POST", "CREATE"},
		{"PUT", "UPDATE"},
		{"PATCH", "UPDATE"},
		{"DELETE", "DELETE"},
		{"OPTIONS", "UNKNOWN"},
		{"HEAD", "UNKNOWN"},
		{"", "UNKNOWN"},
	}
	for _, tt := range tests {
		got := actionFromMethod(tt.method)
		if got != tt.want {
			t.Errorf("actionFromMethod(%q) = %q, want %q", tt.method, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// resourceFromPath
// ---------------------------------------------------------------------------

func TestResourceFromPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/api/v1/skills", "skills"},
		{"/api/v1/skills/my-skill", "skills"},
		{"/api/v2/search", "search"},
		{"/health", "health"},
		{"/api/v1/tenants/abc/audit", "tenants"},
		{"/", "unknown"},
		{"", "unknown"},
		{"skills", "skills"},
	}
	for _, tt := range tests {
		got := resourceFromPath(tt.path)
		if got != tt.want {
			t.Errorf("resourceFromPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// TenantAuditMiddleware — basic logging
// ---------------------------------------------------------------------------

func auditTestRouter(logger AuditLogger, tenantID uuid.UUID) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if tenantID != uuid.Nil {
			tc := &TenantContext{
				TenantID:   tenantID,
				TenantName: "audit-tenant",
			}
			c.Set(ginTenantKey, tc)
			c.Request = c.Request.WithContext(WithTenant(c.Request.Context(), tc))
		}
		c.Next()
	})
	r.Use(TenantAuditMiddleware(logger, nil))
	r.GET("/api/v1/skills", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})
	r.POST("/api/v1/skills", func(c *gin.Context) {
		c.String(http.StatusCreated, "created")
	})
	r.GET("/health", func(c *gin.Context) {
		c.String(http.StatusOK, "healthy")
	})
	r.GET("/ready", func(c *gin.Context) {
		c.String(http.StatusOK, "ready")
	})
	r.GET("/metrics", func(c *gin.Context) {
		c.String(http.StatusOK, "metrics")
	})
	return r
}

func TestTenantAuditMiddleware_LogsRequest(t *testing.T) {
	logger := NewMockAuditLogger()
	tenantID := uuid.New()
	r := auditTestRouter(logger, tenantID)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/skills", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	entries := logger.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}

	entry := entries[0]
	if entry.TenantID != tenantID {
		t.Errorf("TenantID = %s, want %s", entry.TenantID, tenantID)
	}
	if entry.Method != "GET" {
		t.Errorf("Method = %q, want %q", entry.Method, "GET")
	}
	if entry.Action != "READ" {
		t.Errorf("Action = %q, want %q", entry.Action, "READ")
	}
	if entry.Resource != "skills" {
		t.Errorf("Resource = %q, want %q", entry.Resource, "skills")
	}
	if entry.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want %d", entry.StatusCode, http.StatusOK)
	}
	if entry.Duration <= 0 {
		t.Error("Duration should be positive")
	}
	if entry.Timestamp.IsZero() {
		t.Error("Timestamp should not be zero")
	}
}

func TestTenantAuditMiddleware_LogsPOSTAsCreate(t *testing.T) {
	logger := NewMockAuditLogger()
	tenantID := uuid.New()
	r := auditTestRouter(logger, tenantID)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/skills", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w.Code)
	}

	entries := logger.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}

	if entries[0].Action != "CREATE" {
		t.Errorf("Action = %q, want %q", entries[0].Action, "CREATE")
	}
	if entries[0].StatusCode != http.StatusCreated {
		t.Errorf("StatusCode = %d, want %d", entries[0].StatusCode, http.StatusCreated)
	}
}

// ---------------------------------------------------------------------------
// TenantAuditMiddleware — skip paths
// ---------------------------------------------------------------------------

func TestTenantAuditMiddleware_SkipsHealth(t *testing.T) {
	logger := NewMockAuditLogger()
	r := auditTestRouter(logger, uuid.New())

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if logger.Len() != 0 {
		t.Errorf("expected 0 audit entries for /health, got %d", logger.Len())
	}
}

func TestTenantAuditMiddleware_SkipsReady(t *testing.T) {
	logger := NewMockAuditLogger()
	r := auditTestRouter(logger, uuid.New())

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if logger.Len() != 0 {
		t.Errorf("expected 0 audit entries for /ready, got %d", logger.Len())
	}
}

func TestTenantAuditMiddleware_SkipsMetrics(t *testing.T) {
	logger := NewMockAuditLogger()
	r := auditTestRouter(logger, uuid.New())

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if logger.Len() != 0 {
		t.Errorf("expected 0 audit entries for /metrics, got %d", logger.Len())
	}
}

// ---------------------------------------------------------------------------
// TenantAuditMiddleware — no tenant context
// ---------------------------------------------------------------------------

func TestTenantAuditMiddleware_NoTenantUsesNilUUID(t *testing.T) {
	logger := NewMockAuditLogger()
	r := auditTestRouter(logger, uuid.Nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/skills", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	entries := logger.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	if entries[0].TenantID != uuid.Nil {
		t.Errorf("expected uuid.Nil for tenant ID, got %s", entries[0].TenantID)
	}
}

// ---------------------------------------------------------------------------
// TenantAuditMiddleware — multiple requests
// ---------------------------------------------------------------------------

func TestTenantAuditMiddleware_MultipleRequests(t *testing.T) {
	logger := NewMockAuditLogger()
	tenantID := uuid.New()
	r := auditTestRouter(logger, tenantID)

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/skills", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	}

	if logger.Len() != 5 {
		t.Errorf("expected 5 audit entries, got %d", logger.Len())
	}
}

// ---------------------------------------------------------------------------
// TenantAuditMiddleware — request ID propagation
// ---------------------------------------------------------------------------

func TestTenantAuditMiddleware_CapturesRequestID(t *testing.T) {
	logger := NewMockAuditLogger()
	tenantID := uuid.New()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Inject a known request ID.
	r.Use(func(c *gin.Context) {
		c.Set("request_id", "test-request-id-123")
		tc := &TenantContext{TenantID: tenantID}
		c.Set(ginTenantKey, tc)
		c.Request = c.Request.WithContext(WithTenant(c.Request.Context(), tc))
		c.Next()
	})
	r.Use(TenantAuditMiddleware(logger, nil))
	r.GET("/api/v1/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	entries := logger.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].RequestID != "test-request-id-123" {
		t.Errorf("RequestID = %q, want %q", entries[0].RequestID, "test-request-id-123")
	}
}

// ---------------------------------------------------------------------------
// MockAuditLogger — Stop
// ---------------------------------------------------------------------------

func TestMockAuditLogger_Stop(t *testing.T) {
	logger := NewMockAuditLogger()
	if logger.Stopped() {
		t.Error("should not be stopped initially")
	}
	logger.Stop()
	if !logger.Stopped() {
		t.Error("should be stopped after Stop()")
	}
}

// ---------------------------------------------------------------------------
// AuditEntry struct
// ---------------------------------------------------------------------------

func TestAuditEntry_Fields(t *testing.T) {
	now := time.Now()
	entry := AuditEntry{
		TenantID:   uuid.New(),
		Action:     "CREATE",
		Resource:   "skills",
		Method:     "POST",
		Path:       "/api/v1/skills",
		StatusCode: 201,
		Timestamp:  now,
		RequestID:  "req-123",
		Duration:   50 * time.Millisecond,
	}

	if entry.Action != "CREATE" {
		t.Errorf("Action = %q, want %q", entry.Action, "CREATE")
	}
	if entry.Duration != 50*time.Millisecond {
		t.Errorf("Duration = %v, want %v", entry.Duration, 50*time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// splitPath
// ---------------------------------------------------------------------------

func TestSplitPath(t *testing.T) {
	tests := []struct {
		path string
		want []string
	}{
		{"/api/v1/skills", []string{"api", "v1", "skills"}},
		{"skills", []string{"skills"}},
		{"/", nil},
		{"", nil},
		{"///a///b///", []string{"a", "b"}},
	}
	for _, tt := range tests {
		got := splitPath(tt.path)
		if len(got) != len(tt.want) {
			t.Errorf("splitPath(%q) = %v, want %v", tt.path, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("splitPath(%q)[%d] = %q, want %q", tt.path, i, got[i], tt.want[i])
			}
		}
	}
}

// Ensure uuid is used.
var _ = uuid.UUID{}
