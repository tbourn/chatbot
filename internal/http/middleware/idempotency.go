// Package middleware contains shared Gin middleware used by the HTTP layer.
//
// This file implements idempotency support for unsafe HTTP methods (e.g., POST).
// It validates an Idempotency-Key request header, optionally performs a
// user-defined lookup to detect previously completed requests, and annotates
// the request context so downstream handlers can:
//   - read the normalized key (GetIdempotencyKey)
//   - detect replayed requests (IsReplay)
//   - bypass rate limiting when a replay is served (via an internal flag)
//
// Design goals:
//   - Keep transport concerns (validation, context stashing) in middleware.
//   - Decouple persistence via a narrow IdempotencyLookup function type.
//   - Remain framework-agnostic beyond Gin’s context.
package middleware

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"
)

// HeaderIdempotencyKey is the canonical request header that clients use to
// convey an idempotency key for unsafe operations (e.g., POST).
//
// The value is expected to be stable for a given semantic operation so that
// retries (network, client, or server initiated) can be safely deduplicated.
const HeaderIdempotencyKey = "Idempotency-Key"

// Context keys used internally to stash idempotency state.
// These keys are intentionally unexported and referenced via accessor helpers.
const (
	ctxKeyIdemKey    = "idem.key"
	ctxKeyIdemReplay = "idem.replay" // bool: true when a stored replay exists
	ctxKeyRateBypass = "rate.bypass" // bool: true to skip rate limiting
)

// GetIdempotencyKey returns the validated idempotency key stored in the Gin
// context by IdempotencyValidator. The second return value indicates presence.
//
// Handlers should prefer this function over reading the header directly.
func GetIdempotencyKey(c *gin.Context) (string, bool) {
	v, ok := c.Get(ctxKeyIdemKey)
	if !ok {
		return "", false
	}
	s, _ := v.(string)
	return s, s != ""
}

// IsReplay reports whether the middleware detected that this request would
// replay a previously completed operation (based on the provided key/user/chat).
//
// When true, upstream components (e.g., handlers, rate limiters) may choose to
// short-circuit computation and return the previously persisted result.
func IsReplay(c *gin.Context) bool {
	v, ok := c.Get(ctxKeyIdemReplay)
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

// IdempotencyOptions configures header validation behavior for
// IdempotencyValidator. TTL enforcement is intentionally out of scope here and
// should be implemented inside the provided lookup function.
type IdempotencyOptions struct {
	// MaxLen caps the accepted key length. Values <= 0 default to 200.
	MaxLen int
	// Pattern restricts allowed characters. If nil, a conservative RFC7230-like
	// token pattern is used: ^[A-Za-z0-9._~\-:]+$
	Pattern *regexp.Regexp
	// NOTE: TTL is not enforced here; enforce it within your IdempotencyLookup.
}

// IdempotencyLookup answers whether a successful, still-valid result exists for
// (userID, chatID, key) at the given time. Implementations typically consult a
// database record containing the previous response metadata and TTL window.
//
// Return exists=true when the prior response can be replayed; return an error
// only for lookup failures (which should not block normal processing).
type IdempotencyLookup func(ctx context.Context, userID, chatID, key string, now time.Time) (exists bool, err error)

// IdempotencyValidator validates the Idempotency-Key header (if present), stashes
// it in the request context, and optionally checks for a prior completed request
// via the supplied lookup. When a replay is detected, it marks the context so
// downstream components can:
//   - detect replay via IsReplay
//   - bypass rate limiting (internal flag checked by your RL middleware)
//
// Behavior:
//   - If header is absent: the middleware is a no-op.
//   - If header fails validation: responds 400 with a compact error body.
//   - If lookup indicates a replay: sets replay + rate-bypass flags.
//   - Always invokes the next handler unless validation fails.
//
// This middleware does not itself return a cached payload; handlers remain in
// control of how to serve replays (e.g., by fetching previously persisted data).
func IdempotencyValidator(opts IdempotencyOptions, lookup IdempotencyLookup) gin.HandlerFunc {
	// Sensible defaults.
	maxLen := opts.MaxLen
	if maxLen <= 0 {
		maxLen = 200
	}
	pat := opts.Pattern
	if pat == nil {
		// RFC-7230-ish token + common safe chars.
		pat = regexp.MustCompile(`^[A-Za-z0-9._~\-:]+$`)
	}

	return func(c *gin.Context) {
		key := c.GetHeader(HeaderIdempotencyKey)
		if key == "" {
			// Nothing to validate or stash; proceed.
			c.Next()
			return
		}
		if len(key) > maxLen || !pat.MatchString(key) {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"code":    "bad_idempotency_key",
				"message": "invalid Idempotency-Key",
			})
			return
		}

		// Stash the normalized key for downstream use.
		c.Set(ctxKeyIdemKey, key)

		// If we can detect a previously stored response, mark replay + rate bypass.
		if lookup != nil {
			uid := userIDFromCtx(c)
			chatID := c.Param("id") // our POST /chats/:id/messages uses :id
			now := time.Now().UTC()

			if exists, _ := lookup(c.Request.Context(), uid, chatID, key, now); exists {
				c.Set(ctxKeyIdemReplay, true)
				c.Set(ctxKeyRateBypass, true) // let RL middleware skip limiting
			}
		}

		c.Next()
	}
}

// userIDFromCtx extracts the user identifier from the Gin context as set by
// upstream authentication middleware. A development-friendly "demo-user"
// fallback is returned when no identity is available.
func userIDFromCtx(c *gin.Context) string {
	if v, ok := c.Get("userID"); ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return "demo-user"
}
