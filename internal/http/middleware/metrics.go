// Package middleware contains shared Gin middleware used by the HTTP layer.
//
// This file exposes Prometheus instrumentation for HTTP traffic. The Metrics()
// middleware measures request counts, latencies, in-flight concurrency, and
// response sizes with careful attention to label cardinality:
//
//   - method:   HTTP method verb (GET/POST/…)
//   - path:     the registered Gin route (e.g. /api/v1/chats/:id/messages);
//     falls back to the raw URL path when no route matched
//   - status:   numeric status code as a string (e.g. "200", "404")
//
// The chosen labels keep cardinality bounded while remaining actionable in
// dashboards and SLOs. All collectors are safe for concurrent use.
package middleware

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// httpReqs counts requests by method, route path, and status code.
	httpReqs = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests.",
		},
		[]string{"method", "path", "status"},
	)

	// httpLat records request duration in seconds by method and route path.
	// We intentionally omit status to keep latency histogram cardinality lower.
	httpLat = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "Duration of HTTP requests in seconds.",
			Buckets: prometheus.DefBuckets, // suitable for general HTTP latency
		},
		[]string{"method", "path"},
	)

	// httpInflight gauges the number of in-flight (currently processing) requests.
	httpInflight = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "http_requests_inflight",
			Help: "Current number of in-flight HTTP requests.",
		},
	)

	// httpRespSize captures response sizes in bytes by method and route path.
	// Buckets are tuned for typical JSON API payload sizes.
	httpRespSize = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "http_response_size_bytes",
			Help: "Size of HTTP responses in bytes.",
			Buckets: []float64{
				200, 500, 1 << 10, 2 << 10, 5 << 10, // 200B..5KiB
				10 << 10, 25 << 10, 50 << 10, // 10..50KiB
				100 << 10, 250 << 10, 500 << 10, // 100..500KiB
				1 << 20, 2 << 20, 5 << 20, // 1..5MiB
			},
		},
		[]string{"method", "path"},
	)
)

func init() {
	prometheus.MustRegister(httpReqs, httpLat, httpInflight, httpRespSize)
}

// Metrics returns a Gin middleware that instruments requests with Prometheus.
//
// Usage:
//
//	r := gin.New()
//	r.Use(middleware.Metrics())
//	r.GET("/metrics", gin.WrapH(promhttp.Handler()))
//
// Semantics:
//   - Increments http_requests_total(method, path, status) per request
//   - Observes http_request_duration_seconds(method, path) on completion
//   - Tracks http_requests_inflight gauge during handler execution
//   - Observes http_response_size_bytes(method, path) with bytes written
//
// Notes:
//   - The "path" label uses the registered route (c.FullPath()) to avoid
//     unbounded label cardinality from raw URLs. If no route matched (e.g. 404),
//     it falls back to c.Request.URL.Path.
//   - The status label is the numeric code string (e.g., "200"), which is easy
//     to aggregate in PromQL (e.g., sum by (status)).
func Metrics() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		httpInflight.Inc()
		defer httpInflight.Dec()

		c.Next()

		dur := time.Since(start).Seconds()
		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}
		method := c.Request.Method
		status := strconv.Itoa(c.Writer.Status())
		size := c.Writer.Size() // -1 when unknown

		httpReqs.WithLabelValues(method, path, status).Inc()
		httpLat.WithLabelValues(method, path).Observe(dur)
		if size >= 0 {
			httpRespSize.WithLabelValues(method, path).Observe(float64(size))
		} else {
			// Some handlers (e.g., hijacked connections) may not report size;
			// we skip recording a negative value.
		}
	}
}
