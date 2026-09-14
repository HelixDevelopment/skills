package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// G34 regression guard — request-id middleware MUST NOT panic on a context
// value that is absent or not a string.
//
// Register (GAPS_AND_RISKS_REGISTER.md §G34): the Logger() and Recovery()
// middlewares read the "request_id" context value and previously asserted it
// with an unchecked `rid.(string)`. A missing key (nil interface) or a
// non-string value panics the request goroutine — a per-request DoS foot-gun.
// RequestID() normally stores a non-empty UUID string, but Logger()/Recovery()
// MUST NOT assume that: a middleware-ordering mistake (Logger installed without
// RequestID ahead of it) or a mis-set key must degrade to a safe fallback, not
// a panic.
//
// These are §11.4.115 RED-baseline-on-the-broken-artifact tests: they reproduce
// the panic on the pre-fix `rid.(string)` code (the test panics → FAILs) and
// become the permanent GREEN regression guard once the comma-ok fix lands.
// Reverting the fix to `rid.(string)` re-arms the panic and FAILs these tests
// (the §1.1 paired mutation).

// didPanic reports whether calling f panicked. It recovers any panic — including
// the runtime "interface conversion: interface {} is nil, not string" panic the
// unchecked type assertion raises — so the panic surfaces as a clean test
// verdict instead of aborting the test binary.
func didPanic(f func()) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
		}
	}()
	f()
	return false
}

// TestLogger_AbsentRequestID_DoesNotPanic drives the real Logger() middleware
// with NO request_id ever set (the "Logger installed without RequestID" ordering
// mistake). On the pre-fix `rid.(string)` code the nil interface assertion
// panics; after the comma-ok fix the log line is still emitted with a usable,
// non-empty string request id (positive evidence of the fallback).
func TestLogger_AbsentRequestID_DoesNotPanic(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	logger := zap.New(core)

	r := gin.New() // no gin.Recovery(): a panic must escape ServeHTTP so we catch it
	r.Use(Logger(logger))
	r.GET("/x", func(c *gin.Context) {
		// request_id deliberately NOT set.
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()

	if didPanic(func() { r.ServeHTTP(rec, req) }) {
		t.Fatal("Logger() panicked when request_id was absent (nil interface) — unchecked rid.(string) is the G34 defect")
	}

	// Positive evidence: exactly one log entry whose request_id field is a
	// non-empty string produced by the safe fallback.
	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 log entry, got %d", len(entries))
	}
	got, ok := entries[0].ContextMap()["request_id"]
	if !ok {
		t.Fatal("log entry has no request_id field")
	}
	s, ok := got.(string)
	if !ok {
		t.Fatalf("request_id field type = %T, want string", got)
	}
	if s == "" {
		t.Error("request_id fallback produced an empty string; want a usable id")
	}
}

// TestLogger_NonStringRequestID_DoesNotPanic drives the real Logger() middleware
// with request_id set to a NON-STRING value (an int). Pre-fix this panics on
// `rid.(string)`; post-fix the log line carries a usable string request id.
func TestLogger_NonStringRequestID_DoesNotPanic(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	logger := zap.New(core)

	r := gin.New()
	r.Use(Logger(logger))
	r.GET("/x", func(c *gin.Context) {
		c.Set("request_id", 12345) // wrong shape: an int, not a string
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()

	if didPanic(func() { r.ServeHTTP(rec, req) }) {
		t.Fatal("Logger() panicked when request_id was a non-string (int) — unchecked rid.(string) is the G34 defect")
	}

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 log entry, got %d", len(entries))
	}
	got, ok := entries[0].ContextMap()["request_id"]
	if !ok {
		t.Fatal("log entry has no request_id field")
	}
	s, ok := got.(string)
	if !ok {
		t.Fatalf("request_id field type = %T, want string", got)
	}
	if s == "" {
		t.Error("request_id fallback produced an empty string; want a usable id")
	}
}

// TestRecovery_PanicWithAbsentRequestID_DoesNotDoublePanic drives the real
// Recovery() middleware: a handler panics, Recovery() catches it, then reads
// request_id — which was never set. Pre-fix the recovery callback itself panics
// on `rid.(string)` (a double panic that escapes gin's recover and would crash
// the connection); post-fix Recovery() completes and returns a clean 500.
func TestRecovery_PanicWithAbsentRequestID_DoesNotDoublePanic(t *testing.T) {
	r := gin.New()
	r.Use(Recovery())
	r.GET("/boom", func(c *gin.Context) {
		// request_id deliberately NOT set before the panic.
		panic("kaboom")
	})

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	rec := httptest.NewRecorder()

	if didPanic(func() { r.ServeHTTP(rec, req) }) {
		t.Fatal("Recovery() itself panicked on rid.(string) with an absent request_id — a double panic (the G34 defect)")
	}

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d (Recovery must convert the panic into a clean 500)", rec.Code, http.StatusInternalServerError)
	}
}

// TestRecovery_PanicWithNonStringRequestID_DoesNotDoublePanic is the non-string
// shape of the Recovery() double-panic: request_id is set to an int before the
// handler panics.
func TestRecovery_PanicWithNonStringRequestID_DoesNotDoublePanic(t *testing.T) {
	r := gin.New()
	r.Use(Recovery())
	r.GET("/boom", func(c *gin.Context) {
		c.Set("request_id", 999) // wrong shape
		panic("kaboom")
	})

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	rec := httptest.NewRecorder()

	if didPanic(func() { r.ServeHTTP(rec, req) }) {
		t.Fatal("Recovery() itself panicked on rid.(string) with a non-string request_id — a double panic (the G34 defect)")
	}

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d (Recovery must convert the panic into a clean 500)", rec.Code, http.StatusInternalServerError)
	}
}
