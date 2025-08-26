// Package middleware contains shared Gin middleware used by the HTTP layer.
//
// This file provides structured request logging, a panic-safe recovery handler,
// and a request ID injector. The middleware in this module aims to deliver
// production-grade observability with minimal coupling:
//
//   - RequestID() ensures every request carries a stable correlation ID
//     (propagated via X-Request-ID and stored in the Gin context).
//   - Logger() emits structured access logs with request/response metadata
//     (latency, status, sizes), attaches a request-scoped zerolog.Logger, and
//     selects log level by outcome (info/warn/error).
//   - Recovery() converts panics into JSON 500 responses while preserving the
//     correlation ID and emitting a stack trace to logs.
//   - LoggerFrom() retrieves the request-scoped logger to enrich logs within
//     handlers and services (e.g., lg.Info().Str("chat_id", id).Msg("…")).
//
// Design notes:
//   - All middleware is safe to compose in any order, but for best results:
//     1) RequestID()
//     2) Logger() (or RedactingLogger if you use it)
//     3) Recovery()
//     so that panics and errors include the correlation ID and are logged.
//   - Query strings are truncated to a capped length to avoid log bloat.
//   - The request-scoped logger is stored under the "logger" Gin context key.
package middleware

import (
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	// requestIDKey is the Gin context key under which the request ID is stored.
	requestIDKey = "requestID"
	// requestIDHeader is the HTTP header used to propagate the correlation ID.
	requestIDHeader = "X-Request-ID"
	// maxQueryLogLength caps the number of bytes of the raw query string logged.
	maxQueryLogLength = 2048
)

// RequestID attaches (or propagates) a correlation identifier per request.
//
// Behavior:
//   - If the incoming request has X-Request-ID (header lookup is case-insensitive),
//     that value is reused. Otherwise, a new UUIDv4 is generated.
//   - The ID is written back to the response header (X-Request-ID) and stored
//     in the Gin context under the "requestID" key.
//
// Place this early in the chain so subsequent middleware/handlers can rely on
// the ID for logging and error responses.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetHeader(requestIDHeader)
		if rid == "" {
			rid = uuid.NewString()
		}
		c.Set(requestIDKey, rid)
		c.Writer.Header().Set(requestIDHeader, rid)
		c.Next()
	}
}

// Logger writes a structured access log for each request and response.
//
// Features:
//   - Records method, path (route when available), remote IP, UA, referer,
//     correlation ID, user ID (if present in context), request size,
//     response status, latency, and bytes written.
//   - Stores a request-scoped zerolog.Logger in the Gin context (key "logger")
//     so that downstream code can emit enriched logs tied to the request.
//   - Chooses log level based on outcome:
//   - error() for 5xx or when Gin context contains errors,
//   - warn()  for 4xx,
//   - info()  otherwise.
//
// Note: place this after RequestID() so logs include the correlation ID.
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		// Build request-scoped logger with common fields.
		rid, _ := c.Get(requestIDKey)
		uid, _ := c.Get("userID")
		path := c.FullPath()
		if path == "" {
			// Fallback when route not matched / 404.
			path = c.Request.URL.Path
		}

		l := log.With().
			Str("request_id", asString(rid)).
			Str("user_id", asString(uid)).
			Str("method", c.Request.Method).
			Str("path", path).
			Str("remote_ip", c.ClientIP()).
			Str("user_agent", c.Request.UserAgent()).
			Str("referer", c.Request.Referer()).
			Str("query", truncate(c.Request.URL.RawQuery, maxQueryLogLength)).
			// ContentLength can be -1 if unknown.
			Int64("bytes_in", c.Request.ContentLength).
			Logger()

		// Make it available to handlers/services.
		c.Set("logger", &l)

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()
		bytesOut := c.Writer.Size()

		// Attach response fields & emit at level based on status.
		ev := l.With().
			Int("status", status).
			Dur("latency", latency).
			Int("bytes_out", bytesOut).
			Logger()

		switch {
		// If Gin collected errors, prefer error level.
		case len(c.Errors) > 0:
			ev.Error().Str("errors", c.Errors.String()).Msg("request")
		case status >= 500:
			ev.Error().Msg("request")
		case status >= 400:
			ev.Warn().Msg("request")
		default:
			ev.Info().Msg("request")
		}
	}
}

// Recovery intercepts panics, logs a stack trace, and returns a JSON 500 error.
//
// Behavior:
//   - Logs the panic value and stack trace with the request ID.
//   - If no response has been written, emits a standardized JSON error body:
//     { "request_id": "...", "code": "internal_error", "message": "internal server error" }
//   - Ensures the X-Request-ID header is present on the response.
//
// Place this after Logger() so the panic is captured with structured context.
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				rid, _ := c.Get(requestIDKey)
				log.Error().
					Interface("panic", rec).
					Bytes("stack", debug.Stack()).
					Str("request_id", asString(rid)).
					Msg("panic recovered")

				// Only write if nothing has been written yet.
				if !c.Writer.Written() {
					c.Header("Content-Type", "application/json")
					c.Header(requestIDHeader, asString(rid))
					c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
						"request_id": asString(rid),
						"code":       "internal_error",
						"message":    "internal server error",
					})
					return
				}
				c.AbortWithStatus(http.StatusInternalServerError)
			}
		}()
		c.Next()
	}
}

// LoggerFrom returns the request-scoped zerolog.Logger.
//
// If a logger was not previously attached by Logger(), a fallback logger is
// returned (without request-scoped fields). Callers can safely use the result
// without nil checks.
func LoggerFrom(c *gin.Context) *zerolog.Logger {
	if v, ok := c.Get("logger"); ok {
		if lg, ok := v.(*zerolog.Logger); ok {
			return lg
		}
	}
	l := log.With().Logger()
	return &l
}

// asString converts an arbitrary interface to a string, returning an empty
// string when the value is not a string. Used for context values.
func asString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// truncate returns s unchanged when within max length, otherwise it truncates
// s to max bytes and appends an ellipsis. A max <= 0 disables truncation.
//
// Note: This operates on bytes (not runes) which is acceptable for logging.
func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
