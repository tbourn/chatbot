package middleware

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestSecurityHeaders_Baseline_And_ExposeHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("baseline headers + add expose when X-Request-ID present", func(t *testing.T) {
		r := gin.New()
		// pre-middleware sets the request-id header (like a real RequestID mw would)
		r.Use(func(c *gin.Context) {
			c.Header("X-Request-ID", "rid-123")
			c.Next()
		})
		r.Use(SecurityHeaders(SecurityOptions{
			EnableHSTS:   false, // disabled
			HSTSMaxAge:   0,     // triggers default maxAge branch (180d) even if unused
			NoStore:      false, // no cache headers
			EnablePolicy: false, // no policy headers
		}))
		r.GET("/ok", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/ok", nil)
		r.ServeHTTP(w, req)

		h := w.Header()
		// baseline
		if h.Get("X-Content-Type-Options") != "nosniff" ||
			h.Get("X-Frame-Options") != "DENY" ||
			h.Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("baseline headers missing: %#v", h)
		}
		// no optional headers
		if h.Get("Permissions-Policy") != "" || h.Get("X-Permitted-Cross-Domain-Policies") != "" {
			t.Fatalf("unexpected policy headers: %#v", h)
		}
		if h.Get("Cache-Control") != "" || h.Get("Pragma") != "" || h.Get("Expires") != "" {
			t.Fatalf("unexpected cache headers: %#v", h)
		}
		if h.Get("Strict-Transport-Security") != "" {
			t.Fatalf("unexpected HSTS: %#v", h)
		}
		// expose request id should be added
		if h.Get("Access-Control-Expose-Headers") != "X-Request-ID" {
			t.Fatalf("expected Access-Control-Expose-Headers=X-Request-ID, got %q", h.Get("Access-Control-Expose-Headers"))
		}
	})

	t.Run("append X-Request-ID to existing expose header", func(t *testing.T) {
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Header("X-Request-ID", "rid-abc")
			c.Header("Access-Control-Expose-Headers", "Foo")
			c.Next()
		})
		r.Use(SecurityHeaders(SecurityOptions{}))
		r.GET("/ok", func(c *gin.Context) { c.Status(http.StatusOK) })

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/ok", nil)
		r.ServeHTTP(w, req)

		got := w.Header().Get("Access-Control-Expose-Headers")
		if got != "Foo, X-Request-ID" {
			t.Fatalf("expected 'Foo, X-Request-ID', got %q", got)
		}
	})

	t.Run("do not duplicate X-Request-ID in expose header", func(t *testing.T) {
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Header("X-Request-ID", "rid-xyz")
			c.Header("Access-Control-Expose-Headers", "X-Request-ID, Foo")
			c.Next()
		})
		r.Use(SecurityHeaders(SecurityOptions{}))
		r.GET("/ok", func(c *gin.Context) { c.Status(http.StatusOK) })

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/ok", nil)
		r.ServeHTTP(w, req)

		got := w.Header().Get("Access-Control-Expose-Headers")
		if got != "X-Request-ID, Foo" {
			t.Fatalf("expected unchanged expose header, got %q", got)
		}
	})
}

func TestSecurityHeaders_WithPolicy_NoStore_HSTS_TLS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(SecurityHeaders(SecurityOptions{
		EnableHSTS:   true,
		HSTSMaxAge:   24 * time.Hour, // 86400
		NoStore:      true,
		EnablePolicy: true,
	}))
	r.GET("/ok", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	// simulate HTTPS via TLS
	req.TLS = &tls.ConnectionState{}
	r.ServeHTTP(w, req)

	h := w.Header()
	// policy headers
	if h.Get("Permissions-Policy") == "" || h.Get("X-Permitted-Cross-Domain-Policies") != "none" {
		t.Fatalf("missing policy headers: %#v", h)
	}
	// cache headers
	if h.Get("Cache-Control") != "no-store" || h.Get("Pragma") != "no-cache" || h.Get("Expires") != "0" {
		t.Fatalf("missing cache headers: %#v", h)
	}
	// HSTS
	wantHSTS := "max-age=86400; includeSubDomains; preload"
	if h.Get("Strict-Transport-Security") != wantHSTS {
		t.Fatalf("expected HSTS %q, got %q", wantHSTS, h.Get("Strict-Transport-Security"))
	}
}

func TestSecurityHeaders_HSTS_XForwardedProto(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(SecurityHeaders(SecurityOptions{
		EnableHSTS: true,
		HSTSMaxAge: time.Hour,
	}))
	r.GET("/ok", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	// simulate HTTPS via proxy header
	req.Header.Set("X-Forwarded-Proto", "https")
	r.ServeHTTP(w, req)

	if got := w.Header().Get("Strict-Transport-Security"); !strings.HasPrefix(got, "max-age=") {
		t.Fatalf("expected HSTS header, got %q", got)
	}
}

func Test_isHTTPS(t *testing.T) {
	// http
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if isHTTPS(req) {
		t.Fatalf("plain HTTP should not be https")
	}
	// via TLS
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.TLS = &tls.ConnectionState{}
	if !isHTTPS(req2) {
		t.Fatalf("TLS request should be https")
	}
	// via header
	req3 := httptest.NewRequest(http.MethodGet, "/", nil)
	req3.Header.Set("X-Forwarded-Proto", "https")
	if !isHTTPS(req3) {
		t.Fatalf("X-Forwarded-Proto=https should be https")
	}
}

func Test_itoa_and_strconvItoa(t *testing.T) {
	// zero
	if itoa(0) != "0" {
		t.Fatalf("itoa(0) != '0'")
	}
	// positives and negatives
	vals := []int{1, 9, 10, 42, 1234567890, -1, -42}
	for _, v := range vals {
		if itoa(v) != strconv.Itoa(v) {
			t.Fatalf("itoa(%d) mismatch: got %q want %q", v, itoa(v), strconv.Itoa(v))
		}
	}
}
