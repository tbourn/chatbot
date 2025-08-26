package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
)

func Test_fail_500_LogsAndBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	// capture logs from LoggerFrom(c)
	var buf bytes.Buffer
	logger := zerolog.New(&buf)

	// simulate RequestID + request-scoped logger
	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("X-Request-ID", "rid-500")
		c.Set("logger", &logger)
		c.Next()
	})

	r.GET("/boom", func(c *gin.Context) {
		fail(c, http.StatusInternalServerError, "internal_error", "kaboom")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", w.Code)
	}

	var resp ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp.RequestID != "rid-500" || resp.Code != "internal_error" || resp.Message != "kaboom" {
		t.Fatalf("unexpected body: %+v", resp)
	}

	// ensure something was logged at error level
	if !strings.Contains(buf.String(), `"level":"error"`) {
		t.Fatalf("expected error log, got: %s", buf.String())
	}
}

func Test_Fail_404_And_SuccessHelpers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// set request id for envelope
	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("X-Request-ID", "rid-404")
		c.Next()
	})

	// exported Fail (4xx path)
	r.GET("/missing", func(c *gin.Context) {
		Fail(c, http.StatusNotFound, "not_found", "nope")
	})

	// ok helper
	r.GET("/ok", func(c *gin.Context) {
		ok(c, http.StatusCreated, gin.H{"ok": true, "n": 1})
	})

	// noContent helper
	r.DELETE("/gone", func(c *gin.Context) {
		noContent(c)
	})

	// 404
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d", w.Code)
	}
	var er ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &er); err != nil {
		t.Fatalf("json 404: %v", err)
	}
	if er.RequestID != "rid-404" || er.Code != "not_found" || er.Message != "nope" {
		t.Fatalf("unexpected 404 body: %+v", er)
	}

	// ok (201)
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/ok", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d", w.Code)
	}
	var okBody map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &okBody); err != nil {
		t.Fatalf("json 201: %v", err)
	}
	if okBody["ok"] != true || int(okBody["n"].(float64)) != 1 {
		t.Fatalf("unexpected ok body: %#v", okBody)
	}

	// noContent (204)
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/gone", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("expected empty body for 204")
	}
}
