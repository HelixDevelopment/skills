// Package api provides the HTTP API layer for the HelixKnowledge Skill Graph System.
// It includes handlers, middleware, content negotiation, and response utilities.
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/pelletier/go-toml/v2"

	"github.com/HelixDevelopment/skills/internal/toon"
)

// ResponseFormat indicates the serialization format for API responses.
type ResponseFormat string

const (
	// FormatJSON is the fallback wire format (safety net — a client that cannot
	// speak TOON still works, §11.4.6).
	FormatJSON ResponseFormat = "json"
	// FormatTOML is supported via Accept: application/toml header.
	FormatTOML ResponseFormat = "toml"
	// FormatTOON is the primary token-oriented wire format (register G08),
	// selected via Accept: application/toon (or text/x-toon) or ?format=toon.
	FormatTOON ResponseFormat = "toon"
)

// contextKey is an unexported type for type-safe context keys.
type contextKey string

const responseFormatKey contextKey = "response_format"

// ErrorResponse is the standardized error envelope returned by the API.
type ErrorResponse struct {
	Error   string `json:"error" toml:"error"`
	Code    string `json:"code" toml:"code"`
	Details string `json:"details,omitempty" toml:"details,omitempty"`
}

// SetResponseFormat stores the negotiated format in the Gin context.
func SetResponseFormat(c *gin.Context, format ResponseFormat) {
	c.Set(string(responseFormatKey), format)
}

// GetResponseFormat retrieves the negotiated format from the Gin context.
// Defaults to JSON when none is set.
func GetResponseFormat(c *gin.Context) ResponseFormat {
	v, exists := c.Get(string(responseFormatKey))
	if !exists {
		return FormatJSON
	}
	if f, ok := v.(ResponseFormat); ok {
		return f
	}
	return FormatJSON
}

// RespondJSON writes a JSON response with the given HTTP status code.
func RespondJSON(c *gin.Context, status int, data interface{}) {
	c.JSON(status, data)
}

// RespondTOML writes a TOML response with the given HTTP status code.
func RespondTOML(c *gin.Context, status int, data interface{}) {
	c.Header("Content-Type", "application/toml; charset=utf-8")
	c.Status(status)
	if err := toml.NewEncoder(c.Writer).Encode(data); err != nil {
		// If TOML encoding fails, fall back to JSON error
		c.Header("Content-Type", "application/json; charset=utf-8")
		c.JSON(http.StatusInternalServerError, ErrorResponse{
			Error: "failed to encode TOML response",
			Code:  "encoding_error",
		})
	}
}

// RespondTOON writes a TOON response with the given HTTP status code. On encode
// failure it emits a JSON error envelope instead — never a silent, truncated,
// or empty body (§11.4.6 honest boundary; the silent-fallback bluff G08 forbids).
func RespondTOON(c *gin.Context, status int, data interface{}) {
	b, err := toon.Marshal(data)
	if err != nil {
		c.Header("Content-Type", "application/json; charset=utf-8")
		c.JSON(http.StatusInternalServerError, ErrorResponse{
			Error: "failed to encode TOON response",
			Code:  "encoding_error",
		})
		return
	}
	c.Header("Content-Type", toon.MediaType+"; charset=utf-8")
	c.Status(status)
	_, _ = c.Writer.Write(b)
}

// respondNegotiated routes a payload to the codec for the negotiated format.
// TOON is primary; JSON is the fallback (§11.4.6).
func respondNegotiated(c *gin.Context, status int, data interface{}) {
	switch GetResponseFormat(c) {
	case FormatTOON:
		RespondTOON(c, status, data)
	case FormatTOML:
		RespondTOML(c, status, data)
	default:
		RespondJSON(c, status, data)
	}
}

// RespondError writes a structured error response in the negotiated format.
func RespondError(c *gin.Context, status int, message string) {
	respondNegotiated(c, status, ErrorResponse{
		Error: message,
		Code:  http.StatusText(status),
	})
}

// RespondErrorWithCode writes a structured error response with a custom error code.
func RespondErrorWithCode(c *gin.Context, status int, code, message string) {
	respondNegotiated(c, status, ErrorResponse{
		Error: message,
		Code:  code,
	})
}

// NegotiateResponse serializes the response in the negotiated wire format
// (TOON primary, TOML, or JSON fallback) based on content negotiation.
func NegotiateResponse(c *gin.Context, status int, data interface{}) {
	respondNegotiated(c, status, data)
}

// PaginatedResponse wraps a list response with pagination metadata.
type PaginatedResponse[T any] struct {
	Data   []T `json:"data" toml:"data"`
	Total  int `json:"total" toml:"total"`
	Limit  int `json:"limit" toml:"limit"`
	Offset int `json:"offset" toml:"offset"`
}

// RespondPaginated writes a paginated response in the negotiated format.
func RespondPaginated[T any](c *gin.Context, status int, data []T, total, limit, offset int) {
	resp := PaginatedResponse[T]{
		Data:   data,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	}
	NegotiateResponse(c, status, resp)
}
