// Package httpapi wires the HTTP transport (Gin) to application services,
// middleware, and route handlers. It centralizes cross-cutting concerns such
// as tracing, correlation IDs, logging/redaction, panic recovery, metrics,
// CORS, security headers, idempotency, and rate limiting.
//
// Design goals:
//   - Put observability first (OTel + Prometheus)
//   - Safe-by-default middleware ordering (RequestID → logging → recovery)
//   - Deterministic, minimal router setup; all dependencies injected
//   - Production-ready CORS and security header posture
package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/tbourn/go-chat-backend/internal/config"
	"github.com/tbourn/go-chat-backend/internal/domain"
	"github.com/tbourn/go-chat-backend/internal/http/handlers"
	"github.com/tbourn/go-chat-backend/internal/http/middleware"
	"github.com/tbourn/go-chat-backend/internal/repo"
	"github.com/tbourn/go-chat-backend/internal/search"
	"github.com/tbourn/go-chat-backend/internal/services"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"golang.org/x/text/language"
	"gorm.io/gorm"
)

// chatRepoShim adapts the repository free functions to the services.ChatRepo
// interface expected by the ChatService. This keeps services decoupled from
// the concrete repo package while reusing existing functions.
type chatRepoShim struct{}

// CreateChat proxies repo.CreateChat.
func (chatRepoShim) CreateChat(ctx context.Context, db *gorm.DB, userID, title string) (*domain.Chat, error) {
	return repo.CreateChat(ctx, db, userID, title)
}

// ListChats proxies repo.ListChats.
func (chatRepoShim) ListChats(ctx context.Context, db *gorm.DB, userID string) ([]domain.Chat, error) {
	return repo.ListChats(ctx, db, userID)
}

// GetChat proxies repo.GetChat.
func (chatRepoShim) GetChat(ctx context.Context, db *gorm.DB, id, userID string) (*domain.Chat, error) {
	return repo.GetChat(ctx, db, id, userID)
}

// UpdateChatTitle proxies repo.UpdateChatTitle.
func (chatRepoShim) UpdateChatTitle(ctx context.Context, db *gorm.DB, id, userID, title string) error {
	return repo.UpdateChatTitle(ctx, db, id, userID, title)
}

// CountChats proxies repo.CountChats (pagination support).
func (chatRepoShim) CountChats(ctx context.Context, db *gorm.DB, userID string) (int64, error) {
	return repo.CountChats(ctx, db, userID)
}

// ListChatsPage proxies repo.ListChatsPage (pagination support).
func (chatRepoShim) ListChatsPage(ctx context.Context, db *gorm.DB, userID string, offset, limit int) ([]domain.Chat, error) {
	return repo.ListChatsPage(ctx, db, userID, offset, limit)
}

// RegisterRoutes attaches all middleware and HTTP endpoints to the given Gin
// engine. It configures observability (tracing, metrics), idempotency and rate
// limiting, CORS and security headers, health and metrics endpoints, and then
// mounts the versioned public API under /api/v*.
//
// Middleware order matters:
//  1. OpenTelemetry: trace everything
//  2. RequestID: generate/propagate correlation id
//  3. RedactingLogger: structured logs with PII scrubbing
//  4. Recovery: capture panics after logger
//  5. Body size limiter
//  6. Metrics
//  7. Idempotency validator (before rate limiter to allow bypass on replay)
//  8. Rate limiter (per user/IP, bypass on replay)
//  9. CORS and Security headers
func RegisterRoutes(r *gin.Engine, db *gorm.DB, idx search.Index, cfg config.Config) {
	r.HandleMethodNotAllowed = true

	// 1) Trace all HTTP requests
	r.Use(otelgin.Middleware(cfg.OTEL.ServiceName))

	// 2) Correlate requests and logs
	r.Use(middleware.RequestID())

	// 3) Structured logging with redaction
	r.Use(middleware.RedactingLogger(middleware.RedactOptions{
		MaskHeaders: []string{
			"X-API-Key", // project-specific sensitive header example
		},
	}))

	// 4) Panic recovery to JSON 500 (with request id)
	r.Use(middleware.Recovery())

	// 5) Global body size limit (1 MiB)
	r.Use(limitBody(1 << 20))

	// 6) Prometheus metrics and /metrics endpoint
	r.Use(middleware.Metrics())
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// 7) Idempotency validation (before rate limiting)
	r.Use(middleware.IdempotencyValidator(
		middleware.IdempotencyOptions{
			MaxLen: 200,
		},
		func(ctx context.Context, userID, chatID, key string, now time.Time) (bool, error) {
			rec, err := repo.GetIdempotency(ctx, db, userID, chatID, key, now)
			if err != nil || rec == nil {
				return false, nil
			}
			return true, nil
		},
	))

	// 8) Token-bucket rate limiter per user/IP
	rl := middleware.NewRateLimiter(cfg.RateRPS, cfg.RateBurst, middleware.KeyByUserOrIP())
	r.Use(rl.Handler())

	// 9) CORS posture (safe defaults: allow all if none configured)
	if len(cfg.CORS.AllowedOrigins) == 0 {
		// Force ACAO: * even for requests without an Origin header (helps tests and simple health checks).
		r.Use(func(c *gin.Context) {
			c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
			c.Next()
		})
		r.Use(cors.New(cors.Config{
			AllowAllOrigins:  true,
			AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
			AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization", "X-User-ID", middleware.HeaderIdempotencyKey},
			ExposeHeaders:    []string{"X-Request-ID", "Content-Length"},
			AllowCredentials: false, // must remain false with AllowAllOrigins
			MaxAge:           12 * time.Hour,
		}))
	} else {
		// Echo ACAO with the request Origin when it is in the allowlist (in addition to gin-contrib/cors).
		allowed := make(map[string]struct{}, len(cfg.CORS.AllowedOrigins))
		for _, o := range cfg.CORS.AllowedOrigins {
			allowed[o] = struct{}{}
		}
		r.Use(func(c *gin.Context) {
			if origin := c.GetHeader("Origin"); origin != "" {
				if _, ok := allowed[origin]; ok {
					h := c.Writer.Header()
					h.Set("Access-Control-Allow-Origin", origin)
					h.Add("Vary", "Origin")
				}
			}
			c.Next()
		})
		r.Use(cors.New(cors.Config{
			AllowOrigins:     cfg.CORS.AllowedOrigins,
			AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
			AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization", "X-User-ID", middleware.HeaderIdempotencyKey},
			ExposeHeaders:    []string{"X-Request-ID", "Content-Length"},
			AllowCredentials: false,
			MaxAge:           12 * time.Hour,
		}))
	}

	// Security headers (HSTS only when enabled and request is HTTPS)
	r.Use(middleware.SecurityHeaders(middleware.SecurityOptions{
		EnableHSTS:   cfg.Security.EnableHSTS,
		HSTSMaxAge:   cfg.Security.HSTSMaxAge,
		NoStore:      false,
		EnablePolicy: true,
	}))

	// Fallbacks
	r.NoRoute(func(c *gin.Context) {
		handlers.Fail(c, http.StatusNotFound, handlers.ErrCodeNotFound, "route not found")
	})
	r.NoMethod(func(c *gin.Context) {
		handlers.Fail(c, http.StatusMethodNotAllowed, handlers.ErrCodeMethodNotAllowed, "method not allowed")
	})

	// Liveness/health
	r.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

	// Dependency injection: services ← repo/db/index
	chatSvc := services.NewChatService(db, chatRepoShim{})
	msgSvc := &services.MessageService{
		DB:             db,
		Index:          idx,
		Threshold:      cfg.Threshold,
		MaxPromptRunes: 2000,
		MaxReplyRunes:  1500,
		TitleMaxLen:    6,
		TitleLocale:    language.English,
	}

	fbSvc := &services.FeedbackService{DB: db}
	h := handlers.New(chatSvc, msgSvc, fbSvc)

	// Public API
	apiBase := cfg.APIBasePath // e.g. "/api/v1"
	api := groupWithPrefix(r, apiBase)
	{
		// Chats
		api.POST("/chats", h.CreateChat)
		api.GET("/chats", h.ListChats)
		api.PUT("/chats/:id/title", h.UpdateChatTitle)

		// Messages
		api.GET("/chats/:id/messages", h.ListMessages)
		api.POST("/chats/:id/messages", h.PostMessage)

		// Feedback
		api.POST("/messages/:id/feedback", h.LeaveFeedback)
	}
}

// limitBody returns a Gin middleware that caps the request body size for all
// endpoints to maxBytes using http.MaxBytesReader. Requests exceeding the cap
// will cause downstream body reads to error.
func limitBody(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}

// groupWithPrefix mounts a group at prefix, treating "/" (or empty) as root.
func groupWithPrefix(r *gin.Engine, prefix string) *gin.RouterGroup {
	if prefix == "" || prefix == "/" {
		return r.Group("")
	}
	return r.Group(prefix)
}
