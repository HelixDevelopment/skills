package api

// Unit tests for the §G22 per-client token-bucket rate limiter.
//
// RED-first (§11.4.115): before this limiter existed the live surface had NO
// throttle at all — every request was permitted. These tests assert the real
// over-limit condition (§11.4.201): a client that exceeds its burst is denied,
// a client under its burst is permitted, and one client's flood never drains
// another client's bucket (per-key isolation). The paired live-router mutation
// (removing limiter.Middleware() from buildRouter) makes the cmd/server 429 test
// FAIL; here the unit-level guards are the limiter's own falsifiable core.

import (
	"strconv"
	"testing"
	"time"
)

// TestRateLimiter_AllowsUnderBurstThenThrottles proves the token bucket permits
// exactly `burst` requests and then denies — the real over-limit condition.
func TestRateLimiter_AllowsUnderBurstThenThrottles(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{RequestsPerSecond: 1, Burst: 3, TTL: time.Minute})

	for i := 0; i < 3; i++ {
		if !rl.Allow("client-x") {
			t.Fatalf("request %d within burst=3 was denied; the limiter must permit up to burst", i+1)
		}
	}
	if rl.Allow("client-x") {
		t.Fatalf("request 4 above burst=3 was permitted; the limiter must throttle once the bucket is empty")
	}
}

// TestRateLimiter_PerKeyIsolation proves one client flooding to exhaustion does
// NOT throttle a different client — buckets are per key, not shared.
func TestRateLimiter_PerKeyIsolation(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{RequestsPerSecond: 1, Burst: 2, TTL: time.Minute})

	// Drain client A completely.
	drained := false
	for i := 0; i < 10; i++ {
		if !rl.Allow("A") {
			drained = true
			break
		}
	}
	if !drained {
		t.Fatalf("client A was never throttled; expected its burst to be exhausted")
	}

	// Client B must still have its own full bucket.
	if !rl.Allow("B") {
		t.Fatalf("client B was throttled by client A's flood: per-key isolation is broken")
	}
}

// TestRateLimiter_ClampsNonPositiveConfig proves an enabled-but-misconfigured
// limiter (zero rate/burst) does NOT degrade to "reject everything" — that would
// be a §11.4.201 false-positive refusal of legitimate traffic.
func TestRateLimiter_ClampsNonPositiveConfig(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{RequestsPerSecond: 0, Burst: 0, TTL: 0})
	if !rl.Allow("first") {
		t.Fatalf("a zero-configured limiter rejected the very first request; non-positive rate/burst must clamp to safe defaults, never block all traffic")
	}
}

// TestRateLimiter_HardCapBoundsMapUnderDistinctKeyFlood proves the tracking map
// is HARD-bounded (F2): flooding far more distinct keys than MaxClients never
// grows rl.clients past the cap — the least-recently-used entries are evicted.
// This is the real "cannot grow unbounded" invariant the pre-fix (TTL-only) code
// did not hold: a distinct-key/IP flood within a single TTL window grew the map
// to the flood size.
//
// §1.1 paired mutation: deleting the hard-cap eviction loop in limiterFor lets
// the map grow to `flood` entries and this guard FAILs.
func TestRateLimiter_HardCapBoundsMapUnderDistinctKeyFlood(t *testing.T) {
	const cap = 50
	const flood = 500
	// Burst 1 with a long TTL: no reaping happens during the flood, so ONLY the
	// hard cap can keep the map bounded — isolating the F2 mechanism.
	rl := NewRateLimiter(RateLimitConfig{RequestsPerSecond: 1000, Burst: 1, TTL: time.Hour, MaxClients: cap})

	for i := 0; i < flood; i++ {
		rl.Allow("k" + strconv.Itoa(i))
	}

	if got := len(rl.clients); got > cap {
		t.Fatalf("map grew to %d entries after a %d distinct-key flood; MaxClients=%d must hard-bound it (F2)", got, flood, cap)
	}
	if rl.lru.Len() != len(rl.clients) {
		t.Fatalf("LRU list (%d) and client map (%d) drifted out of sync; every tracked key must have exactly one LRU node", rl.lru.Len(), len(rl.clients))
	}
	// The limiter must still function after eviction: a fresh key is admitted.
	if !rl.Allow("post-flood-fresh-key") {
		t.Fatalf("a fresh key was denied after the flood; eviction must not break admission of new clients")
	}
}

// TestRateLimiter_ClampsNonPositiveMaxClients proves a non-positive MaxClients
// clamps to a safe positive default rather than a zero cap that would evict
// every key on sight (a §11.4.201 false-positive refusal).
func TestRateLimiter_ClampsNonPositiveMaxClients(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{RequestsPerSecond: 1000, Burst: 1, TTL: time.Hour, MaxClients: 0})
	for i := 0; i < 10; i++ {
		rl.Allow("k" + strconv.Itoa(i))
	}
	if got := len(rl.clients); got != 10 {
		t.Fatalf("MaxClients=0 must clamp to a positive default; expected 10 distinct keys retained, got %d", got)
	}
}
