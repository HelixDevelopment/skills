package api

// Unit test for the §G22 Brotli-error handling fix.
//
// RED-first (§11.4.115): before this fix BrotliMiddleware discarded the return
// values of writer.Flush() and writer.Close(), so a compression failure yielded a
// 200 over a silently truncated body. This test forces the underlying writer to
// fail and asserts the middleware SURFACES the error (records it on the request
// via c.Error) rather than swallowing it. The paired mutation (reverting the
// deferred flush/close to discard the errors) makes this test FAIL.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// failingResponseWriter is an http.ResponseWriter whose body Write always fails,
// simulating a downstream/compression write failure so the Brotli flush/close in
// the middleware's defer returns a non-nil error.
type failingResponseWriter struct {
	header http.Header
	status int
}

func (f *failingResponseWriter) Header() http.Header {
	if f.header == nil {
		f.header = make(http.Header)
	}
	return f.header
}
func (f *failingResponseWriter) WriteHeader(status int) { f.status = status }
func (f *failingResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("simulated downstream write failure")
}

// TestBrotliMiddleware_FlushErrorIsHandledNotIgnored proves a Brotli
// flush/close/write error is recorded on the request instead of being discarded.
func TestBrotliMiddleware_FlushErrorIsHandledNotIgnored(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var captured *gin.Context
	r := gin.New()
	// First middleware captures the live context so we can inspect c.Errors
	// AFTER ServeHTTP returns (the Brotli defer runs during the unwind).
	r.Use(func(c *gin.Context) { captured = c; c.Next() })
	r.Use(BrotliMiddleware())
	r.GET("/", func(c *gin.Context) {
		// A payload large enough to force a real flush through the compressor.
		c.String(http.StatusOK, strings.Repeat("payload-bytes ", 256))
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "br")
	r.ServeHTTP(&failingResponseWriter{}, req)

	if captured == nil {
		t.Fatal("test setup error: gin context was not captured")
	}
	if len(captured.Errors) == 0 {
		t.Fatalf("Brotli flush/close error was discarded: the middleware recorded zero c.Errors; " +
			"a compression failure must be surfaced, never swallowed into a 200 over a truncated body")
	}
}
