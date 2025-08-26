package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMetrics_Counters_Histograms_InflightAndPathFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(Metrics())

	// Route with body → positive size (observed)
	r.GET("/ok", func(c *gin.Context) {
		c.String(http.StatusOK, "hello") // writes body (size >= 0)
	})

	// Route with status only → size stays -1 (skipped in size histogram)
	r.GET("/statusonly", func(c *gin.Context) {
		c.Status(http.StatusNoContent) // 204, no body => size -1
	})

	// Baselines before we hit the routes (to avoid interference from other tests)
	baseOK := testutil.ToFloat64(httpReqs.WithLabelValues("GET", "/ok", "200"))
	base404 := testutil.ToFloat64(httpReqs.WithLabelValues("GET", "/does-not-exist", "404"))

	// 1) Hit /ok (matches route → path label is "/ok")
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /ok -> %d", w.Code)
	}

	// 2) Hit a missing route (no match → fallback to raw URL path label)
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET /does-not-exist -> %d", w.Code)
	}

	// 3) Hit /statusonly (size -1 path executed)
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/statusonly", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("GET /statusonly -> %d", w.Code)
	}

	// --- Assertions ---

	// Counters for specific label sets should have incremented by 1
	gotOK := testutil.ToFloat64(httpReqs.WithLabelValues("GET", "/ok", "200"))
	if gotOK != baseOK+1 {
		t.Fatalf("counter /ok 200 = %v; want %v", gotOK, baseOK+1)
	}

	// 404 path uses raw URL (fallback)
	got404 := testutil.ToFloat64(httpReqs.WithLabelValues("GET", "/does-not-exist", "404"))
	if got404 != base404+1 {
		t.Fatalf("counter 404 fallback = %v; want %v", got404, base404+1)
	}

	// In-flight gauge should be 0 after requests complete
	if inFlight := testutil.ToFloat64(httpInflight); inFlight != 0 {
		t.Fatalf("httpInflight = %v; want 0", inFlight)
	}

	// We don't assert exact histogram bucket counts (they’re timing-dependent),
	// but by executing the code paths above we hit both:
	// - httpLat.WithLabelValues(method, path).Observe(...)
	// - httpRespSize.WithLabelValues(method, path).Observe(...) when size>=0
	// and skip when size<0.
}
