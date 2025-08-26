// Package middleware contains shared Gin middleware used by the HTTP layer.
//
// This file implements a lightweight, in-memory, token-bucket rate limiter
// with per-identity buckets and opportunistic garbage collection. It is
// designed for simplicity, low overhead, and predictable behavior in a
// single-process deployment (e.g., a container or dev setup).
//
// Features:
//   - Per-key token buckets using golang.org/x/time/rate
//   - Pluggable identity function (user ID or client IP)
//   - Best-effort cleanup of idle buckets to bound memory
//   - Seamless bypass for idempotent replays (when paired with IdempotencyValidator)
//
// Notes:
//   - This limiter is process-local. For horizontally scaled deployments,
//     prefer a distributed limiter (e.g., Redis-backed) to enforce global limits.
//   - The limiter is intended for edge-level abuse control and cost protection;
//     it is not an authorization mechanism.
package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// keyFunc selects the identity used to key a rate-limit bucket.
//
// Implementations should return a stable string for the duration of a request
// (e.g., "user:<id>" or "ip:<addr>"). The returned key is used to look up the
// corresponding token bucket.
type keyFunc func(*gin.Context) string

// KeyByUserOrIP returns a keyFunc that prefers a user identity (from the Gin
// context under "userID", typically set by your auth middleware) and falls back
// to the client IP address.
//
// The resulting keys are prefixed to avoid collisions between user and IP
// namespaces (e.g., "user:abc123" vs "ip:203.0.113.7").
func KeyByUserOrIP() keyFunc {
	return func(c *gin.Context) string {
		if v, ok := c.Get("userID"); ok {
			if s, ok := v.(string); ok && s != "" {
				return "user:" + s
			}
		}
		return "ip:" + c.ClientIP()
	}
}

// visitor holds a single rate limiter and the last time it was seen.
// Used to opportunistically evict idle buckets.
type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RateLimiter implements a per-key token-bucket rate limiter.
//
// Buckets are created on demand and stored in an internal map guarded by a
// mutex. Idle buckets are evicted after a TTL via opportunistic cleanup during
// lookups to keep memory usage bounded.
//
// This type is safe for concurrent use.
type RateLimiter struct {
	rps      rate.Limit
	burst    int
	keyFn    keyFunc
	mu       sync.Mutex
	visitors map[string]*visitor

	ttl      time.Duration
	cleanupN uint64
}

// NewRateLimiter constructs a RateLimiter with the given tokens-per-second
// and burst size, keyed by keyFn.
//
//   - rps:   tokens replenished per second (0 allows no requests; use >0).
//   - burst: maximum burst size; values <= 0 are coerced to 1.
//   - keyFn: function that maps a request to a bucket identity.
//
// The returned limiter is ready to be installed as middleware via Handler().
func NewRateLimiter(rps float64, burst int, keyFn keyFunc) *RateLimiter {
	if burst <= 0 {
		burst = 1
	}
	return &RateLimiter{
		rps:      rate.Limit(rps),
		burst:    burst,
		keyFn:    keyFn,
		visitors: make(map[string]*visitor),
		ttl:      10 * time.Minute, // evict idle entries after TTL
	}
}

// getVisitor returns (and updates) the limiter for key, creating it if absent.
// It also performs opportunistic GC of idle entries after ~5000 lookups.
//
// IMPORTANT: Run GC *before* touching the requested visitor so an "old" bucket
// can be evicted even when it's the one being fetched.
func (rl *RateLimiter) getVisitor(key string) *rate.Limiter {
	now := time.Now()

	rl.mu.Lock()
	// Opportunistic cleanup after a threshold of lookups, then reset the counter.
	// Do this BEFORE updating/creating the requested visitor to avoid
	// refreshing an "old" entry that should be evicted.
	rl.cleanupN++
	if rl.cleanupN >= 5000 {
		for k, vv := range rl.visitors {
			// Evict if idle for >= TTL (robust boundary check)
			if now.Sub(vv.lastSeen) >= rl.ttl {
				delete(rl.visitors, k)
			}
		}
		rl.cleanupN = 0
	}

	// Fetch or create this visitor.
	if v, ok := rl.visitors[key]; ok {
		v.lastSeen = now
		lim := v.limiter
		rl.mu.Unlock()
		return lim
	}

	lim := rate.NewLimiter(rl.rps, rl.burst)
	rl.visitors[key] = &visitor{limiter: lim, lastSeen: now}
	rl.mu.Unlock()
	return lim
}

// IsRateBypass reports whether IdempotencyValidator marked this request for
// rate-limit bypass (i.e., it is a replay of a previously completed request).
//
// When true, Handler() will skip limiting so replays are served without
// consuming tokens.
func IsRateBypass(c *gin.Context) bool {
	v, ok := c.Get(ctxKeyRateBypass) // set by IdempotencyValidator
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

// Handler returns a Gin middleware that enforces per-key token-bucket limits.
//
// Behavior:
//   - If IsRateBypass(c) is true (idempotent replay), limiting is skipped.
//   - Otherwise, the request is checked against the key’s limiter. If allowed,
//     the request proceeds; if not, a 429 response is returned with a compact
//     JSON body and a minimal Retry-After header.
//
// The middleware emits:
//
//	HTTP/1.1 429 Too Many Requests
//	{
//	  "request_id": "<uuid>",
//	  "code":       "rate_limited",
//	  "message":    "rate limit exceeded"
//	}
func (rl *RateLimiter) Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		if IsRateBypass(c) {
			c.Next()
			return
		}

		key := rl.keyFn(c)
		lim := rl.getVisitor(key)

		if lim.Allow() {
			c.Next()
			return
		}

		c.Header("Retry-After", "1")
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
			"request_id": c.Writer.Header().Get("X-Request-ID"),
			"code":       "rate_limited",
			"message":    "rate limit exceeded",
		})
	}
}
