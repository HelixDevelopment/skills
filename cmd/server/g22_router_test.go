package main

// Runtime-layer (§11.4.108) §G22 tests for the assembled live router.
//
// These exercise the REAL buildRouter assembly (the same one cmd/server serves)
// via httptest, proving the DoS-hardening middleware is wired on the LIVE surface
// — not the dead internal/api/server.go path (the §11.4.108 SOURCE≠RUNTIME trap
// G01 closed). They assert:
//   (a) a burst above the configured rate is throttled (429) while the first
//       request passes — the rate limiter fires ahead of auth;
//   (b) per-key isolation — one client's flood does not 429 a different client;
//   (c) an over-cap request body is rejected (413) before auth, while an
//       under-cap body passes the cap and reaches auth (401).
//
// RED-first / paired mutation (§11.4.115 / §1.1): removing limiter.Middleware()
// from buildRouter makes (a)/(b) FAIL (no 429); removing the MaxBodySize Use (or
// reverting the 413 up-front reject) makes (c) FAIL (413 -> 401). The router is
// built with a nil *db.Pool: the throttle/cap/auth all fire BEFORE any handler
// dereferences the pool.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/mcp"
	"github.com/HelixDevelopment/skills/internal/registry"
	"github.com/HelixDevelopment/skills/internal/skill"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// newG22Router assembles the real production router (buildRouter) from an
// explicit config so each test can drive rate-limit / body-cap behaviour. The
// *db.Pool is nil on purpose: no test path reaches a handler that touches it.
func newG22Router(cfg *config.Config) *gin.Engine {
	gin.SetMode(gin.TestMode)
	var pool *db.Pool
	store := skill.NewStore(pool)
	reg := registry.NewRegistry(pool)
	mcpServer := mcp.NewMCPServer(pool, store, reg, cfg, zap.NewNop())
	mcpServer.RegisterTools()
	router, _ := buildRouter(cfg, pool, store, reg, mcpServer, zap.NewNop())
	return router
}

func doReqBody(r *gin.Engine, method, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestLiveRouter_RateLimit_BurstThrottled proves (a): a burst above the rate on
// the live path yields 429, while the first request is NOT throttled.
func TestLiveRouter_RateLimit_BurstThrottled(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.APIKeys = []string{"valid-key"}
	cfg.Server.RateLimit = config.RateLimitConfig{
		Enabled:           true,
		RequestsPerSecond: 1,
		Burst:             3,
		TTL:               time.Minute,
	}
	r := newG22Router(cfg)

	firstCode := 0
	sawThrottle := false
	for i := 0; i < 40; i++ {
		// No API key -> keyed by client IP; all requests share one bucket.
		w := doReq(r, http.MethodGet, "/api/v1/skills", nil)
		if i == 0 {
			firstCode = w.Code
		}
		if w.Code == http.StatusTooManyRequests {
			sawThrottle = true
		}
	}
	if firstCode == http.StatusTooManyRequests {
		t.Fatalf("first request within burst was throttled (got 429): the limiter must not deny legitimate traffic")
	}
	if !sawThrottle {
		t.Fatalf("a burst of 40 requests above burst=3 was never throttled: expected >=1 429 on the live path (rate limiter absent from buildRouter?)")
	}
}

// TestLiveRouter_RateLimit_PerSocketIsolation proves (b), reconciled to the
// SECURE keying (§11.4.120): the limiter isolates by the REAL socket peer, not by
// an attacker-controlled header. Socket A flooding to 429 does not throttle a
// DISTINCT socket B. The pre-fix test asserted isolation by X-API-Key value —
// which was exactly the header-rotation bypass F1 closed — so it is rewritten to
// assert per-socket isolation, the genuine per-client isolation in the R15
// no-proxy topology. (§1.1: a clientKey that ignored the socket peer, e.g.
// returned a constant, would throttle B and FAIL this guard.)
func TestLiveRouter_RateLimit_PerSocketIsolation(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.APIKeys = []string{"valid-key"}
	cfg.Server.RateLimit = config.RateLimitConfig{
		Enabled:           true,
		RequestsPerSecond: 1,
		Burst:             3,
		TTL:               time.Minute,
	}
	r := newG22Router(cfg)

	// Flood socket A (a fixed RemoteAddr) until throttled.
	sawA429 := false
	for i := 0; i < 40; i++ {
		w := doReqFrom(r, "198.51.100.10:40000", "/api/v1/skills", nil)
		if w.Code == http.StatusTooManyRequests {
			sawA429 = true
		}
	}
	if !sawA429 {
		t.Fatalf("socket A's flood was never throttled: expected >=1 429")
	}

	// A DISTINCT socket B must still have its own bucket: its first request is not
	// a 429 (no key, so it reaches fail-closed auth and gets 401 — proving it was
	// not throttled by A's flood).
	w := doReqFrom(r, "198.51.100.20:40000", "/api/v1/skills", nil)
	if w.Code == http.StatusTooManyRequests {
		t.Fatalf("socket B was throttled (429) by socket A's flood: per-socket isolation is broken on the live path")
	}
}

// TestLiveRouter_BodyCap_OverAndUnder proves (c): an over-cap body is rejected
// 413 before auth, and an under-cap body passes the cap and reaches auth (401).
func TestLiveRouter_BodyCap_OverAndUnder(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.APIKeys = []string{"valid-key"}
	cfg.Server.RateLimit.Enabled = false // isolate the body-cap behaviour
	cfg.Server.MaxRequestBodyBytes = 1024
	r := newG22Router(cfg)

	// Over-cap POST to an existing (auth-guarded) route: the global body cap
	// fires BEFORE the group auth, so the status is 413, not 401.
	over := doReqBody(r, http.MethodPost, "/mcp/v1/tools/skill_create/call", make([]byte, 4096), nil)
	if over.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-cap body (4096 > 1024) on the live path: got %d, want 413", over.Code)
	}

	// Under-cap POST: passes the body cap, then hits fail-closed auth (no key) -> 401.
	under := doReqBody(r, http.MethodPost, "/mcp/v1/tools/skill_create/call", []byte("{}"), nil)
	if under.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("under-cap body (2 <= 1024) was wrongly rejected with 413")
	}
	if under.Code != http.StatusUnauthorized {
		t.Fatalf("under-cap body: got %d, want 401 (passes the cap and reaches auth)", under.Code)
	}
}
