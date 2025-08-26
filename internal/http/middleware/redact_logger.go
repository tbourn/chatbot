// Package middleware contains shared Gin middleware used by the HTTP layer.
//
// This file implements RedactingLogger, a structured HTTP logger that
// automatically scrubs obvious PII from request metadata before emitting logs.
//
// Design goals:
//   - Default-safe: never logs request or response bodies
//   - Redacts common identifiers (emails, phone numbers, UUIDs)
//   - Masks sensitive headers (Authorization, Cookie, Set-Cookie, plus custom)
//   - Produces structured JSON logs via zerolog
//
// Usage:
//
//	r := gin.New()
//	r.Use(middleware.RedactingLogger(middleware.RedactOptions{
//	    MaskHeaders: []string{"X-Api-Key"},
//	}))
//
// Security note: this middleware reduces but does not eliminate the risk of
// sensitive data leaking to logs. You should still ensure that clients and
// upstream services avoid transmitting PII in query strings or headers unless
// strictly necessary.
package middleware

import (
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"
)

// RedactOptions configures additional scrub behavior for RedactingLogger.
//
// MaskHeaders specifies extra HTTP header names whose values will be fully
// replaced with "[REDACTED]". Matching is case-insensitive and merged with
// built-in sensitive headers ("Authorization", "Cookie", "Set-Cookie").
type RedactOptions struct {
	MaskHeaders []string
}

// RedactingLogger returns a Gin middleware that logs HTTP requests and
// responses with sensitive values scrubbed.
//
// Behavior:
//   - Logs method, path, query string, status, response size, latency,
//     and request headers (with scrubbing applied).
//   - Applies regex-based substitution to redact email addresses,
//     phone numbers, and UUID-like identifiers from query strings
//     and header values.
//   - Fully masks built-in sensitive headers and any additional headers
//     provided in opts.MaskHeaders.
//   - Logs in structured JSON format at INFO level by default, WARN for 4xx,
//     and ERROR for 5xx responses.
//
// NOTE: redact UUIDs *before* phone numbers to avoid the phone pattern
// accidentally matching the digit/hyphen segments of a UUID.
func RedactingLogger(opts RedactOptions) gin.HandlerFunc {
	// Compile regex patterns once.
	uuidRE := regexp.MustCompile(`(?i)\b[0-9a-f]{8}\-[0-9a-f]{4}\-[1-5][0-9a-f]{3}\-[89ab][0-9a-f]{3}\-[0-9a-f]{12}\b`)
	emailRE := regexp.MustCompile(`(?i)\b[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}\b`)
	// Digits-only phone pattern (prevents matching hex characters from UUIDs).
	// Examples matched: "+1 212-555-1212", "212 555 1212", "(212) 555-1212".
	phoneRE := regexp.MustCompile(`\b(?:\+?\d{1,3}[ .-]?)?(?:\(?\d{2,4}\)?[ .-]?)?\d{3,4}[ .-]?\d{4}\b`)

	redact := func(s string) string {
		if s == "" {
			return s
		}
		out := s
		// Order matters: IDs → email → phone (phone is the loosest).
		out = uuidRE.ReplaceAllString(out, "[REDACTED:id]")
		out = emailRE.ReplaceAllString(out, "[REDACTED:email]")
		out = phoneRE.ReplaceAllString(out, "[REDACTED:phone]")
		return out
	}

	// Build header mask set (case-insensitive).
	maskHeaders := map[string]struct{}{
		"authorization": {},
		"cookie":        {},
		"set-cookie":    {},
	}
	for _, h := range opts.MaskHeaders {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			maskHeaders[h] = struct{}{}
		}
	}

	return func(c *gin.Context) {
		start := time.Now()

		// Request path and query.
		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}
		rawQuery := c.Request.URL.RawQuery
		safeQuery := redact(rawQuery)

		// Scrub headers.
		safeHeaders := make(map[string]string, len(c.Request.Header))
		for k, vv := range c.Request.Header {
			keyLower := strings.ToLower(k)
			val := strings.Join(vv, ", ")
			if _, ok := maskHeaders[keyLower]; ok {
				safeHeaders[k] = "[REDACTED]"
				continue
			}
			safeHeaders[k] = redact(val)
		}

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()
		size := c.Writer.Size()

		reqID := c.Writer.Header().Get("X-Request-ID")
		if reqID == "" {
			reqID = c.GetHeader("X-Request-ID")
		}

		// Severity based on status.
		ev := log.Info()
		switch {
		case status >= 500:
			ev = log.Error()
		case status >= 400:
			ev = log.Warn()
		}

		ev.
			Str("request_id", reqID).
			Str("method", c.Request.Method).
			Str("path", path).
			Str("query", safeQuery).
			Int("status", status).
			Int("bytes", size).
			Dur("latency", latency).
			Interface("headers", safeHeaders).
			Msg("http_request")
	}
}
