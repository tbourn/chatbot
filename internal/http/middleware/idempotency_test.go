package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestHelpers_GetIdempotencyKey_IsReplay_UserIDFromCtx(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	// Not set
	if k, ok := GetIdempotencyKey(c); k != "" || ok {
		t.Fatalf("expected empty key when not set")
	}
	if IsReplay(c) {
		t.Fatalf("expected IsReplay=false by default")
	}

	// Set non-string for key → should return false
	c.Set(ctxKeyIdemKey, 123)
	if k, ok := GetIdempotencyKey(c); k != "" || !(!ok) {
		t.Fatalf("expected GetIdempotencyKey to be absent for non-string value")
	}
	// Set bool and check IsReplay=true
	c.Set(ctxKeyIdemReplay, true)
	if !IsReplay(c) {
		t.Fatalf("expected IsReplay=true")
	}
	// Non-bool value shouldn’t panic, should be false
	c.Set(ctxKeyIdemReplay, "yes")
	if IsReplay(c) {
		t.Fatalf("expected IsReplay=false for non-bool")
	}

	// userIDFromCtx fallback
	if got := userIDFromCtx(c); got != "demo-user" {
		t.Fatalf("userIDFromCtx fallback mismatch: %q", got)
	}
	c.Set("userID", "u1")
	if got := userIDFromCtx(c); got != "u1" {
		t.Fatalf("userIDFromCtx with userID mismatch: %q", got)
	}
	c.Set("userID", 42) // wrong type → fallback
	if got := userIDFromCtx(c); got != "demo-user" {
		t.Fatalf("userIDFromCtx wrong-type fallback mismatch: %q", got)
	}
}

func TestIdempotencyValidator_NoHeader_NoLookupCalled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	lookupCalled := false
	lookup := func(_ context.Context, _ string, _ string, _ string, _ time.Time) (bool, error) {
		lookupCalled = true
		return false, nil
	}
	r.Use(IdempotencyValidator(IdempotencyOptions{}, lookup))
	r.GET("/ping", func(c *gin.Context) {
		// header absent ⇒ no key stashed
		if _, ok := GetIdempotencyKey(c); ok {
			t.Fatalf("key should not be present when header missing")
		}
		c.Status(http.StatusNoContent)
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
	if lookupCalled {
		t.Fatalf("lookup should not be called when header missing")
	}
}

func TestIdempotencyValidator_InvalidKey_Length(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(IdempotencyValidator(IdempotencyOptions{MaxLen: 5}, nil)) // very small
	r.POST("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set(HeaderIdempotencyKey, "abcdef") // 6 > 5
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if body["code"] != "bad_idempotency_key" {
		t.Fatalf("unexpected body: %v", body)
	}
}

func TestIdempotencyValidator_InvalidKey_Pattern(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// only digits allowed → alpha will fail
	r.Use(IdempotencyValidator(IdempotencyOptions{Pattern: regexp.MustCompile(`^[0-9]+$`)}, nil))
	r.POST("/y", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/y", nil)
	req.Header.Set(HeaderIdempotencyKey, "abc123") // invalid
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestIdempotencyValidator_Valid_NoLookup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// MaxLen <= 0 triggers default 200, Pattern nil triggers default regex
	r.Use(IdempotencyValidator(IdempotencyOptions{}, nil))
	r.POST("/z", func(c *gin.Context) {
		key, ok := GetIdempotencyKey(c)
		if !ok || key != "abc-123" {
			t.Fatalf("expected stashed key abc-123, got %q ok=%v", key, ok)
		}
		if IsReplay(c) {
			t.Fatalf("expected IsReplay=false when lookup=nil")
		}
		if IsRateBypass(c) {
			t.Fatalf("expected IsRateBypass=false when lookup=nil")
		}
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/z", nil)
	req.Header.Set(HeaderIdempotencyKey, "abc-123") // matches default pattern
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestIdempotencyValidator_Valid_WithLookup_MissAndHit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("lookup miss", func(t *testing.T) {
		r := gin.New()
		lookup := func(_ context.Context, userID, chatID, key string, now time.Time) (bool, error) {
			if userID == "" || key == "" || now.IsZero() {
				t.Fatalf("lookup args not populated: uid=%q key=%q now=%v", userID, key, now)
			}
			// When no user set in context, userIDFromCtx returns "demo-user"
			if userID != "demo-user" {
				t.Fatalf("expected demo-user fallback, got %q", userID)
			}
			if chatID != "c42" {
				t.Fatalf("expected chatID path param c42, got %q", chatID)
			}
			return false, nil
		}
		r.Use(IdempotencyValidator(IdempotencyOptions{}, lookup))
		r.POST("/chats/:id/messages", func(c *gin.Context) {
			if IsReplay(c) || IsRateBypass(c) {
				t.Fatalf("expected no replay/bypass on miss")
			}
			c.Status(http.StatusOK)
		})

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/chats/c42/messages", nil)
		req.Header.Set(HeaderIdempotencyKey, "key-1")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("miss: expected 200, got %d", w.Code)
		}
	})

	t.Run("lookup hit sets replay and bypass, passes user id", func(t *testing.T) {
		r := gin.New()
		// inject user before idempotency middleware
		r.Use(func(c *gin.Context) { c.Set("userID", "u9"); c.Next() })
		lookup := func(_ context.Context, userID, chatID, key string, _ time.Time) (bool, error) {
			if userID != "u9" {
				t.Fatalf("expected userID u9, got %q", userID)
			}
			if chatID != "abc" || key != "k-9" {
				t.Fatalf("unexpected chatID/key: %q %q", chatID, key)
			}
			return true, nil
		}
		r.Use(IdempotencyValidator(IdempotencyOptions{}, lookup))
		r.POST("/chats/:id/messages", func(c *gin.Context) {
			if !IsReplay(c) {
				t.Fatalf("expected IsReplay=true on hit")
			}
			if !IsRateBypass(c) {
				t.Fatalf("expected IsRateBypass=true on hit")
			}
			c.Status(http.StatusOK)
		})

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/chats/abc/messages", nil)
		req.Header.Set(HeaderIdempotencyKey, "k-9")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("hit: expected 200, got %d", w.Code)
		}
	})
}
