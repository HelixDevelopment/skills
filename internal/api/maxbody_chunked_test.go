package api

// §G22 body-cap: chunked / streamed (Content-Length unknown) over-cap path (W2).
//
// RED-first (§11.4.115): the pre-fix MaxBodySize only rejected up-front on a
// DECLARED Content-Length above the cap and otherwise wrapped the body in a
// MaxBytesReader that a handler which never reads the body never trips — so a
// chunked (ContentLength = -1) over-cap body reached a non-reading handler and
// returned 200 while an UNBOUNDED stream could still be read into memory by a
// handler that did read it. These guards assert the cap is enforced by the
// MIDDLEWARE (handler-independent) for the unknown-length case, with a bounded
// read (no OOM) proven against an INFINITE body.
//
// §1.1 paired mutation: removing the ContentLength<0 drain branch makes the
// over-cap guard FAIL (200 instead of 413).

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// infiniteBody is an unbounded request body: it never returns EOF. A cap that
// read the whole stream would run forever / exhaust memory; the guard proves the
// middleware reads at most cap+1 bytes and then rejects.
type infiniteBody struct{}

func (infiniteBody) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'A'
	}
	return len(p), nil
}

// chunkedReq builds a POST whose Content-Length is unknown (-1), i.e. a
// chunked/streamed request, with the given body reader.
func chunkedReq(path string, body io.Reader) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, body)
	req.ContentLength = -1
	req.TransferEncoding = []string{"chunked"}
	return req
}

// maxBodyReadEngine installs the cap in front of a handler that READS the whole
// body (proving the reconstructed body survives for the under-cap case).
func maxBodyReadEngine(cap int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(MaxBodySize(cap))
	r.POST("/x", func(c *gin.Context) {
		n, _ := io.Copy(io.Discard, c.Request.Body)
		c.String(http.StatusOK, "read %d", n)
	})
	return r
}

// TestMaxBodySize_ChunkedOverCapRejected413NoOOM proves the over-cap chunked
// path is refused with 413 by the middleware itself — against an INFINITE body,
// so a passing test also proves the read is bounded (no OOM). The handler never
// reads the body, proving the guarantee is handler-independent.
func TestMaxBodySize_ChunkedOverCapRejected413NoOOM(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(MaxBodySize(1024))
	// A handler that NEVER reads the body: the middleware must still reject the
	// oversized chunked stream up front.
	r.POST("/x", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, chunkedReq("/x", infiniteBody{}))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("infinite chunked body over cap: got %d, want 413 (middleware must bound the "+
			"read to cap+1 and reject; the pre-fix reader-only path never trips for a "+
			"non-reading handler)", w.Code)
	}
}

// TestMaxBodySize_ChunkedUnderCapReachesHandler proves an under-cap chunked body
// is not falsely rejected and its bytes survive to the handler (the drain
// reconstructs the body). Non-regression companion to the over-cap guard.
func TestMaxBodySize_ChunkedUnderCapReachesHandler(t *testing.T) {
	r := maxBodyReadEngine(1024)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, chunkedReq("/x", io.LimitReader(infiniteBody{}, 512)))
	if w.Code != http.StatusOK {
		t.Fatalf("under-cap chunked body (512 <= 1024): got %d, want 200", w.Code)
	}
	if got := w.Body.String(); got != "read 512" {
		t.Fatalf("under-cap chunked body not fully delivered to handler: got %q, want %q", got, "read 512")
	}
}
