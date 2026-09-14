package main

// Runtime-layer (§11.4.108) §G24 tests for the assembled LIVE router.
//
// These exercise the REAL buildRouter assembly (the same one cmd/server serves)
// via httptest, proving the §G24 telemetry-hardening posture is wired on the
// LIVE surface — not the dead internal/api/server.go path (which registered
// /metrics + /version OUTSIDE auth and was never wired; the §11.4.108
// SOURCE≠RUNTIME trap). They assert:
//
//	(a) anonymous GET /metrics is DENIED (401 with keys configured) — the
//	    Prometheus exposition (goroutine/memory gauges, version strings, Go
//	    runtime internals) is never leaked to an unauthenticated caller;
//	(b) anonymous GET /version is DENIED (401) — build/version info is gated,
//	    matching the api/openapi.yaml contract (401);
//	(c) anonymous GET /health stays OPEN (200) — the liveness probe an
//	    orchestrator/systemd needs must reach it WITHOUT a key (regression guard:
//	    the gated telemetry must NOT accidentally auth /health);
//	(d) an AUTHENTICATED GET /metrics (valid X-API-Key) returns 200 with the real
//	    Prometheus exposition (positive path — §11.4.201: the guard denies a
//	    genuine unauthenticated request but never a legitimate one);
//	(e) an AUTHENTICATED GET /version (valid X-API-Key) returns 200 with the
//	    build/version JSON (positive path).
//
// RED-first / paired mutation (§11.4.115 / §1.1): before the fix, buildRouter
// registers NEITHER /metrics NOR /version, so (a)/(b)/(d)/(e) get 404 and FAIL.
// After the fix they are registered UNDER the SAME fail-closed authMW as
// /api/v1. The §1.1 mutation moves the /metrics registration OUTSIDE the auth
// guard (router root) — the anonymous-denied test (a) then sees 200 and FAILs,
// proving the guard is load-bearing. The router is built with a nil *db.Pool:
// the auth guard fires BEFORE any handler dereferences the pool, and /metrics
// + /version handlers never touch the pool at all.
//
// newTestRouter + doReq are defined in security_test.go (same package).

import (
	"net/http"
	"strings"
	"testing"
)

// TestLiveRouter_Metrics_AnonymousDenied proves (a): an anonymous /metrics scrape
// on the live path is rejected (401), so the Prometheus exposition is not public.
func TestLiveRouter_Metrics_AnonymousDenied(t *testing.T) {
	r := newTestRouter([]string{"secret-key"}, false)

	w := doReq(r, http.MethodGet, "/metrics", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /metrics: got %d, want 401 (the endpoint must be registered UNDER auth on the live router; a 404 means it is not registered, a 200 means it is public and leaks Prometheus internals)", w.Code)
	}
}

// TestLiveRouter_Version_AnonymousDenied proves (b): anonymous /version is gated
// (401), aligned with the openapi.yaml contract.
func TestLiveRouter_Version_AnonymousDenied(t *testing.T) {
	r := newTestRouter([]string{"secret-key"}, false)

	w := doReq(r, http.MethodGet, "/version", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /version: got %d, want 401 (must be registered UNDER auth on the live router)", w.Code)
	}
}

// TestLiveRouter_Health_StaysOpenForLiveness proves (c): /health remains OPEN
// for orchestrator/systemd liveness probes — gating the telemetry endpoints must
// NOT accidentally auth (or remove) the liveness endpoint. This unit harness
// builds the router with a nil *db.Pool (like every other cmd/server router
// test), so the OPEN /health handler runs and its pool.Health() nil-dereference
// is recovered as 500. The invariant asserted is therefore openness+presence,
// not the wire body: an anonymous /health that is NEITHER 401 (would mean it got
// gated) NOR 404 (would mean the route vanished) proves it stayed open — a gated
// endpoint returns 401 BEFORE the handler ever runs, so the fact the handler ran
// at all (500 on the nil pool) is itself proof /health is unauthenticated. On a
// real deployment with a live pool this endpoint returns 200 (§11.4.108 runtime
// signature); that DB-backed 200 is out of scope for this nil-pool unit guard.
func TestLiveRouter_Health_StaysOpenForLiveness(t *testing.T) {
	r := newTestRouter([]string{"secret-key"}, false)

	w := doReq(r, http.MethodGet, "/health", nil)
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("anonymous GET /health: got 401 — the liveness endpoint was accidentally auth-gated; a probe must reach it WITHOUT a key")
	}
	if w.Code == http.StatusNotFound {
		t.Fatalf("anonymous GET /health: got 404 — the liveness route vanished")
	}
}

// TestLiveRouter_Metrics_AuthenticatedOK proves (d): a valid-key scrape returns
// 200 with the REAL Prometheus exposition (a custom helix_api gauge is present),
// so the guard denies only genuine unauthenticated requests, never legitimate
// scrapes (§11.4.201 — no false-positive refusal).
func TestLiveRouter_Metrics_AuthenticatedOK(t *testing.T) {
	r := newTestRouter([]string{"secret-key"}, false)

	w := doReq(r, http.MethodGet, "/metrics", map[string]string{"X-API-Key": "secret-key"})
	if w.Code != http.StatusOK {
		t.Fatalf("authenticated GET /metrics: got %d, want 200 (a valid key must reach the exposition)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "helix_api_uptime_seconds") {
		t.Fatalf("authenticated GET /metrics body did not contain the helix_api_uptime_seconds gauge: got a 200 that is not the real Prometheus exposition")
	}
}

// TestLiveRouter_Version_AuthenticatedOK proves (e): a valid-key GET /version
// returns 200 with the build/version payload (positive path).
//
// §G24 finding 1: "go_version" alone is NOT divergence-sensitive — it is the
// one field name that was already identical between the served body
// (internal/api/system_handler.go's VersionResponse json tags) and the
// pre-fix openapi.yaml VersionResponse schema, so asserting only its presence
// could not have caught the schema drift (the served body also carries
// "commit"/"build_time"/"platform" where the spec previously declared
// "git_commit"/"build_date" and had no "platform" at all). This test now
// additionally asserts "build_time" and "platform" — fields that only exist
// under those exact names on the LIVE served body — so a regression back to
// the old field names (or their absence) fails this test.
func TestLiveRouter_Version_AuthenticatedOK(t *testing.T) {
	r := newTestRouter([]string{"secret-key"}, false)

	w := doReq(r, http.MethodGet, "/version", map[string]string{"X-API-Key": "secret-key"})
	if w.Code != http.StatusOK {
		t.Fatalf("authenticated GET /version: got %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, field := range []string{"go_version", `"build_time"`, `"platform"`} {
		if !strings.Contains(body, field) {
			t.Fatalf("authenticated GET /version body did not contain the %s field: got a 200 that is not the real version payload: %s", field, body)
		}
	}
	if strings.Contains(body, "git_commit") || strings.Contains(body, "build_date") {
		t.Fatalf("authenticated GET /version body used the STALE field names git_commit/build_date instead of commit/build_time: %s", body)
	}
}
