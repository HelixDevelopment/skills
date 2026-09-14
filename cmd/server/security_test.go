package main

// Runtime-layer (§11.4.108) security tests for the assembled HTTP router.
//
// These exercise the REAL buildRouter assembly (the same one cmd/server serves)
// against httptest, proving the §11.4.108 SOURCE≠RUNTIME defect is closed:
//   (a) ONE router serves BOTH an /api/v1/* route AND an /mcp/v1/* route;
//   (b) the MCP write tool-call (skill_create) is REJECTED without a valid key
//       (401 when keys are configured; 503 when unconfigured-and-not-disabled),
//       never 200;
//   (c) NO live-path response carries a wildcard Access-Control-Allow-Origin.
//
// Every assertion is paired-mutation-real: it FAILs if the corresponding guard
// is removed (drop the MCP auth guard -> the tool-call reaches a nil-pool
// handler and returns 500, not 401/503; re-add the wildcard CORS -> ACAO "*"
// reappears; un-mount the MCP group -> the route 404s instead of 401/503).
//
// The router is built with a nil *db.Pool: auth rejects every guarded request
// BEFORE any handler dereferences the pool, so no database is required.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/mcp"
	"github.com/HelixDevelopment/skills/internal/registry"
	"github.com/HelixDevelopment/skills/internal/skill"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// newTestRouter assembles the real production router (buildRouter) with the
// given auth configuration and an empty CORS allowlist (fail-closed, no
// wildcard). The *db.Pool is nil on purpose: no test path reaches a handler
// that touches it.
func newTestRouter(keys []string, authDisabled bool) *gin.Engine {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Server.APIKeys = keys
	cfg.Server.AuthDisabled = authDisabled
	// cfg.Server.AllowedOrigins is empty -> fail-closed CORS (no "*").

	var pool *db.Pool // nil: guarded handlers never run, so it is never used
	store := skill.NewStore(pool)
	reg := registry.NewRegistry(pool)
	mcpServer := mcp.NewMCPServer(pool, store, reg, cfg, zap.NewNop())
	mcpServer.RegisterTools()

	router, _ := buildRouter(cfg, pool, store, reg, mcpServer, zap.NewNop())
	return router
}

func doReq(r *gin.Engine, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestSingleRouter_MCPWriteRequiresAuth_KeysConfigured proves (a) both groups
// live on one router and (b) the MCP write tool-call is auth-guarded (401).
func TestSingleRouter_MCPWriteRequiresAuth_KeysConfigured(t *testing.T) {
	r := newTestRouter([]string{"secret-key"}, false)

	// (a) ONE router serves BOTH an /api/v1 route AND an /mcp/v1 route: an
	// unauthenticated request to each is REJECTED (401), proving the route
	// EXISTS and is guarded — a 404 here would mean the group is not mounted.
	if w := doReq(r, http.MethodGet, "/api/v1/skills", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/v1/skills without key: got %d, want 401 (route must exist and be guarded)", w.Code)
	}

	// (b) the MCP write tool-call (skill_create) is REJECTED without a key and
	// with an invalid key — never 200.
	if w := doReq(r, http.MethodPost, "/mcp/v1/tools/skill_create/call", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("POST /mcp/v1/tools/skill_create/call without key: got %d, want 401", w.Code)
	}
	if w := doReq(r, http.MethodPost, "/mcp/v1/tools/skill_create/call", map[string]string{"X-API-Key": "wrong"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("POST /mcp/v1/tools/skill_create/call with invalid key: got %d, want 401", w.Code)
	}
}

// TestSingleRouter_FailsClosed_WhenUnconfigured proves the fail-closed posture:
// with no keys and auth not explicitly disabled, BOTH the MCP write route and an
// /api/v1 route are rejected with 503 — never served open.
func TestSingleRouter_FailsClosed_WhenUnconfigured(t *testing.T) {
	r := newTestRouter(nil, false)

	if w := doReq(r, http.MethodPost, "/mcp/v1/tools/skill_create/call", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("MCP write with no keys and auth not disabled: got %d, want 503 (fail-closed)", w.Code)
	}
	if w := doReq(r, http.MethodGet, "/api/v1/skills", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/v1/skills with no keys and auth not disabled: got %d, want 503 (fail-closed)", w.Code)
	}
}

// TestNoWildcardCORSOnLivePaths proves (c): no live response — open or guarded,
// API or MCP — echoes a wildcard Access-Control-Allow-Origin for a
// non-allowlisted origin (with an empty allowlist it gets no ACAO at all).
func TestNoWildcardCORSOnLivePaths(t *testing.T) {
	r := newTestRouter([]string{"secret-key"}, false)
	evil := map[string]string{"Origin": "https://evil.example.com"}

	paths := []struct {
		method, path string
	}{
		{http.MethodGet, "/"},                                // open
		{http.MethodGet, "/mcp/v1/tools"},                    // MCP read (guarded)
		{http.MethodGet, "/api/v1/skills"},                   // API (guarded)
		{http.MethodPost, "/mcp/v1/tools/skill_create/call"}, // MCP write (guarded)
	}
	for _, p := range paths {
		w := doReq(r, p.method, p.path, evil)
		got := w.Header().Get("Access-Control-Allow-Origin")
		if got == "*" {
			t.Fatalf("%s %s: live response carries wildcard Access-Control-Allow-Origin: * (must never)", p.method, p.path)
		}
		if got != "" {
			t.Fatalf("%s %s: non-allowlisted origin got Access-Control-Allow-Origin=%q, want none (fail-closed CORS)", p.method, p.path, got)
		}
	}
}
