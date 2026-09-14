package api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.uber.org/zap"

	"github.com/HelixDevelopment/skills/internal/toon"
)

// Prometheus metrics for API monitoring.
var (
	httpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "helix",
		Subsystem: "api",
		Name:      "http_requests_total",
		Help:      "Total number of HTTP requests",
	}, []string{"method", "endpoint", "status"})

	httpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "helix",
		Subsystem: "api",
		Name:      "http_request_duration_seconds",
		Help:      "HTTP request latency in seconds",
		Buckets:   prometheus.DefBuckets,
	}, []string{"method", "endpoint"})

	httpRequestSize = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "helix",
		Subsystem: "api",
		Name:      "http_request_size_bytes",
		Help:      "HTTP request size in bytes",
		Buckets:   []float64{100, 1000, 10000, 100000, 1000000},
	}, []string{"method", "endpoint"})

	httpResponseSize = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "helix",
		Subsystem: "api",
		Name:      "http_response_size_bytes",
		Help:      "HTTP response size in bytes",
		Buckets:   []float64{100, 1000, 10000, 100000, 1000000},
	}, []string{"method", "endpoint"})
)

// BrotliWriter wraps gin.ResponseWriter with Brotli compression.
type BrotliWriter struct {
	gin.ResponseWriter
	writer *brotli.Writer
	// writeErr records the first error returned by the underlying compressor so
	// a mid-stream compression failure is surfaced by the middleware rather than
	// silently swallowed (§G22).
	writeErr error
}

// Write implements http.ResponseWriter.
func (w *BrotliWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	if err != nil && w.writeErr == nil {
		w.writeErr = err
	}
	return n, err
}

// WriteString implements gin.ResponseWriter.
func (w *BrotliWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

// BrotliMiddleware adds Brotli compression for responses when the client
// advertises support via Accept-Encoding.
func BrotliMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Check if client accepts Brotli
		if !strings.Contains(c.GetHeader("Accept-Encoding"), "br") {
			c.Next()
			return
		}

		// Skip compression for small responses or already-compressed content
		contentType := c.Writer.Header().Get("Content-Type")
		if strings.Contains(contentType, "br") ||
			strings.Contains(contentType, "gzip") ||
			strings.Contains(contentType, "video/") ||
			strings.Contains(contentType, "audio/") ||
			strings.Contains(contentType, "image/") {
			c.Next()
			return
		}

		// Set Brotli encoding header
		c.Header("Content-Encoding", "br")
		c.Header("Vary", "Accept-Encoding")
		c.Writer.Header().Del("Content-Length") // Length will change

		// Wrap writer with Brotli compressor
		bw := &BrotliWriter{
			ResponseWriter: c.Writer,
			writer:         brotli.NewWriterLevel(c.Writer, brotli.DefaultCompression),
		}
		c.Writer = bw

		defer func() {
			// A Brotli compression error (a failed streamed write, flush, or
			// close) MUST NOT be discarded: a swallowed error yields a 200 over a
			// silently truncated/corrupt body (§G22). Capture every failure,
			// surface it on the request (c.Error + an error log), and — when the
			// response has not yet been committed — abort with 500 instead of a
			// misleading success. Once bytes have already been flushed the status
			// line is on the wire and cannot be rewritten; there the error is
			// still recorded and logged so the corruption is never silent
			// (§11.4.6 honest boundary).
			err := bw.writeErr
			if flushErr := bw.writer.Flush(); flushErr != nil && err == nil {
				err = flushErr
			}
			if closeErr := bw.writer.Close(); closeErr != nil && err == nil {
				err = closeErr
			}
			if err != nil {
				_ = c.Error(err)
				zap.L().Error("brotli response compression failed",
					zap.String("request_id", requestIDFromContext(c)),
					zap.Error(err),
				)
				if !c.Writer.Written() {
					c.Writer.WriteHeader(http.StatusInternalServerError)
				}
			}
		}()

		c.Next()
	}
}

// ContentNegotiation parses the Accept header and determines the response format.
// It sets the negotiated format in the Gin context for downstream handlers.
func ContentNegotiation() gin.HandlerFunc {
	return func(c *gin.Context) {
		accept := c.GetHeader("Accept")

		// Default to JSON
		format := FormatJSON

		if accept != "" {
			// TOON is the primary wire format (register G08); check it first, then
			// TOML, then JSON. The media-type strings are all distinct, so order
			// only decides which wins if a client lists several — TOON wins.
			switch {
			case strings.Contains(accept, toon.MediaType):
				format = FormatTOON
			case strings.Contains(accept, toon.AltMediaType):
				format = FormatTOON
			case strings.Contains(accept, "application/toml"):
				format = FormatTOML
			case strings.Contains(accept, "text/x-toml"):
				format = FormatTOML
			case strings.Contains(accept, "application/json"):
				format = FormatJSON
			}
			// An unknown Accept (or */*, or missing) falls back to JSON — the
			// safety net (§11.4.6); never an error, never a silent TOON.
		}

		// Check for format query parameter override
		if qf := c.Query("format"); qf != "" {
			switch strings.ToLower(qf) {
			case "toon":
				format = FormatTOON
			case "toml":
				format = FormatTOML
			case "json":
				format = FormatJSON
			}
		}

		SetResponseFormat(c, format)
		c.Next()
	}
}

// RequestID generates and attaches a unique request ID to each request.
// It checks for an existing X-Request-ID header first; if not present,
// it generates a new UUID.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetHeader("X-Request-ID")
		if rid == "" {
			rid = uuid.New().String()
		}
		c.Set("request_id", rid)
		c.Header("X-Request-ID", rid)
		c.Next()
	}
}

// requestIDFromContext returns the request ID stored under the "request_id"
// context key as a string. RequestID() normally stores a non-empty UUID string,
// but this accessor never assumes that: a missing key (nil interface) or a value
// of any non-string type — a mis-set key, or a middleware-ordering mistake where
// Logger()/Recovery() run before RequestID() — yields a freshly generated UUID
// instead of panicking the request goroutine on an unchecked `rid.(string)` type
// assertion (G34). The log field is therefore always a usable id and never a
// per-request DoS foot-gun.
func requestIDFromContext(c *gin.Context) string {
	if v, ok := c.Get("request_id"); ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return uuid.New().String()
}

// Logger returns a Gin middleware that logs all HTTP requests with structured fields.
func Logger(logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		raw := c.Request.URL.RawQuery
		method := c.Request.Method

		// Process request
		c.Next()

		// Collect log fields
		latency := time.Since(start)
		status := c.Writer.Status()
		clientIP := c.ClientIP()
		errorMsg := c.Errors.ByType(gin.ErrorTypePrivate).String()
		requestID := requestIDFromContext(c)

		fields := []zap.Field{
			zap.String("request_id", requestID),
			zap.Time("ts", start),
			zap.Duration("latency", latency),
			zap.String("client_ip", clientIP),
			zap.String("method", method),
			zap.String("path", path),
			zap.Int("status", status),
			zap.Int("body_size", c.Writer.Size()),
		}
		if raw != "" {
			fields = append(fields, zap.String("query", redactQuery(raw)))
		}
		if errorMsg != "" {
			fields = append(fields, zap.String("error", errorMsg))
		}

		// Log at appropriate level based on status code
		switch {
		case status >= http.StatusInternalServerError:
			logger.Error("HTTP request", fields...)
		case status >= http.StatusBadRequest:
			logger.Warn("HTTP request", fields...)
		default:
			logger.Info("HTTP request", fields...)
		}
	}
}

// sensitiveQueryParams are query-string keys whose values must never be
// written to logs. Matching is case-insensitive.
var sensitiveQueryParams = map[string]struct{}{
	"api_key":       {},
	"apikey":        {},
	"token":         {},
	"access_token":  {},
	"refresh_token": {},
	"password":      {},
	"secret":        {},
	"authorization": {},
	"signature":     {},
}

// redactQuery parses a raw URL query string and replaces the values of any
// sensitive parameters with "REDACTED" so secrets never reach the logs.
// If the query cannot be parsed it is redacted wholesale rather than risk
// leaking an embedded secret.
func redactQuery(raw string) string {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return "REDACTED"
	}
	changed := false
	for key, vals := range values {
		if _, ok := sensitiveQueryParams[strings.ToLower(key)]; !ok {
			continue
		}
		for i := range vals {
			vals[i] = "REDACTED"
		}
		values[key] = vals
		changed = true
	}
	if !changed {
		return raw
	}
	return values.Encode()
}

// Recovery returns a Gin middleware that recovers from panics,
// logs the stack trace, and returns a 500 Internal Server Error.
func Recovery() gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered interface{}) {
		requestID := requestIDFromContext(c)

		if err, ok := recovered.(error); ok {
			zap.L().Error("panic recovered",
				zap.String("request_id", requestID),
				zap.Error(err),
				zap.String("stack", string(debug.Stack())),
			)
		} else {
			zap.L().Error("panic recovered",
				zap.String("request_id", requestID),
				zap.Any("panic", recovered),
				zap.String("stack", string(debug.Stack())),
			)
		}

		RespondError(c, http.StatusInternalServerError,
			"An internal server error occurred. Please try again later.")
		c.Abort()
	})
}

// APIKeyAuth validates requests against a set of valid API keys.
// It checks the X-API-Key header and aborts with 401 if invalid.
func APIKeyAuth(validKeys []string) gin.HandlerFunc {
	// Build a lookup map for O(1) validation
	keySet := make(map[string]struct{}, len(validKeys))
	for _, k := range validKeys {
		keySet[k] = struct{}{}
	}

	return func(c *gin.Context) {
		// Extract API key from the header only. The api_key query-parameter
		// fallback was removed: query strings are routinely captured in access
		// logs, proxies, browser history, and Referer headers, so accepting a
		// secret there leaks it. Credentials must travel in the X-API-Key header.
		key := c.GetHeader("X-API-Key")

		if key == "" {
			RespondErrorWithCode(c, http.StatusUnauthorized, "missing_api_key",
				"API key required. Provide it via the X-API-Key header.")
			c.Abort()
			return
		}

		if _, valid := keySet[key]; !valid {
			RespondErrorWithCode(c, http.StatusUnauthorized, "invalid_api_key",
				"The provided API key is invalid or has been revoked.")
			c.Abort()
			return
		}

		c.Next()
	}
}

// RequireAuthConfigured is a fail-CLOSED middleware installed when API
// authentication is neither configured (no API keys) nor explicitly disabled.
// It rejects every request with 503 so protected routes are never served with
// authentication silently absent.
func RequireAuthConfigured() gin.HandlerFunc {
	return func(c *gin.Context) {
		RespondErrorWithCode(c, http.StatusServiceUnavailable, "auth_not_configured",
			"API authentication is not configured. Configure API keys, or set "+
				"auth_disabled=true to run without authentication.")
		c.Abort()
	}
}

// ResolveAPIKeyAuth selects the authentication middleware for the protected API
// group under a fail-CLOSED policy:
//
//   - API keys configured        -> APIKeyAuth validates every request.
//   - no keys, authDisabled=true  -> nil (no auth middleware), logged loudly as
//     a deliberate, explicit open-access mode.
//   - no keys, authDisabled=false -> RequireAuthConfigured rejects every request
//     (503). This replaces the prior fail-OPEN "len(keys) > 0" gate that served
//     protected routes wide open whenever no keys happened to be configured.
//
// A nil return means "install no auth middleware" and occurs ONLY in the
// explicit auth-disabled mode.
func ResolveAPIKeyAuth(apiKeys []string, authDisabled bool, logger *zap.Logger) gin.HandlerFunc {
	if logger == nil {
		logger = zap.L()
	}
	switch {
	case len(apiKeys) > 0:
		return APIKeyAuth(apiKeys)
	case authDisabled:
		logger.Warn("API authentication is DISABLED by explicit configuration " +
			"(auth_disabled=true); /api/v1 is publicly accessible")
		return nil
	default:
		logger.Error("no API keys configured and auth_disabled is not set; " +
			"failing closed — every /api/v1 request is rejected with 503 until " +
			"API keys are configured or auth is explicitly disabled")
		return RequireAuthConfigured()
	}
}

// CORS returns a middleware that sets Cross-Origin Resource Sharing headers
// driven by an explicit origin allowlist.
//
// Security: the CORS specification forbids combining a wildcard
// "Access-Control-Allow-Origin: *" (or a reflected arbitrary Origin) with
// "Access-Control-Allow-Credentials: true" — doing so would let any website
// make credentialed cross-origin requests. This implementation therefore:
//   - only echoes an Origin back when it appears in allowedOrigins, and only
//     then sets Allow-Credentials: true;
//   - supports a literal "*" entry that allows any origin WITHOUT credentials;
//   - always emits "Vary: Origin" so shared caches never serve a response
//     keyed for the wrong origin;
//   - sends no Allow-Origin header at all for non-allowlisted origins, so the
//     browser blocks the cross-origin response.
//
// An empty allowlist is fail-closed: no cross-origin origin is permitted.
func CORS(allowedOrigins []string) gin.HandlerFunc {
	allowAll := false
	originSet := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		o = strings.TrimSpace(o)
		switch {
		case o == "":
			continue
		case o == "*":
			allowAll = true
		default:
			originSet[o] = struct{}{}
		}
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")

		// Cache correctness: the response depends on the request Origin.
		c.Header("Vary", "Origin")

		allowed := false
		if origin != "" {
			if _, ok := originSet[origin]; ok {
				// Exact allowlist match: safe to allow credentials.
				c.Header("Access-Control-Allow-Origin", origin)
				c.Header("Access-Control-Allow-Credentials", "true")
				allowed = true
			} else if allowAll {
				// Wildcard is only safe without credentials.
				c.Header("Access-Control-Allow-Origin", "*")
				allowed = true
			}
		}

		// Advertise the rest of the policy only for allowed cross-origin
		// requests (or same-origin/non-browser clients that send no Origin).
		if allowed || origin == "" {
			c.Header("Access-Control-Allow-Headers",
				"Content-Type, Content-Length, Accept, Accept-Encoding, Authorization, X-API-Key, X-Request-ID")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			c.Header("Access-Control-Expose-Headers", "X-Request-ID, Content-Type")
			c.Header("Access-Control-Max-Age", "86400")
		}

		// Handle preflight requests.
		if c.Request.Method == http.MethodOptions {
			// Disallowed cross-origin preflight: reject rather than reply with
			// a permissive (and header-less) success.
			if origin != "" && !allowed {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// MetricsMiddleware records Prometheus metrics for each request.
func MetricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		method := c.Request.Method
		endpoint := c.FullPath()
		if endpoint == "" {
			endpoint = "unknown"
		}

		// Record request size
		if cl := c.GetHeader("Content-Length"); cl != "" {
			if size, err := strconv.ParseInt(cl, 10, 64); err == nil {
				httpRequestSize.WithLabelValues(method, endpoint).Observe(float64(size))
			}
		}

		c.Next()

		// Record after request completes
		status := strconv.Itoa(c.Writer.Status())
		duration := time.Since(start).Seconds()
		responseSize := float64(c.Writer.Size())

		httpRequestsTotal.WithLabelValues(method, endpoint, status).Inc()
		httpRequestDuration.WithLabelValues(method, endpoint).Observe(duration)
		httpResponseSize.WithLabelValues(method, endpoint).Observe(responseSize)
	}
}

// bodyLogWriter captures the response body for logging purposes.
type bodyLogWriter struct {
	gin.ResponseWriter
	body *strings.Builder
}

func (w *bodyLogWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

func (w *bodyLogWriter) WriteString(s string) (int, error) {
	w.body.WriteString(s)
	return w.ResponseWriter.WriteString(s)
}

// DefaultMaxBodyBytes is the request-body cap applied on the live router when
// the operator does not configure one (§G22). 100 MiB matches the value the
// import path was designed around.
const DefaultMaxBodyBytes int64 = 100 * 1024 * 1024

// MaxBodySize limits the request body size and returns 413 (Request Entity Too
// Large) when it is exceeded (§G22). It enforces the cap in three cases so the
// rejection is handler-INDEPENDENT (a handler that never reads the body cannot
// let an oversized request through):
//
//   - A DECLARED Content-Length above the cap is refused up-front, before the
//     body is read at all.
//   - A CHUNKED / unknown-length body (Content-Length < 0) is drained through a
//     bounded reader that stops at cap+1 bytes: if it exceeds the cap the request
//     is refused 413, otherwise the already-read bytes are handed back to the
//     handler. The read is bounded to cap+1 regardless of how large — or
//     unbounded — the incoming stream is, so a chunked flood cannot exhaust
//     memory (no OOM), while an under-cap body is delivered intact (W2).
//   - A declared-length body within the cap is wrapped in http.MaxBytesReader so
//     a body that lies about its length (sends more than it declared) is still
//     truncated at the cap when a handler reads it.
//
// A maxBytes <= 0 disables the cap.
func MaxBodySize(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if maxBytes <= 0 {
			c.Next()
			return
		}
		// Declared length over the cap: refuse before reading a single byte.
		if c.Request.ContentLength > maxBytes {
			rejectTooLarge(c, maxBytes)
			return
		}
		if c.Request.Body == nil {
			c.Next()
			return
		}
		// Unknown/chunked length: the up-front check cannot see the size, so
		// enforce the cap by reading at most cap+1 bytes into a bounded buffer.
		// This never reads the whole (possibly unbounded) stream, so it cannot be
		// used to exhaust memory.
		if c.Request.ContentLength < 0 {
			buf, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBytes+1))
			if err != nil {
				RespondErrorWithCode(c, http.StatusBadRequest, "invalid_body",
					"The request body could not be read.")
				c.Abort()
				return
			}
			if int64(len(buf)) > maxBytes {
				rejectTooLarge(c, maxBytes)
				return
			}
			// Hand the buffered body back so downstream handlers read it normally.
			c.Request.Body = io.NopCloser(bytes.NewReader(buf))
			c.Request.ContentLength = int64(len(buf))
			c.Next()
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}

// rejectTooLarge aborts the request with a 413 body-cap error.
func rejectTooLarge(c *gin.Context, maxBytes int64) {
	RespondErrorWithCode(c, http.StatusRequestEntityTooLarge, "request_too_large",
		fmt.Sprintf("Request body exceeds the %d-byte limit.", maxBytes))
	c.Abort()
}

// DetectContentType automatically detects whether request body is JSON or TOML
// and sets the "body_format" context key. It reads and restores the body.
func DetectContentType() gin.HandlerFunc {
	return func(c *gin.Context) {
		contentType := c.ContentType()

		// Normalize Content-Type header
		switch {
		case strings.Contains(contentType, "application/json"):
			c.Set("body_format", "json")
		case strings.Contains(contentType, toon.MediaType):
			c.Set("body_format", "toon")
		case strings.Contains(contentType, toon.AltMediaType):
			c.Set("body_format", "toon")
		case strings.Contains(contentType, "application/toml"):
			c.Set("body_format", "toml")
		case strings.Contains(contentType, "text/x-toml"):
			c.Set("body_format", "toml")
		case contentType == "":
			// No Content-Type: auto-detect from body
			bodyBytes, err := io.ReadAll(c.Request.Body)
			if err != nil {
				c.Set("body_format", "json")
				c.Next()
				return
			}
			c.Request.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))

			// Heuristic: TOML starts with key = value, or [section] patterns
			body := strings.TrimSpace(string(bodyBytes))
			if len(body) > 0 {
				if body[0] == '[' || (strings.Contains(body, "= ") && !strings.HasPrefix(body, "{")) {
					c.Set("body_format", "toml")
				} else {
					c.Set("body_format", "json")
				}
			} else {
				c.Set("body_format", "json")
			}
		default:
			c.Set("body_format", "json")
		}

		c.Next()
	}
}

// ValidateContentType ensures the request Content-Type is acceptable for the endpoint.
func ValidateContentType(allowed ...string) gin.HandlerFunc {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		allowedSet[a] = struct{}{}
	}

	return func(c *gin.Context) {
		ct := c.ContentType()
		if ct == "" {
			// Empty Content-Type is acceptable
			c.Next()
			return
		}

		// Strip charset suffix for comparison
		ct = strings.Split(ct, ";")[0]
		ct = strings.TrimSpace(ct)

		if _, ok := allowedSet[ct]; !ok {
			RespondErrorWithCode(c, http.StatusUnsupportedMediaType, "unsupported_media_type",
				fmt.Sprintf("Content-Type '%s' not supported. Allowed: %s", ct, strings.Join(allowed, ", ")))
			c.Abort()
			return
		}

		c.Next()
	}
}
