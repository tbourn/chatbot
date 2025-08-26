package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	sqlite "github.com/glebarez/sqlite"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/tbourn/go-chat-backend/internal/domain"
)

// optional: create a tiny DB so handlers in other middleware (if any) never explode
func newDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:redactlog?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	_ = db.AutoMigrate(&domain.Chat{}, &domain.Message{}, &domain.Feedback{})
	return db
}

func withCapturedLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Logger
	t.Cleanup(func() { log.Logger = prev })
	log.Logger = zerolog.New(&buf) // plain JSON lines
	return &buf
}

func TestRedactingLogger_InfoAndRedactions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	_ = newDB(t) // just to ensure no DB panics in other middlewares, not strictly required

	buf := withCapturedLogger(t)

	// Simulate upstream RequestID middleware that sets response header
	r.Use(func(c *gin.Context) {
		c.Header("X-Request-ID", "rid-resp")
		c.Next()
	})
	// Our logger with a custom masked header
	r.Use(RedactingLogger(RedactOptions{MaskHeaders: []string{"X-Api-Key"}}))

	// Route with params so c.FullPath() is non-empty
	r.GET("/users/:id", func(c *gin.Context) {
		// Return OK to produce "info" level
		c.String(http.StatusOK, "ok")
	})

	// Build a request containing PII in query and headers
	// Raw query is redacted with regex (no parsing), so simple occurrences are enough
	q := "email=a.b+tag@example.com&phone=+1-555-123-4567&id=123e4567-e89b-12d3-a456-426614174000"
	req := httptest.NewRequest(http.MethodGet, "/users/123?"+q, nil)
	// Built-in sensitive headers
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Cookie", "sid=topsecret")
	// Custom masked header
	req.Header.Set("X-Api-Key", "shhh")
	// Header with PII that should be pattern-redacted (not fully masked)
	req.Header.Set("X-Custom", "email a@b.com id=123e4567-e89b-12d3-a456-426614174000 phone 555-123-4567")
	// Also set a request header request-id; response header should still win
	req.Header.Set("X-Request-ID", "rid-req")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	logs := buf.String()
	// level info
	if !strings.Contains(logs, `"level":"info"`) {
		t.Fatalf("expected info log, got: %s", logs)
	}
	// path should be the route pattern
	if !strings.Contains(logs, `"path":"/users/:id"`) {
		t.Fatalf("expected path to use c.FullPath, got: %s", logs)
	}
	// request id prefers response header
	if !strings.Contains(logs, `"request_id":"rid-resp"`) {
		t.Fatalf("expected request_id from response header, got: %s", logs)
	}
	// query redactions
	if !strings.Contains(logs, `[REDACTED:email]`) || !strings.Contains(logs, `[REDACTED:phone]`) || !strings.Contains(logs, `[REDACTED:id]`) {
		t.Fatalf("expected query redactions, got: %s", logs)
	}
	// header masking for built-ins and custom
	if !strings.Contains(logs, `"Authorization":"[REDACTED]"`) {
		t.Fatalf("Authorization must be masked: %s", logs)
	}
	if !strings.Contains(logs, `"Cookie":"[REDACTED]"`) {
		t.Fatalf("Cookie must be masked: %s", logs)
	}
	if !strings.Contains(logs, `"X-Api-Key":"[REDACTED]"`) {
		t.Fatalf("X-Api-Key must be masked: %s", logs)
	}
	// pattern redactions inside non-masked header
	if !strings.Contains(logs, `"X-Custom":"email [REDACTED:email] id=[REDACTED:id] phone [REDACTED:phone]"`) {
		t.Fatalf("expected redacted X-Custom header, got: %s", logs)
	}
}

func TestRedactingLogger_WarnAndErrorLevels_RequestIDFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	buf := withCapturedLogger(t)

	// No response header X-Request-ID this time
	r.Use(RedactingLogger(RedactOptions{}))

	r.GET("/warn", func(c *gin.Context) { c.Status(http.StatusNotFound) })             // 404 -> warn
	r.GET("/error", func(c *gin.Context) { c.Status(http.StatusInternalServerError) }) // 500 -> error

	// Set only request header request-id; logger should fall back to it
	reqWarn := httptest.NewRequest(http.MethodGet, "/warn", nil)
	reqWarn.Header.Set("X-Request-ID", "rid-warn")
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, reqWarn)

	reqErr := httptest.NewRequest(http.MethodGet, "/error", nil)
	reqErr.Header.Set("X-Request-ID", "rid-err")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, reqErr)

	logs := buf.String()
	if !strings.Contains(logs, `"level":"warn"`) || !strings.Contains(logs, `"request_id":"rid-warn"`) {
		t.Fatalf("warn log not found or missing request_id fallback: %s", logs)
	}
	if !strings.Contains(logs, `"level":"error"`) || !strings.Contains(logs, `"request_id":"rid-err"`) {
		t.Fatalf("error log not found or missing request_id fallback: %s", logs)
	}
}
