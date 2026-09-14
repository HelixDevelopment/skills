package main

// Runtime-layer (§11.4.108) §G22 SECURITY-FIX guards for the assembled live
// router. These close the re-review's BLOCKING F1 finding: the pre-fix limiter
// keyed on the raw, UNVALIDATED X-API-Key (and trusted a spoofable
// X-Forwarded-For), so an unauthenticated attacker could mint an unlimited
// number of buckets by rotating a header — a full rate-limit bypass.
//
// RED-first (§11.4.115) — each guard REPRODUCES the bypass on the pre-fix
// artifact and flips GREEN on the fix:
//
//   (a) forged-key rotation from ONE socket: many requests, each with a
//       DIFFERENT X-API-Key, from the SAME socket peer, MUST still be throttled
//       (the limiter keys on the socket peer, never the attacker-controlled
//       header). Pre-fix: every rotated key gets a fresh bucket -> never 429.
//   (b) spoofed X-Forwarded-For is IGNORED: many requests, each with a DIFFERENT
//       X-Forwarded-For, from the SAME socket peer, MUST still be throttled
//       (ClientIP resolves to the real socket peer via SetTrustedProxies(nil) +
//       ForwardedByClientIP=false). Pre-fix: gin trusts all proxies -> ClientIP
//       follows the spoofed header -> fresh bucket per value -> never 429.
//   (e) a 429 response carries a Retry-After header (a valid seconds hint).
//
// §1.1 paired mutation: reverting clientKey to key-on-X-API-Key makes (a) FAIL;
// reverting SetTrustedProxies(nil)/ForwardedByClientIP=false makes (b) FAIL;
// removing the Retry-After header makes (e) FAIL. The router is built with a nil
// *db.Pool: the throttle fires BEFORE any handler dereferences the pool.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/gin-gonic/gin"
)

// g22FixCfg returns a config with a tiny rate limit (burst 3) so throttling is
// reached within a short flood, and one configured API key so the auth group is
// active (proving the throttle fires ahead of — and independently of — auth).
func g22FixCfg() *config.Config {
	cfg := &config.Config{}
	cfg.Server.APIKeys = []string{"valid-key"}
	cfg.Server.RateLimit = config.RateLimitConfig{
		Enabled:           true,
		RequestsPerSecond: 1,
		Burst:             3,
		TTL:               time.Minute,
	}
	return cfg
}

// doReqFrom issues a GET carrying a fixed socket peer (RemoteAddr) plus the
// given headers, so a test can hold the socket constant while varying
// attacker-controlled headers.
func doReqFrom(r *gin.Engine, remoteAddr, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = remoteAddr
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestLiveRouter_ForgedKeyRotationThrottledBySocket proves guard (a): rotating
// the X-API-Key header from ONE socket does NOT let a client escape the limiter.
func TestLiveRouter_ForgedKeyRotationThrottledBySocket(t *testing.T) {
	r := newG22Router(g22FixCfg())

	const socket = "203.0.113.7:44444"
	sawThrottle := false
	for i := 0; i < 60; i++ {
		// Every request presents a DIFFERENT (forged) X-API-Key from the SAME
		// socket. A limiter that keys on the header would hand each one a fresh
		// bucket and never throttle — the bypass this guard forbids.
		w := doReqFrom(r, socket, "/api/v1/skills", map[string]string{
			"X-API-Key": fmt.Sprintf("forged-%d", i),
		})
		if w.Code == http.StatusTooManyRequests {
			sawThrottle = true
			break
		}
	}
	if !sawThrottle {
		t.Fatalf("rotating X-API-Key from one socket was never throttled: the limiter " +
			"keys on the attacker-controlled header, so a forged-key rotation mints " +
			"unlimited buckets and bypasses the rate limit (F1)")
	}
}

// TestLiveRouter_SpoofedForwardedForIgnored proves guard (b): rotating
// X-Forwarded-For from ONE socket does NOT let a client escape the limiter.
func TestLiveRouter_SpoofedForwardedForIgnored(t *testing.T) {
	r := newG22Router(g22FixCfg())

	const socket = "203.0.113.9:55555"
	sawThrottle := false
	for i := 0; i < 60; i++ {
		// Same socket peer, a DIFFERENT spoofed X-Forwarded-For each time. With
		// the proxy header trusted (the pre-fix default) ClientIP() follows the
		// spoofed value and every request gets a fresh bucket.
		w := doReqFrom(r, socket, "/api/v1/skills", map[string]string{
			"X-Forwarded-For": fmt.Sprintf("198.51.100.%d", i%250+1),
		})
		if w.Code == http.StatusTooManyRequests {
			sawThrottle = true
			break
		}
	}
	if !sawThrottle {
		t.Fatalf("a spoofed X-Forwarded-For was honoured for the limiter key: rotating " +
			"the header from one socket was never throttled, so ClientIP is following " +
			"an untrusted proxy header instead of the real socket peer (F1)")
	}
}

// TestLiveRouter_RateLimited429CarriesRetryAfter proves guard (e): the throttle
// response carries a parseable, positive Retry-After seconds hint (W3).
func TestLiveRouter_RateLimited429CarriesRetryAfter(t *testing.T) {
	r := newG22Router(g22FixCfg())

	const socket = "203.0.113.11:33333"
	var throttled *httptest.ResponseRecorder
	for i := 0; i < 60; i++ {
		w := doReqFrom(r, socket, "/api/v1/skills", nil)
		if w.Code == http.StatusTooManyRequests {
			throttled = w
			break
		}
	}
	if throttled == nil {
		t.Fatalf("flood from one socket was never throttled; expected a 429 to assert Retry-After on")
	}
	ra := throttled.Header().Get("Retry-After")
	if ra == "" {
		t.Fatalf("429 response is missing the Retry-After header (W3)")
	}
	secs, err := strconv.Atoi(ra)
	if err != nil || secs < 1 {
		t.Fatalf("Retry-After header %q is not a positive integer seconds hint (W3)", ra)
	}
}
