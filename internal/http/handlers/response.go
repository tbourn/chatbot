// Package handlers provides HTTP handler implementations for the public API.
//
// This file defines the standard response utilities used across all endpoints,
// including structured error envelopes, consistent JSON serialization, and
// helpers for common HTTP patterns. The goal is to guarantee uniform responses
// for both success and failure cases, making the API predictable and
// machine-friendly.
//
// Conventions:
//   - All error responses must return an ErrorResponse with a stable `code`.
//   - `fail()` centralizes error logging and formatting, ensuring 5xx responses
//     are logged with request context for observability.
//   - `ok()` and `noContent()` simplify writing success responses in a consistent
//     shape across handlers.
//
// Example error response:
//
//	HTTP/1.1 404 Not Found
//	{
//	  "request_id": "123e4567-e89b-12d3-a456-426614174000",
//	  "code": "not_found",
//	  "message": "resource not found"
//	}
//
// Example success response:
//
//	HTTP/1.1 200 OK
//	{ "id": "abc123", "title": "New chat" }
package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tbourn/go-chat-backend/internal/http/middleware"
)

// ErrorResponse is the standard error envelope returned by all endpoints.
//
// Fields:
//   - RequestID: Optional correlation ID, echoed from X-Request-ID header, used
//     to correlate server logs with client-side errors.
//   - Code: A stable, machine-readable string (see errors.go constants).
//   - Message: A human-readable error description, safe for display to users.
//
// This struct is used in OpenAPI documentation via Swagger annotations.
type ErrorResponse struct {
	// Correlates server logs and client errors
	RequestID string `json:"request_id,omitempty" example:"123e4567-e89b-12d3-a456-426614174000"`
	// Stable, machine-readable code (see errors.go constants)
	Code string `json:"code" example:"not_found"`
	// Human-readable message (safe to show to users)
	Message string `json:"message" example:"resource not found"`
}

// fail aborts the request with a structured error and logs server-side errors.
//
// It constructs an ErrorResponse, writes it as JSON with the given HTTP status,
// and calls gin.Context.AbortWithStatusJSON to stop further processing.
//
// Server errors (>=500) are logged using the request-scoped logger from middleware.
func fail(c *gin.Context, status int, code, msg string) {
	reqID := c.Writer.Header().Get("X-Request-ID")
	resp := ErrorResponse{
		RequestID: reqID,
		Code:      code,
		Message:   msg,
	}

	// Log 5xx (server-side) with request-scoped logger
	if status >= http.StatusInternalServerError {
		lg := middleware.LoggerFrom(c)
		lg.Error().
			Int("status", status).
			Str("code", code).
			Str("message", msg).
			Msg("api error")
	}

	c.AbortWithStatusJSON(status, resp)
}

// Fail is the exported variant of fail().
//
// External packages (e.g., router setup) should call Fail to return
// consistent error envelopes without directly depending on unexported helpers.
func Fail(c *gin.Context, status int, code, msg string) { fail(c, status, code, msg) }

// ok writes a success JSON response.
//
// It serializes `body` as JSON with the given HTTP status code.
func ok(c *gin.Context, status int, body any) {
	c.JSON(status, body)
}

// noContent writes an HTTP 204 No Content response.
//
// Used when the operation succeeds but there is no response body.
func noContent(c *gin.Context) {
	c.Status(http.StatusNoContent)
}
