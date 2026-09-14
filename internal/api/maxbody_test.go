package api

// Unit tests for the §G22 request-body cap.
//
// RED-first (§11.4.115): before this fix the MaxBodySize helper only wrapped the
// body in a MaxBytesReader and never rejected up-front, so a handler that did not
// read the body accepted an arbitrarily large Content-Length. These tests assert
// the real over-cap condition — a declared body above the cap is refused with 413
// before it is read, while an under-cap body passes through.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func maxBodyEngine(cap int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(MaxBodySize(cap))
	// A handler that NEVER reads the body: the cap must reject an oversized body
	// even here (the pre-fix reader-only behaviour would have let it through).
	r.POST("/x", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	return r
}

func postBody(r *gin.Engine, n int) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(make([]byte, n)))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestMaxBodySize_RejectsOverCap proves a declared body above the cap yields 413
// even when the handler never touches the body.
func TestMaxBodySize_RejectsOverCap(t *testing.T) {
	r := maxBodyEngine(1024)
	if w := postBody(r, 4096); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-cap body (4096 > 1024): got %d, want 413", w.Code)
	}
}

// TestMaxBodySize_AllowsUnderCap proves an under-cap body reaches the handler.
func TestMaxBodySize_AllowsUnderCap(t *testing.T) {
	r := maxBodyEngine(1024)
	if w := postBody(r, 16); w.Code != http.StatusOK {
		t.Fatalf("under-cap body (16 <= 1024): got %d, want 200", w.Code)
	}
}

// TestMaxBodySize_ZeroDisables proves a non-positive cap disables the guard (used
// by the router fallback path, which substitutes the 100 MiB default instead).
func TestMaxBodySize_ZeroDisables(t *testing.T) {
	r := maxBodyEngine(0)
	if w := postBody(r, 4096); w.Code != http.StatusOK {
		t.Fatalf("cap<=0 must disable the limit: got %d, want 200", w.Code)
	}
}
