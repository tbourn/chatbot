package middleware

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

func TestKeyByUserOrIP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// Build a context with a known RemoteAddr
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// Ensure a deterministic IP for ClientIP()
	req.RemoteAddr = net.JoinHostPort("203.0.113.9", "12345")

	c, _ := gin.CreateTestContext(w)
	c.Request = req

	// IP fallback when no userID
	key := KeyByUserOrIP()(c)
	if !strings.HasPrefix(key, "ip:") || !strings.Contains(key, "203.0.113.9") {
		t.Fatalf("expected ip-based key; got %q", key)
	}

	// Prefer userID when present
	c.Set("userID", "u123")
	key2 := KeyByUserOrIP()(c)
	if key2 != "user:u123" {
		t.Fatalf("expected user-based key; got %q", key2)
	}
}

func TestNewRateLimiter_BurstCoercion_AndGetVisitorReuse(t *testing.T) {
	rl := NewRateLimiter(2.0, 0, KeyByUserOrIP()) // burst<=0 coerced to 1
	if rl.burst != 1 {
		t.Fatalf("burst coercion failed, got %d", rl.burst)
	}

	// First call creates limiter
	lim := rl.getVisitor("k1")
	if lim == nil {
		t.Fatalf("expected limiter")
	}
	// Second call reuses same limiter (pointer equality via map lookup)
	if got := rl.getVisitor("k1"); got != lim {
		t.Fatalf("expected same limiter instance to be reused")
	}
}

func TestRateLimiter_getVisitor_GC(t *testing.T) {
	rl := NewRateLimiter(1.0, 1, KeyByUserOrIP())
	// Make TTL immediate so anything old gets evicted
	rl.ttl = 1 * time.Nanosecond

	// Seed an old visitor
	rl.mu.Lock()
	rl.visitors["old"] = &visitor{
		limiter:  rate.NewLimiter(1, 1),
		lastSeen: time.Now().Add(-time.Hour),
	}
	// Force cleanup to run on next getVisitor by setting cleanupN to 4999
	rl.cleanupN = 4999
	rl.mu.Unlock()

	// Trigger cleanup by calling getVisitor for a different key
	_ = rl.getVisitor("new")

	rl.mu.Lock()
	_, existsOld := rl.visitors["old"]
	_, existsNew := rl.visitors["new"]
	rl.mu.Unlock()

	if existsOld {
		t.Fatalf("expected 'old' visitor to be evicted by opportunistic GC")
	}
	if !existsNew {
		t.Fatalf("expected 'new' visitor to be created")
	}
}

func TestIsRateBypass(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	// Default false
	if IsRateBypass(c) {
		t.Fatalf("expected IsRateBypass=false by default")
	}

	// Mark bypass (ctxKeyRateBypass is package-private; we’re in same package)
	c.Set(ctxKeyRateBypass, true)
	if !IsRateBypass(c) {
		t.Fatalf("expected IsRateBypass=true when set")
	}

	// Non-bool values shouldn’t panic, should read as false
	c.Set(ctxKeyRateBypass, "yes")
	if IsRateBypass(c) {
		t.Fatalf("expected IsRateBypass=false when non-bool stored")
	}
}

func TestRateLimiter_Handler_Allow_Deny_And_Bypass(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// rps=1, burst=1 -> first immediate request allowed, second denied
	rl := NewRateLimiter(1.0, 1, KeyByUserOrIP())

	// Router with only the rate limiter and a simple 200 handler
	r := gin.New()
	// Set a request-id header like our real stack would, so JSON has it (may be empty otherwise)
	r.Use(func(c *gin.Context) { c.Header("X-Request-ID", "rid-1"); c.Next() })
	r.Use(rl.Handler())
	r.GET("/ok", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	// First request (should be allowed)
	w1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodGet, "/ok", nil)
	r.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("first request should be allowed, got %d", w1.Code)
	}

	// Second immediate request (should be 429)
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/ok", nil)
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request should be rate-limited, got %d", w2.Code)
	}
	if got := w2.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("expected Retry-After=1, got %q", got)
	}
	var body map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON body: %v", err)
	}
	if body["code"] != "rate_limited" || body["message"] != "rate limit exceeded" {
		t.Fatalf("unexpected JSON body: %v", body)
	}

	// Bypass path: a pre-middleware flags the request; limiter should skip
	rBypass := gin.New()
	rBypass.Use(func(c *gin.Context) { c.Set(ctxKeyRateBypass, true); c.Next() })
	rBypass.Use(rl.Handler()) // reuse same rl: bypass must skip token checks
	rBypass.GET("/ok", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	w3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, "/ok", nil)
	rBypass.ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("bypass request should be allowed, got %d", w3.Code)
	}
}
