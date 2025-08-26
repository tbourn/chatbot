package httpapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	sqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/tbourn/go-chat-backend/internal/config"
	"github.com/tbourn/go-chat-backend/internal/domain"
	"github.com/tbourn/go-chat-backend/internal/http/middleware"
	"github.com/tbourn/go-chat-backend/internal/search"
)

// --- tiny fake index to satisfy search.Index ---
type fakeIndex struct{}

func (fakeIndex) TopK(_ string, _ int) []search.Result { return nil }

// --- test DB helper (pure-Go sqlite, no CGO) ---
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:routerdb?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// schema so handlers don't explode on list endpoints
	if err := db.AutoMigrate(&domain.Chat{}, &domain.Message{}, &domain.Feedback{}, &domain.Idempotency{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

func TestRegisterRoutes_CORSAllowAll_Health_Metrics_Fallbacks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	cfg := config.Config{
		APIBasePath: "/api/v1",
		RateRPS:     100,
		RateBurst:   10,
		CORS:        config.CORSConfig{AllowedOrigins: nil}, // triggers AllowAllOrigins branch
		Security:    config.SecurityConfig{EnableHSTS: false, HSTSMaxAge: 0},
		OTEL:        config.OTELConfig{ServiceName: "test-svc"},
		Threshold:   0.2,
	}
	db := newTestDB(t)

	RegisterRoutes(r, db, fakeIndex{}, cfg)

	// /health works
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /health = %d", w.Code)
	}
	// CORS (AllowAllOrigins) → header "*"
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("AllowAllOrigins expected '*', got %q", got)
	}

	// /metrics is wired
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || len(w.Body.Bytes()) == 0 {
		t.Fatalf("GET /metrics bad: code=%d len=%d", w.Code, w.Body.Len())
	}

	// NoRoute → 404
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/nope", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET /nope expected 404, got %d", w.Code)
	}

	// NoMethod → 405 (POST /health)
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/health", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /health expected 405, got %d", w.Code)
	}
}

func TestRegisterRoutes_CORSWithOrigins_HeaderEcho(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	cfg := config.Config{
		APIBasePath: "/api/v2",
		RateRPS:     50,
		RateBurst:   5,
		CORS:        config.CORSConfig{AllowedOrigins: []string{"http://example.com"}},
		Security:    config.SecurityConfig{EnableHSTS: false, HSTSMaxAge: 0},
		OTEL:        config.OTELConfig{ServiceName: "test-svc"},
		Threshold:   0.2,
	}
	db := newTestDB(t)

	RegisterRoutes(r, db, fakeIndex{}, cfg)

	// Any request runs through CORS middleware; header should reflect origin.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "http://example.com")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /health = %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://example.com" {
		t.Fatalf("expected ACAO echo, got %q", got)
	}
}

func Test_limitBody_Middleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// tiny cap to trigger MaxBytesReader
	r.Use(limitBody(10))
	r.POST("/echo", func(c *gin.Context) {
		_, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.String(http.StatusRequestEntityTooLarge, "too big")
			return
		}
		c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/echo", bytes.NewBufferString("0123456789AB")) // 12 bytes
	r.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 from limitBody, got %d", w.Code)
	}
}

func Test_groupWithPrefix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	// "/" and "" should mount at root
	root1 := groupWithPrefix(r, "/")
	root1.GET("/one", func(c *gin.Context) { c.String(http.StatusOK, "one") })
	root2 := groupWithPrefix(r, "")
	root2.GET("/two", func(c *gin.Context) { c.String(http.StatusOK, "two") })

	// non-root prefix
	api := groupWithPrefix(r, "/api")
	api.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })

	// Hit all three
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/one", nil)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "one" {
		t.Fatalf("GET /one got %d %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/two", nil)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "two" {
		t.Fatalf("GET /two got %d %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/ping", nil)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "pong" {
		t.Fatalf("GET /api/ping got %d %q", rec.Code, rec.Body.String())
	}
}

// Smoke test that a request traverses idempotency + ratelimit + otel + security headers pipeline.
func TestPipeline_Smoke(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	cfg := config.Config{
		APIBasePath: "/api/v1",
		RateRPS:     100,
		RateBurst:   10,
		CORS:        config.CORSConfig{},                                            // allow-all branch
		Security:    config.SecurityConfig{EnableHSTS: true, HSTSMaxAge: time.Hour}, // enabled (but only set on https)
		OTEL:        config.OTELConfig{ServiceName: "svc"},
		Threshold:   0.2,
	}
	db := newTestDB(t)
	RegisterRoutes(r, db, fakeIndex{}, cfg)

	// Any request goes through the middleware stack
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	// simulate https so HSTS could be eligible if middleware checks scheme
	req.URL.Scheme = "https"
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("pipeline GET /health = %d", w.Code)
	}
	// RequestID header should be present (from RequestID middleware)
	if rid := w.Header().Get("X-Request-ID"); rid == "" {
		t.Fatalf("expected X-Request-ID header to be set")
	}
	// Tracing middleware shouldn't cause errors; nothing to assert here beyond 200.
	_ = context.Background()
}

func Test_chatRepoShim_Proxies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTestDB(t)

	shim := chatRepoShim{}
	ctx := context.Background()

	// --- CreateChat ---
	c1, err := shim.CreateChat(ctx, db, "u1", "t1")
	if err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	if c1 == nil || c1.ID == "" || c1.Title != "t1" || c1.UserID != "u1" {
		t.Fatalf("CreateChat returned bad chat: %+v", c1)
	}

	// --- ListChats ---
	all, err := shim.ListChats(ctx, db, "u1")
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if len(all) < 1 {
		t.Fatalf("ListChats expected >=1, got %d", len(all))
	}

	// --- GetChat ---
	got, err := shim.GetChat(ctx, db, c1.ID, "u1")
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if got.ID != c1.ID || got.UserID != "u1" {
		t.Fatalf("GetChat mismatch: got=%+v want id=%s user=u1", got, c1.ID)
	}

	// --- UpdateChatTitle ---
	if err := shim.UpdateChatTitle(ctx, db, c1.ID, "u1", "t1-renamed"); err != nil {
		t.Fatalf("UpdateChatTitle: %v", err)
	}
	got2, err := shim.GetChat(ctx, db, c1.ID, "u1")
	if err != nil {
		t.Fatalf("GetChat (after update): %v", err)
	}
	if got2.Title != "t1-renamed" {
		t.Fatalf("UpdateChatTitle failed, title=%q", got2.Title)
	}

	// Seed a few more for pagination
	if _, err := shim.CreateChat(ctx, db, "u1", "t2"); err != nil {
		t.Fatalf("CreateChat t2: %v", err)
	}
	if _, err := shim.CreateChat(ctx, db, "u1", "t3"); err != nil {
		t.Fatalf("CreateChat t3: %v", err)
	}

	// --- CountChats ---
	n, err := shim.CountChats(ctx, db, "u1")
	if err != nil {
		t.Fatalf("CountChats: %v", err)
	}
	if n < 3 {
		t.Fatalf("CountChats expected >=3, got %d", n)
	}

	// --- ListChatsPage ---
	page, err := shim.ListChatsPage(ctx, db, "u1", 0, 2)
	if err != nil {
		t.Fatalf("ListChatsPage: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("ListChatsPage expected 2, got %d", len(page))
	}
}

func TestRegisterRoutes_IdempotencyCallback_MissAndHit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	cfg := config.Config{
		APIBasePath: "/api/vX",
		RateRPS:     100,
		RateBurst:   10,
		CORS:        config.CORSConfig{}, // allow-all branch
		Security:    config.SecurityConfig{EnableHSTS: false},
		OTEL:        config.OTELConfig{ServiceName: "svc"},
		Threshold:   0.2,
	}
	db := newTestDB(t)
	RegisterRoutes(r, db, fakeIndex{}, cfg)

	const userID = "u1"
	const key = "key-hit"
	const chatID = "" // we’ll hit /health, so no path param

	// --- MISS: record does not exist (executes 'rec == nil' branch) ---
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/health", bytes.NewBufferString("{}"))
	req.Header.Set("X-User-ID", userID)
	req.Header.Set(middleware.HeaderIdempotencyKey, key)
	r.ServeHTTP(w, req)
	// NoMethod is expected for POST /health, but middleware ran.

	// --- seed an idempotency record so the callback returns non-nil ---
	seed := &domain.Idempotency{
		ID:        "idem-seed-1",
		UserID:    userID,
		ChatID:    chatID,
		Key:       key,
		MessageID: "m-1",
		Status:    1,
		// ensure it's considered valid "now"
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := db.Create(seed).Error; err != nil {
		t.Fatalf("seed idempotency: %v", err)
	}

	// --- HIT: record exists (executes 'return true, nil' branch) ---
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/health", bytes.NewBufferString("{}"))
	req.Header.Set("X-User-ID", userID)
	req.Header.Set(middleware.HeaderIdempotencyKey, key)
	r.ServeHTTP(w, req)
	// again, 405 is fine; goal is to drive the middleware branch.
}

func TestRegisterRoutes_IdempotencyCallback_ErrorBranch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	cfg := config.Config{
		APIBasePath: "/api/v1",
		RateRPS:     100,
		RateBurst:   10,
		CORS:        config.CORSConfig{}, // allow-all branch
		Security:    config.SecurityConfig{EnableHSTS: false},
		OTEL:        config.OTELConfig{ServiceName: "svc"},
		Threshold:   0.2,
	}

	// Make a fresh in-memory DB and migrate normally.
	db, err := gorm.Open(sqlite.Open("file:routerdb_err?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&domain.Chat{}, &domain.Message{}, &domain.Feedback{}, &domain.Idempotency{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}

	// Wire routes first...
	RegisterRoutes(r, db, fakeIndex{}, cfg)

	// ...then force queries to fail by closing the underlying connection.
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB(): %v", err)
	}
	_ = sqlDB.Close()

	// Now any repo.GetIdempotency call should error → drives (err != nil) branch.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/health", bytes.NewBufferString("{}"))
	req.Header.Set("X-User-ID", "u1")
	req.Header.Set(middleware.HeaderIdempotencyKey, "force-error")
	r.ServeHTTP(w, req)

	// 405 is expected for POST /health; goal is to exercise the middleware branch.
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}
