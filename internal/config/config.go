// Package config provides application configuration loaded from environment
// variables with defaults and validation. It centralizes application settings
// such as server timeouts, logging, database paths, rate limiting, and observability.
package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

// CORSConfig defines Cross-Origin Resource Sharing settings.
type CORSConfig struct {
	AllowedOrigins []string
}

// SecurityConfig defines security-related settings such as HSTS.
type SecurityConfig struct {
	EnableHSTS bool
	HSTSMaxAge time.Duration
}

// OTELConfig defines OpenTelemetry observability settings.
type OTELConfig struct {
	Enabled     bool    // OTEL_ENABLED
	Endpoint    string  // OTEL_EXPORTER_OTLP_ENDPOINT (e.g. "otel:4317")
	Insecure    bool    // OTEL_EXPORTER_OTLP_INSECURE (true if no TLS)
	ServiceName string  // OTEL_SERVICE_NAME (e.g. "go-chat-backend")
	SampleRatio float64 // OTEL_TRACES_SAMPLER_ARG in [0..1]
}

// Config holds all configuration values for the application.
type Config struct {
	// Server
	Port              string        // just the number
	ReadTimeout       time.Duration // e.g. 15s
	ReadHeaderTimeout time.Duration // e.g. 10s
	WriteTimeout      time.Duration // e.g. 20s
	IdleTimeout       time.Duration // e.g. 60s
	MaxHeaderBytes    int           // bytes
	GinMode           string        // debug|release|test

	// Logging / Docs
	LogLevel       string // debug|info|warn|error|fatal|panic
	LogPretty      bool   // pretty console logs in dev
	SwaggerEnabled bool   // enable Swagger UI route
	APIBasePath    string // base path for API routes

	// App
	DBPath    string  // SQLite path
	DataPath  string  // default path to data.md
	DataMD    string  // optional override for DataPath
	Threshold float64 // retrieval confidence threshold [0,1]

	// Rate limiting
	RateRPS   float64 // tokens per second (>= 0)
	RateBurst int     // bucket size (>= 1)

	// Web protection
	CORS     CORSConfig
	Security SecurityConfig

	// Idempotency
	IdempotencyTTL time.Duration // how long a given Idempotency-Key is valid

	// Observability
	OTEL OTELConfig
}

// MustLoad loads the configuration and panics if validation fails.
func MustLoad() Config {
	cfg, err := Load()
	if err != nil {
		panic(err)
	}
	return cfg
}

// Load reads configuration from environment variables,
// applies defaults, normalizes values, and validates the result.
func Load() (Config, error) {
	cfg := Config{
		// Server
		Port:              getenv("PORT", "8080"),
		ReadTimeout:       getdur("READ_TIMEOUT", 15*time.Second),
		ReadHeaderTimeout: getdur("READ_HEADER_TIMEOUT", 10*time.Second),
		WriteTimeout:      getdur("WRITE_TIMEOUT", 20*time.Second),
		IdleTimeout:       getdur("IDLE_TIMEOUT", 60*time.Second),
		MaxHeaderBytes:    getint("MAX_HEADER_BYTES", 1<<20),
		GinMode:           strings.ToLower(getenv("GIN_MODE", "release")),

		// Logging / Docs
		LogLevel:       strings.ToLower(getenv("LOG_LEVEL", "info")),
		LogPretty:      getbool("LOG_PRETTY", false),
		SwaggerEnabled: getbool("SWAGGER_ENABLED", false),
		APIBasePath:    normalizeBasePath(getenv("API_BASE_PATH", "/api/v1")),

		// App
		DBPath:    getenv("DB_PATH", "app.db"),
		DataPath:  getenv("DATA_PATH", "data/data.md"),
		DataMD:    getenv("DATA_MD", ""),
		Threshold: getfloat("THRESHOLD", 0.32),

		// Rate limiting
		RateRPS:   getfloat("RATE_RPS", 5.0),
		RateBurst: getint("RATE_BURST", 10),

		// Web protection
		CORS: CORSConfig{
			AllowedOrigins: splitCSV(getenv("CORS_ALLOWED_ORIGINS", "")),
		},
		Security: SecurityConfig{
			EnableHSTS: getbool("ENABLE_HSTS", false),
			HSTSMaxAge: getdur("HSTS_MAX_AGE", 180*24*time.Hour),
		},

		// Idempotency
		IdempotencyTTL: getdur("IDEMPOTENCY_TTL", 24*time.Hour),

		// Observability (OpenTelemetry)
		OTEL: OTELConfig{
			Enabled:     getbool("OTEL_ENABLED", false),
			Endpoint:    getenv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
			Insecure:    getbool("OTEL_EXPORTER_OTLP_INSECURE", true),
			ServiceName: getenv("OTEL_SERVICE_NAME", "go-chat-backend"),
			SampleRatio: getfloat("OTEL_TRACES_SAMPLER_ARG", 1.0),
		},
	}

	// --- normalization ---
	if cfg.LogLevel == "warning" {
		cfg.LogLevel = "warn"
	}
	switch cfg.GinMode {
	case "debug", "release", "test":
	default:
		cfg.GinMode = "release"
	}

	// --- validation ---
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error", "fatal", "panic":
	default:
		return cfg, errors.New("LOG_LEVEL must be one of: debug, info, warn, error, fatal, panic")
	}
	if strings.TrimSpace(cfg.Port) == "" {
		return cfg, errors.New("PORT must not be empty")
	}
	if cfg.ReadTimeout <= 0 || cfg.ReadHeaderTimeout <= 0 || cfg.WriteTimeout <= 0 || cfg.IdleTimeout <= 0 {
		return cfg, errors.New("timeouts must be positive durations")
	}
	if cfg.MaxHeaderBytes <= 0 {
		return cfg, errors.New("MAX_HEADER_BYTES must be > 0")
	}
	if strings.TrimSpace(cfg.DBPath) == "" {
		return cfg, errors.New("DB_PATH must not be empty")
	}
	if strings.TrimSpace(cfg.DataPath) == "" {
		return cfg, errors.New("DATA_PATH must not be empty")
	}
	if cfg.Threshold < 0 || cfg.Threshold > 1 {
		return cfg, errors.New("THRESHOLD must be between 0 and 1")
	}
	if cfg.RateRPS < 0 {
		return cfg, errors.New("RATE_RPS must be >= 0")
	}
	if cfg.RateBurst < 1 {
		return cfg, errors.New("RATE_BURST must be >= 1")
	}
	if cfg.Security.HSTSMaxAge < 0 {
		return cfg, errors.New("HSTS_MAX_AGE must be >= 0")
	}
	if cfg.IdempotencyTTL <= 0 {
		return cfg, errors.New("IDEMPOTENCY_TTL must be > 0")
	}
	if cfg.OTEL.SampleRatio < 0 || cfg.OTEL.SampleRatio > 1 {
		return cfg, errors.New("OTEL_TRACES_SAMPLER_ARG must be in [0,1]")
	}
	// if cfg.APIBasePath == "" || cfg.APIBasePath[0] != '/' {
	// 	return cfg, errors.New("API_BASE_PATH must start with '/'")
	// }

	return cfg, nil
}

// ---- helpers (no external deps) ----

func getenv(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func getfloat(k string, def float64) float64 {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func getint(k string, def int) int {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

func getbool(k string, def bool) bool {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "y", "on":
			return true
		case "0", "false", "no", "n", "off":
			return false
		}
	}
	return def
}

func getdur(k string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// normalizeBasePath ensures leading '/' and strips trailing '/' (except root).
func normalizeBasePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if len(p) > 1 && strings.HasSuffix(p, "/") {
		p = strings.TrimRight(p, "/")
	}
	return p
}
