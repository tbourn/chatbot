// Package middleware contains shared Gin middleware used by the HTTP layer.
//
// This file provides SecurityHeaders, a hardening middleware that attaches a
// conservative set of HTTP security headers suitable for JSON APIs running
// behind a reverse proxy. It supports HSTS (when traffic is HTTPS end-to-end),
// cache controls for sensitive responses, and modern browser feature policies.
//
// Design notes:
//   - Safe defaults for APIs: no CSP here (only relevant when serving HTML)
//   - HSTS is opt-in and only applied when the request is actually HTTPS
//   - Header values are idempotent and inexpensive to compute per request
package middleware

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// SecurityOptions configures HTTP security headers emitted by SecurityHeaders.
//
// EnableHSTS controls whether to emit Strict-Transport-Security for HTTPS
// requests (never for plain HTTP). Only enable when traffic is HTTPS
// end-to-end (including between proxy and app).
//
// HSTSMaxAge is the lifetime for HSTS. Common values are 15552000 (180 days)
// or 31536000 (1 year). Defaults to 180 days if not set (> 0 enforced).
//
// NoStore, when true, adds Cache-Control: no-store (plus legacy Pragma/Expires)
// to prevent caching of sensitive API responses.
//
// EnablePolicy controls whether modern browser feature policies are sent
// (Permissions-Policy and X-Permitted-Cross-Domain-Policies). They have effect
// only in user agents (browsers) and are harmless for non-browser clients.
type SecurityOptions struct {
	EnableHSTS   bool          // set true only when traffic is HTTPS end-to-end
	HSTSMaxAge   time.Duration // e.g., 180 * 24h
	NoStore      bool          // add Cache-Control: no-store
	EnablePolicy bool          // include Permissions-Policy, etc.
}

// SecurityHeaders returns a Gin middleware that adds a set of conservative,
// production-ready HTTP security headers to each response.
//
// Behavior:
//   - Always sets:
//     X-Content-Type-Options: nosniff
//     X-Frame-Options: DENY
//     Referrer-Policy: no-referrer
//   - Optionally sets (when EnablePolicy):
//     Permissions-Policy: geolocation=(), microphone=(), camera=(), payment=()
//     X-Permitted-Cross-Domain-Policies: none
//   - Optionally sets (when NoStore):
//     Cache-Control: no-store
//     Pragma: no-cache
//     Expires: 0
//   - Optionally sets (when EnableHSTS && request is HTTPS):
//     Strict-Transport-Security: max-age=<seconds>; includeSubDomains; preload
//     Note: Do not enable HSTS for localhost or when traffic between proxy and
//     app is plain HTTP.
//   - If X-Request-ID is present, exposes it via Access-Control-Expose-Headers
//     so browser clients can read it.
//
// This middleware is safe to use alongside CORS and logging middlewares.
// For HTML routes, consider adding a Content-Security-Policy header at the
// template layer rather than here to avoid breaking non-HTML API clients.
func SecurityHeaders(opt SecurityOptions) gin.HandlerFunc {
	maxAge := int(opt.HSTSMaxAge.Seconds())
	if maxAge <= 0 {
		maxAge = int((180 * 24 * time.Hour).Seconds()) // 180 days default
	}
	return func(c *gin.Context) {
		h := c.Writer.Header()

		// Baseline hardening for APIs.
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")

		// Optional modern browser feature restrictions (harmless for non-browsers).
		if opt.EnablePolicy {
			h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=(), payment=()")
			h.Set("X-Permitted-Cross-Domain-Policies", "none")
		}

		// Prevent caching of sensitive API responses when requested.
		if opt.NoStore {
			h.Set("Cache-Control", "no-store")
			h.Set("Pragma", "no-cache")
			h.Set("Expires", "0")
		}

		// Strict-Transport-Security only for HTTPS requests (never for HTTP).
		if opt.EnableHSTS && isHTTPS(c.Request) {
			h.Set("Strict-Transport-Security",
				"max-age="+itoa(maxAge)+"; includeSubDomains; preload")
		}

		// Expose X-Request-ID for clients (useful for correlating logs).
		if rid := h.Get("X-Request-ID"); rid != "" {
			// Append without clobbering existing exposed headers.
			const hdr = "Access-Control-Expose-Headers"
			cur := h.Get(hdr)
			if cur == "" {
				h.Set(hdr, "X-Request-ID")
			} else if !strings.Contains(cur, "X-Request-ID") {
				h.Set(hdr, cur+", X-Request-ID")
			}
		}

		c.Next()
	}
}

// isHTTPS reports whether the incoming request used HTTPS either directly
// (r.TLS != nil) or via a reverse proxy that set X-Forwarded-Proto: https.
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// itoa converts an int to its decimal string form without importing strconv.
// This keeps the middleware dependency surface minimal.
func itoa(i int) string { return strconvItoa(i) }

// strconvItoa is a small, allocation-free integer-to-string converter.
// It handles negatives and zero and returns a freshly sliced string.
func strconvItoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
