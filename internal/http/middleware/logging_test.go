package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func captureLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Logger
	t.Cleanup(func() { log.Logger = prev })
	log.Logger = zerolog.New(&buf) // plain JSON lines
	return &buf
}

func TestRequestID_GenerateAndPropagate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID())
	r.GET("/rid", func(c *gin.Context) {
		if v, ok := c.Get(requestIDKey); !ok || v == "" {
			t.Fatalf("requestID not set in context")
		}
		c.String(http.StatusOK, "ok")
	})

	// No header -> generated
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/rid", nil)
	r.ServeHTTP(w, req)
	gen := w.Header().Get(requestIDHeader)
	if gen == "" {
		t.Fatalf("expected generated %s header", requestIDHeader)
	}

	// Lowercase header -> propagated
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/rid", nil)
	req2.Header.Set(strings.ToLower(requestIDHeader), "abc-123")
	r.ServeHTTP(w2, req2)
	if got := w2.Header().Get(requestIDHeader); got != "abc-123" {
		t.Fatalf("expected propagated request id, got %q", got)
	}
}

func TestLogger_InfoWarnErrorAndPathFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	buf := captureLogger(t)

	// Stack: RequestID then Logger
	r := gin.New()
	r.Use(RequestID())
	r.Use(Logger())

	// /ok → 200 → info; route pattern should be used in log
	r.GET("/ok", func(c *gin.Context) { c.String(http.StatusOK, "hello") })

	// /err → set an error on context and 400 → should log at error level (because len(c.Errors)>0)
	r.GET("/err", func(c *gin.Context) {
		_ = c.Error(errSentinel{}) // mark a gin error (custom type below)
		c.Status(http.StatusBadRequest)
	})

	// 1) /ok
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /ok -> %d", w.Code)
	}

	// 2) missing route -> 404 -> warn, and path fallback must be raw URL
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/missing", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET /missing -> %d", w.Code)
	}

	// 3) /err -> error level (because of c.Errors)
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/err", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("GET /err -> %d", w.Code)
	}

	logs := buf.String()
	if !strings.Contains(logs, `"level":"info"`) || !strings.Contains(logs, `"path":"/ok"`) {
		t.Fatalf("expected info log with route path, got:\n%s", logs)
	}
	if !strings.Contains(logs, `"level":"warn"`) || !strings.Contains(logs, `"path":"/missing"`) {
		t.Fatalf("expected warn log with raw path fallback, got:\n%s", logs)
	}
	if !strings.Contains(logs, `"level":"error"`) {
		t.Fatalf("expected error log, got:\n%s", logs)
	}
}

type errSentinel struct{}

func (e errSentinel) Error() string { return "boom" }

func TestRecovery_PanicsToJSON500AndLogs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	buf := captureLogger(t)

	r := gin.New()
	r.Use(RequestID())
	r.Use(Logger())
	r.Use(Recovery())

	r.GET("/panic", func(c *gin.Context) {
		panic("kaboom")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 from Recovery, got %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid json body: %v", err)
	}
	if body["code"] != "internal_error" || body["message"] != "internal server error" {
		t.Fatalf("unexpected body: %v", body)
	}
	// log should contain the panic marker and a stack
	out := buf.String()
	if !strings.Contains(out, `"panic recovered"`) && !strings.Contains(out, `"panic"`) {
		t.Fatalf("expected panic log, got:\n%s", out)
	}
}

func TestLoggerFrom_FallbackAndRequestScoped(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 1) Fallback, no Logger() installed
	buf1 := captureLogger(t)
	r1 := gin.New()
	r1.Use(RequestID())
	r1.GET("/use", func(c *gin.Context) {
		lg := LoggerFrom(c) // fallback logger (no request fields)
		lg.Info().Msg("custom")
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/use", nil)
	r1.ServeHTTP(w, req)
	if !strings.Contains(buf1.String(), `"message":"custom"`) {
		t.Fatalf("expected custom log in fallback")
	}
	// Should not include request_id (since no Logger() attached fields)
	if strings.Contains(buf1.String(), `"request_id"`) {
		t.Fatalf("fallback logger unexpectedly had request_id")
	}

	// 2) With Logger() installed – request-scoped logger carries request_id
	buf2 := captureLogger(t)
	r2 := gin.New()
	r2.Use(RequestID())
	r2.Use(Logger())
	r2.GET("/use", func(c *gin.Context) {
		lg := LoggerFrom(c) // request-scoped
		lg.Info().Msg("custom2")
		c.Status(http.StatusOK)
	})
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/use", nil)
	r2.ServeHTTP(w2, req2)
	out := buf2.String()
	if !strings.Contains(out, `"message":"custom2"`) {
		t.Fatalf("expected custom2 log present")
	}
	if !strings.Contains(out, `"request_id"`) {
		t.Fatalf("expected request-scoped logger to include request_id")
	}
}

func TestHelpers_asString_and_truncate(t *testing.T) {
	if asString("x") != "x" || asString(123) != "" {
		t.Fatalf("asString failed")
	}
	// truncate: no-op if <= max
	if truncate("hello", 10) != "hello" {
		t.Fatalf("truncate no-op failed")
	}
	// truncates and appends ellipsis
	got := truncate("abcdefgh", 5)
	if got != "abcde…" {
		t.Fatalf("truncate result = %q; want %q", got, "abcde…")
	}
	// max <= 0 disables truncation
	if truncate("abc", 0) != "abc" {
		t.Fatalf("truncate disable failed")
	}
}

func TestRequestID_UppercaseHeaderPropagates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID())
	r.GET("/rid", func(c *gin.Context) {
		v, _ := c.Get(requestIDKey)
		if v != "Z-REQ-123" {
			t.Fatalf("context requestID = %v; want Z-REQ-123", v)
		}
		c.Status(http.StatusNoContent)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/rid", nil)
	// Uppercase canonical header path
	req.Header.Set(requestIDHeader, "Z-REQ-123")
	r.ServeHTTP(w, req)

	if got := w.Header().Get(requestIDHeader); got != "Z-REQ-123" {
		t.Fatalf("response %s header = %q; want %q", requestIDHeader, got, "Z-REQ-123")
	}
}

func TestRecovery_PanicAfterWrite_NoJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	buf := captureLogger(t)

	r := gin.New()
	r.Use(RequestID())
	r.Use(Logger())
	r.Use(Recovery())

	// Write a response first, then panic -> exercises the branch where
	// c.Writer.Written() == true, so Recovery uses AbortWithStatus(500)
	// without writing the JSON body.
	r.GET("/panic-after-write", func(c *gin.Context) {
		c.String(http.StatusOK, "partial-body")
		panic("late kaboom")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/panic-after-write", nil)
	r.ServeHTTP(w, req)

	// We don't assert the final status code (Gin may have already flushed 200),
	// we just ensure we did NOT get the JSON error body.
	if strings.Contains(w.Body.String(), "internal error") || strings.Contains(strings.ToLower(w.Header().Get("Content-Type")), "application/json") {
		t.Fatalf("expected no JSON error body when panic after write; got CT=%q body=%q",
			w.Header().Get("Content-Type"), w.Body.String())
	}

	// And logs should contain the panic marker.
	if !strings.Contains(buf.String(), "panic recovered") && !strings.Contains(buf.String(), `"panic"`) {
		t.Fatalf("expected panic log, got:\n%s", buf.String())
	}
}
