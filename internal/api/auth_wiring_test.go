package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// buildAuthRouter mounts a protected /api/v1/ping guarded by whatever
// middleware ResolveAPIKeyAuth selects for the given (keys, authDisabled)
// configuration — mirroring how both cmd/server and internal/api.Server wire
// the authenticated /api/v1 group.
func buildAuthRouter(keys []string, authDisabled bool) *gin.Engine {
	r := gin.New()
	v1 := r.Group("/api/v1")
	if mw := ResolveAPIKeyAuth(keys, authDisabled, zap.NewNop()); mw != nil {
		v1.Use(mw)
	}
	v1.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })
	return r
}

func authGet(r *gin.Engine, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestResolveAPIKeyAuth_FailsClosedWhenUnconfigured guards the fixed fail-OPEN
// gate: with no API keys and auth not explicitly disabled, every protected
// request MUST be rejected (503), never served open.
func TestResolveAPIKeyAuth_FailsClosedWhenUnconfigured(t *testing.T) {
	r := buildAuthRouter(nil, false)
	w := authGet(r, nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured auth must fail closed with 503, got %d (%s)", w.Code, w.Body.String())
	}
}

// TestResolveAPIKeyAuth_EnforcesConfiguredKeys verifies a valid key passes and
// a missing/invalid key is rejected with 401.
func TestResolveAPIKeyAuth_EnforcesConfiguredKeys(t *testing.T) {
	r := buildAuthRouter([]string{"secret-key"}, false)

	if w := authGet(r, map[string]string{"X-API-Key": "secret-key"}); w.Code != http.StatusOK {
		t.Fatalf("valid key must pass, got %d (%s)", w.Code, w.Body.String())
	}
	if w := authGet(r, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("missing key must be 401, got %d", w.Code)
	}
	if w := authGet(r, map[string]string{"X-API-Key": "wrong"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("invalid key must be 401, got %d", w.Code)
	}
}

// TestResolveAPIKeyAuth_ExplicitDisableIsOpenButDeliberate verifies the only
// unauthenticated path is the explicit auth-disabled mode: it installs no
// middleware (nil) and the protected route is served.
func TestResolveAPIKeyAuth_ExplicitDisableIsOpenButDeliberate(t *testing.T) {
	if mw := ResolveAPIKeyAuth(nil, true, zap.NewNop()); mw != nil {
		t.Fatalf("explicit auth-disabled mode must install no auth middleware (got non-nil)")
	}
	r := buildAuthRouter(nil, true)
	if w := authGet(r, nil); w.Code != http.StatusOK {
		t.Fatalf("auth-disabled mode must serve, got %d", w.Code)
	}
}
