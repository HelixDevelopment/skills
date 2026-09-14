package main

// RED-first (§11.4.115) test for the §G24 independent-review finding 3: the
// OPEN, unauthenticated /health endpoint must never leak database connection
// details (host/user/password) when the DB ping fails.
//
// pgx/pgxpool connection-error strings routinely embed exactly that DSN
// detail — e.g. "database health check failed: failed to connect to
// `host=127.0.0.1 user=skillsys password=... database=postgres`: dial error
// (dial tcp 127.0.0.1:55438: connect: connection refused)" — and the pre-fix
// handler emitted "error: " + err.Error() verbatim on this anonymous surface.
// newHealthHandler (cmd/server/main.go) now redacts the wire response to a
// coarse "ok"/"error" constant and logs the real error server-side via zap.
//
// This test exercises the REAL newHealthHandler (not a standalone helper)
// with a fake unhealthy healthPinger that returns a realistic
// connection-detail-bearing error, so it proves the handler's own redaction
// logic end-to-end without needing an actually-broken database connection.
//
// Paired §1.1 mutation: re-introducing `dbStatus = "error: " + err.Error()`
// in newHealthHandler makes TestHealthHandler_DBErrorRedacted_NoConnectionDetailsLeak
// FAIL (the leaked substrings reappear in the response body) — see the
// mutation record captured alongside this change.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// fakeUnhealthyPinger simulates a database dependency whose Health check
// returns a caller-supplied error (or nil), standing in for *db.Pool in
// these hermetic unit tests.
type fakeUnhealthyPinger struct{ err error }

func (f fakeUnhealthyPinger) Health(ctx context.Context) error { return f.err }

// TestHealthHandler_DBErrorRedacted_NoConnectionDetailsLeak proves the
// anonymous /health body on a DB-error path contains NO connection-detail
// substrings (host=/user=/password=/the literal address/credential), and
// that the coarse "database":"error" status is still reported so the
// liveness signal itself is not lost — only the sensitive detail is.
func TestHealthHandler_DBErrorRedacted_NoConnectionDetailsLeak(t *testing.T) {
	gin.SetMode(gin.TestMode)

	leaky := errors.New(
		"database health check failed: failed to connect to `host=127.0.0.1 " +
			"user=skillsys password=skillsys_test_pw database=postgres`: " +
			"dial error (dial tcp 127.0.0.1:55438: connect: connection refused)",
	)

	r := gin.New()
	r.GET("/health", newHealthHandler(fakeUnhealthyPinger{err: leaky}, "http", zap.NewNop()))

	w := doReq(r, http.MethodGet, "/health", nil)

	body := w.Body.String()
	for _, needle := range []string{"host=", "user=", "password=", "127.0.0.1", "skillsys_test_pw", "connection refused"} {
		if strings.Contains(body, needle) {
			t.Fatalf("anonymous /health body leaked connection-detail substring %q: %s", needle, body)
		}
	}
	if !strings.Contains(body, `"database":"error"`) {
		t.Fatalf("expected coarse database status \"error\" in body, got: %s", body)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200 (the live handler's overall status/code does not vary with dbStatus; see api/openapi.yaml HealthResponse notes)", w.Code)
	}
}

// TestHealthHandler_DBHealthy_OK proves the positive path: a healthy pinger
// yields the coarse "ok" database status (§11.4.201 — the redaction guard
// must never false-positive on a genuinely healthy dependency).
func TestHealthHandler_DBHealthy_OK(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.GET("/health", newHealthHandler(fakeUnhealthyPinger{err: nil}, "http", zap.NewNop()))

	w := doReq(r, http.MethodGet, "/health", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"database":"ok"`) {
		t.Fatalf("expected \"database\":\"ok\" in body, got: %s", w.Body.String())
	}
}
