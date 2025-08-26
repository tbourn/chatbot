package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// --- MustLoad ---

func TestMustLoad_PanicsOnInvalidConfig(t *testing.T) {
	t.Setenv("LOG_LEVEL", "verbose") // invalid -> Load() error
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("MustLoad should panic on invalid config")
		}
	}()
	_ = MustLoad()
}

// --- Load success + normalization + parsing ---

func TestLoad_Success_DefaultsAndOverrides(t *testing.T) {
	// Clear all env that might affect defaults. t.Setenv isolates per test.
	// Server timeouts / sizes (valid)
	t.Setenv("PORT", "8088")
	t.Setenv("READ_TIMEOUT", "2s")
	t.Setenv("READ_HEADER_TIMEOUT", "1s")
	t.Setenv("WRITE_TIMEOUT", "3s")
	t.Setenv("IDLE_TIMEOUT", "4s")
	t.Setenv("MAX_HEADER_BYTES", "8192")
	t.Setenv("GIN_MODE", "weird") // will normalize to "release"

	// Logging / Docs
	t.Setenv("LOG_LEVEL", "warning") // will normalize to "warn"
	t.Setenv("LOG_PRETTY", "yes")
	t.Setenv("SWAGGER_ENABLED", "on")
	t.Setenv("API_BASE_PATH", "api/v1/") // no leading slash + trailing slash -> "/api/v1"

	// App
	t.Setenv("DB_PATH", "db.sqlite")
	t.Setenv("DATA_PATH", "data.md")
	t.Setenv("DATA_MD", "override.md")
	t.Setenv("THRESHOLD", "0.5")

	// Rate limiting (use invalids for parse to fall back to defaults)
	t.Setenv("RATE_RPS", "x")      // -> default 5.0
	t.Setenv("RATE_BURST", "nope") // -> default 10

	// Web protection
	t.Setenv("CORS_ALLOWED_ORIGINS", " https://a.com , , http://b ")
	t.Setenv("ENABLE_HSTS", "TRUE")
	t.Setenv("HSTS_MAX_AGE", "24h")

	// Idempotency
	t.Setenv("IDEMPOTENCY_TTL", "48h")

	// OTEL
	t.Setenv("OTEL_ENABLED", "1")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "otel:4317")
	t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "0")
	t.Setenv("OTEL_SERVICE_NAME", "svc")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0.75")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// Server
	if cfg.Port != "8088" ||
		cfg.ReadTimeout != 2*time.Second ||
		cfg.ReadHeaderTimeout != 1*time.Second ||
		cfg.WriteTimeout != 3*time.Second ||
		cfg.IdleTimeout != 4*time.Second ||
		cfg.MaxHeaderBytes != 8192 ||
		cfg.GinMode != "release" {
		t.Fatalf("server fields unexpected: %+v", cfg)
	}

	// Logging / Docs
	if cfg.LogLevel != "warn" || !cfg.LogPretty || !cfg.SwaggerEnabled || cfg.APIBasePath != "/api/v1" {
		t.Fatalf("logging/docs unexpected: %+v", cfg)
	}

	// App
	if cfg.DBPath != "db.sqlite" || cfg.DataPath != "data.md" || cfg.DataMD != "override.md" || cfg.Threshold != 0.5 {
		t.Fatalf("app fields unexpected: %+v", cfg)
	}

	// Rate limiting (parse fallback to defaults)
	if cfg.RateRPS != 5.0 || cfg.RateBurst != 10 {
		t.Fatalf("rate limiting unexpected: %+v", cfg)
	}

	// Web protection
	if !reflect.DeepEqual(cfg.CORS.AllowedOrigins, []string{"https://a.com", "http://b"}) {
		t.Fatalf("cors origins unexpected: %#v", cfg.CORS.AllowedOrigins)
	}
	if !cfg.Security.EnableHSTS || cfg.Security.HSTSMaxAge != 24*time.Hour {
		t.Fatalf("security unexpected: %+v", cfg.Security)
	}

	// Idempotency
	if cfg.IdempotencyTTL != 48*time.Hour {
		t.Fatalf("idempotency ttl unexpected: %v", cfg.IdempotencyTTL)
	}

	// OTEL
	if !cfg.OTEL.Enabled || cfg.OTEL.Endpoint != "otel:4317" || cfg.OTEL.Insecure || cfg.OTEL.ServiceName != "svc" || cfg.OTEL.SampleRatio != 0.75 {
		t.Fatalf("otel unexpected: %+v", cfg.OTEL)
	}
}

// --- Load validations (each case triggers exactly one validation error) ---

func TestLoad_ValidationErrors(t *testing.T) {
	t.Run("invalid LOG_LEVEL", func(t *testing.T) {
		t.Setenv("LOG_LEVEL", "verbose")
		if _, err := Load(); err == nil {
			t.Fatalf("expected LOG_LEVEL validation error")
		}
	})
	t.Run("empty PORT via spaces", func(t *testing.T) {
		t.Setenv("PORT", "   ")
		if _, err := Load(); err == nil || !containsErr(err, "PORT must not be empty") {
			t.Fatalf("expected port validation error, got: %v", err)
		}
	})
	t.Run("non-positive timeouts", func(t *testing.T) {
		t.Setenv("READ_TIMEOUT", "0s")
		if _, err := Load(); err == nil || !containsErr(err, "timeouts must be positive") {
			t.Fatalf("expected timeouts validation error, got: %v", err)
		}
	})
	t.Run("max header bytes <= 0", func(t *testing.T) {
		t.Setenv("MAX_HEADER_BYTES", "0")
		if _, err := Load(); err == nil || !containsErr(err, "MAX_HEADER_BYTES") {
			t.Fatalf("expected MAX_HEADER_BYTES validation error, got: %v", err)
		}
	})
	t.Run("empty DB_PATH", func(t *testing.T) {
		t.Setenv("DB_PATH", "   ")
		if _, err := Load(); err == nil || !containsErr(err, "DB_PATH must not be empty") {
			t.Fatalf("expected DB_PATH validation error, got: %v", err)
		}
	})
	t.Run("empty DATA_PATH", func(t *testing.T) {
		t.Setenv("DATA_PATH", "   ")
		if _, err := Load(); err == nil || !containsErr(err, "DATA_PATH must not be empty") {
			t.Fatalf("expected DATA_PATH validation error, got: %v", err)
		}
	})
	t.Run("threshold out of range", func(t *testing.T) {
		t.Setenv("THRESHOLD", "1.5")
		if _, err := Load(); err == nil || !containsErr(err, "THRESHOLD") {
			t.Fatalf("expected THRESHOLD validation error, got: %v", err)
		}
	})
	t.Run("rate rps negative", func(t *testing.T) {
		t.Setenv("RATE_RPS", "-1")
		if _, err := Load(); err == nil || !containsErr(err, "RATE_RPS") {
			t.Fatalf("expected RATE_RPS validation error, got: %v", err)
		}
	})
	t.Run("rate burst < 1", func(t *testing.T) {
		t.Setenv("RATE_BURST", "0")
		if _, err := Load(); err == nil || !containsErr(err, "RATE_BURST") {
			t.Fatalf("expected RATE_BURST validation error, got: %v", err)
		}
	})
	t.Run("hsts max age negative", func(t *testing.T) {
		t.Setenv("HSTS_MAX_AGE", "-1s")
		if _, err := Load(); err == nil || !containsErr(err, "HSTS_MAX_AGE") {
			t.Fatalf("expected HSTS_MAX_AGE validation error, got: %v", err)
		}
	})
	t.Run("idempotency ttl non-positive", func(t *testing.T) {
		t.Setenv("IDEMPOTENCY_TTL", "0s")
		if _, err := Load(); err == nil || !containsErr(err, "IDEMPOTENCY_TTL") {
			t.Fatalf("expected IDEMPOTENCY_TTL validation error, got: %v", err)
		}
	})
	t.Run("otel sample ratio out of range", func(t *testing.T) {
		t.Setenv("OTEL_TRACES_SAMPLER_ARG", "1.5")
		if _, err := Load(); err == nil || !containsErr(err, "OTEL_TRACES_SAMPLER_ARG") {
			t.Fatalf("expected OTEL_TRACES_SAMPLER_ARG validation error, got: %v", err)
		}
	})

	// Note: API_BASE_PATH validation is effectively unreachable due to normalizeBasePath
	// always ensuring a leading '/' and returning "/" for empty input.
}

// --- helpers ---

func TestHelpers_getenv(t *testing.T) {
	t.Setenv("X_EMPTY", "")
	if getenv("X_EMPTY", "d") != "d" {
		t.Fatalf("getenv should fall back to default on empty var")
	}
	t.Setenv("X_SET", "val")
	if getenv("X_SET", "d") != "val" {
		t.Fatalf("getenv should read set value")
	}
}

func TestHelpers_getfloat_getint_getdur(t *testing.T) {
	t.Setenv("F_VALID", "3.14")
	if getfloat("F_VALID", 0) != 3.14 {
		t.Fatalf("getfloat parse failed")
	}
	t.Setenv("F_BAD", "nope")
	if getfloat("F_BAD", 1.23) != 1.23 {
		t.Fatalf("getfloat default on bad parse failed")
	}

	t.Setenv("I_VALID", "42")
	if getint("I_VALID", 0) != 42 {
		t.Fatalf("getint parse failed")
	}
	t.Setenv("I_BAD", "x")
	if getint("I_BAD", 7) != 7 {
		t.Fatalf("getint default on bad parse failed")
	}

	t.Setenv("D_VALID", "150ms")
	if getdur("D_VALID", time.Second) != 150*time.Millisecond {
		t.Fatalf("getdur parse failed")
	}
	t.Setenv("D_BAD", "zzz")
	if getdur("D_BAD", 2*time.Second) != 2*time.Second {
		t.Fatalf("getdur default on bad parse failed")
	}
}

func TestHelpers_getbool(t *testing.T) {
	trueVals := []string{"1", "true", "TRUE", " yes ", "Y", "on", "On"}
	for i, v := range trueVals {
		k := "B_T_" + config_strconv(i)
		t.Setenv(k, v)
		if !getbool(k, false) {
			t.Fatalf("getbool(%q) = false; want true", v)
		}
	}
	falseVals := []string{"0", "false", "FALSE", " no ", "N", "off", "Off"}
	for i, v := range falseVals {
		k := "B_F_" + config_strconv(i)
		t.Setenv(k, v)
		if getbool(k, true) {
			t.Fatalf("getbool(%q) = true; want false", v)
		}
	}
	// default on unset/empty
	t.Setenv("B_EMPTY", "")
	if !getbool("B_EMPTY", true) || getbool("B_EMPTY", false) {
		t.Fatalf("getbool default behavior unexpected")
	}
}

func TestHelpers_splitCSV_and_normalizeBasePath(t *testing.T) {
	if out := splitCSV(""); out != nil {
		t.Fatalf("splitCSV empty should return nil")
	}
	in := " a, ,b ,  c  ,"
	want := []string{"a", "b", "c"}
	if got := splitCSV(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("splitCSV mismatch: got %#v want %#v", got, want)
	}

	// normalizeBasePath
	if normalizeBasePath("") != "/" {
		t.Fatalf("normalizeBasePath empty -> '/' failed")
	}
	if normalizeBasePath("v1") != "/v1" {
		t.Fatalf("normalizeBasePath missing leading slash failed")
	}
	if normalizeBasePath("/v1/") != "/v1" {
		t.Fatalf("normalizeBasePath trailing slash trim failed")
	}
	if normalizeBasePath(" / ") != "/" {
		t.Fatalf("normalizeBasePath whitespace failed")
	}
}

// small helper (avoid fmt just for ints)
func config_strconv(i int) string { return string('a' + rune(i)) }

// Ensure tests don’t leak env to others.
func TestMain(m *testing.M) {
	os.Unsetenv("PORT")
	os.Exit(m.Run())
}

// containsErr reports whether err's message contains the given substring.
func containsErr(err error, want string) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), want)
}

func TestLoad_Defaults_APIBasePathDefault_And_DataMDOptional(t *testing.T) {
	t.Setenv("DB_PATH", "db.sqlite")
	t.Setenv("DATA_PATH", "data.md")
	// Intentionally leave DATA_MD and API_BASE_PATH unset

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	// default per code is "/api/v1"
	if cfg.APIBasePath != "/api/v1" {
		t.Fatalf("API_BASE_PATH default expected '/api/v1', got %q", cfg.APIBasePath)
	}
	// DataMD remains empty when unset
	if cfg.DataMD != "" {
		t.Fatalf("expected empty DataMD when unset, got %q", cfg.DataMD)
	}
}

func TestMustLoad_Success_NoPanic(t *testing.T) {
	// No special env needed; defaults are valid.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("MustLoad should not panic on valid defaults, got: %v", r)
		}
	}()
	cfg := MustLoad()
	if cfg.APIBasePath == "" {
		t.Fatalf("unexpected empty config from MustLoad")
	}
}
