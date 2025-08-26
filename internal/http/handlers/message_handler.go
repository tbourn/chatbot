// Message HTTP handlers.
//
// This file exposes REST endpoints for chat messages:
//   - POST /chats/{id}/messages   (append a user message and create assistant reply)
//   - GET  /chats/{id}/messages   (list paginated messages for a chat)
//
// Handlers are transport-thin:
//   - validate & normalize inputs (including newline and length constraints)
//   - delegate to application services (MessageService)
//   - implement conditional responses (ETag) and idempotency semantics
//
// Idempotency:
// If the client supplies an Idempotency-Key header and a previous successful
// result exists for (user, chat, key), the handler returns that recorded
// assistant message and sets `Idempotency-Replayed: true`.
package handlers

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/tbourn/go-chat-backend/internal/domain"
	"github.com/tbourn/go-chat-backend/internal/repo"
	"github.com/tbourn/go-chat-backend/internal/services"
	"github.com/tbourn/go-chat-backend/internal/utils"
)

//
// DTOs
//

// PostMessageRequest is the JSON payload for sending a user message.
//
// Content is normalized by the handler (line endings and excessive blank lines)
// before being passed to the service layer. The service also enforces a
// maximum rune count, which can be configured in MessageService.
type PostMessageRequest struct {
	// Content is the user prompt. It must be non-empty.
	Content string `json:"content" binding:"required,min=1" example:"What percentage of Gen Z in Nashville discover new brands through podcasts?"`
}

// PostMessageResponse is the JSON envelope for a newly created assistant message.
type PostMessageResponse struct {
	// Message is the assistant reply created as a result of the request.
	Message *domain.Message `json:"message"`
}

// ListMessagesResponse contains a page of chat messages and pagination metadata.
type ListMessagesResponse struct {
	Messages   []domain.Message `json:"messages"`
	Pagination Pagination       `json:"pagination"`
}

//
// Helpers
//

// clampMsgPagination parses page/page_size from query parameters, applies sane
// defaults and caps, and returns the validated (page, pageSize).
func clampMsgPagination(c *gin.Context) (page, pageSize int) {
	const (
		defaultPage     = 1
		defaultPageSize = 20
		maxPageSize     = 100
	)
	page = utils.AtoiDefault(c.Query("page"), defaultPage)
	if page < 1 {
		page = 1
	}
	pageSize = utils.AtoiDefault(c.Query("page_size"), defaultPageSize)
	if pageSize < 1 {
		pageSize = 1
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	return
}

// nlCollapseRE collapses runs of 3+ newlines to two, preserving paragraphs.
var nlCollapseRE = regexp.MustCompile(`\n{3,}`)

// sanitizeContent normalizes user text for consistent downstream behavior:
//   - converts CRLF/CR to LF,
//   - collapses runs of 3+ LFs to exactly two (paragraph separation),
//   - trims surrounding whitespace.
func sanitizeContent(raw string) string {
	s := strings.ReplaceAll(raw, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = nlCollapseRE.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// discoverMaxPromptRunes inspects the concrete MessageService for a configured
// prompt-length limit. If unavailable, it returns a conservative fallback.
func discoverMaxPromptRunes(msgSvc MessageService) int {
	const fallback = 4000
	if ms, ok := msgSvc.(*services.MessageService); ok {
		if ms.MaxPromptRunes > 0 {
			return ms.MaxPromptRunes
		}
	}
	return fallback
}

//
// Handlers
//

// PostMessage godoc
// @ID          postMessage
// @Summary     Send a message and get assistant reply
// @Description Appends a user message to the chat and generates an assistant reply.
// @Description Supports idempotency via the Idempotency-Key header (same key → same result).
// @Tags        Messages
// @Accept      json
// @Produce     json
//
// @Param       X-User-ID        header  string  true  "User ID that owns the chat"  example(user123)
// @Param       Idempotency-Key  header  string  false "Idempotency key for safe retries (UUID recommended)"  example(7a8d9f4c-1b2a-4c3d-8e9f-0123456789ab)
// @Param       id               path    string  true  "Chat ID (UUID)"              format(uuid)
// @Param       body             body    handlers.PostMessageRequest  true  "User message payload"
//
// @Success     200  {object}  handlers.PostMessageResponse  "Assistant reply"
// @Failure     400  {object}  handlers.ErrorResponse        "Bad request"
// @Failure     404  {object}  handlers.ErrorResponse        "Chat not found"
// @Failure     500  {object}  handlers.ErrorResponse        "Internal error"
// @Router      /chats/{id}/messages [post]
func (h *Handlers) PostMessage(c *gin.Context) {
	ctx := c.Request.Context()
	chatID := c.Param("id")

	// Validate chat id shape if you use UUIDs.
	if _, err := uuid.Parse(chatID); err != nil {
		fail(c, http.StatusBadRequest, ErrCodeBadRequest, "chat id must be a UUID")
		return
	}

	var req PostMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, ErrCodeBadRequest, "content required")
		return
	}

	// Sanitize + early size cap to fail fast at the edge.
	content := sanitizeContent(req.Content)
	maxRunes := discoverMaxPromptRunes(h.msgSvc)
	if maxRunes > 0 && utf8.RuneCountInString(content) > maxRunes {
		fail(c, http.StatusBadRequest, ErrCodeBadRequest, fmt.Sprintf("content too long: max %d runes", maxRunes))
		return
	}
	if content == "" {
		fail(c, http.StatusBadRequest, ErrCodeBadRequest, "content required")
		return
	}

	currentUser := userID(c)

	// Idempotency (replay path) – read validated key if present.
	idemKey, _ := middlewareGetIdempotencyKey(c)
	if idemKey != "" {
		if svc, okSvc := h.msgSvc.(*services.MessageService); okSvc && svc.DB != nil {
			if rec, err := repo.GetIdempotency(ctx, svc.DB, currentUser, chatID, idemKey, time.Now().UTC()); err == nil && rec != nil {
				if prev, err2 := repo.GetMessage(svc.DB, rec.MessageID); err2 == nil {
					c.Header("Idempotency-Replayed", "true")
					ok(c, http.StatusOK, PostMessageResponse{Message: prev})
					return
				}
			}
		}
	}

	// Normal processing (service has a second guard for length).
	m, err := h.msgSvc.Answer(ctx, currentUser, chatID, content)
	if err != nil {
		switch err {
		case services.ErrChatNotFound:
			fail(c, http.StatusNotFound, ErrCodeNotFound, "chat not found")
		case services.ErrTooLong:
			fail(c, http.StatusBadRequest, ErrCodeBadRequest, fmt.Sprintf("content too long: max %d runes", maxRunes))
		case services.ErrEmptyPrompt:
			fail(c, http.StatusBadRequest, ErrCodeBadRequest, "content required")
		default:
			fail(c, http.StatusInternalServerError, ErrCodeAnswerFailed, err.Error())
		}
		return
	}

	// Idempotency (store path) – best effort.
	if idemKey != "" {
		if svc, ok := h.msgSvc.(*services.MessageService); ok && svc.DB != nil {
			ttl := 24 * time.Hour
			_, _ = repo.CreateIdempotency(ctx, svc.DB, currentUser, chatID, idemKey, m.ID, http.StatusOK, ttl)
		}
	}

	ok(c, http.StatusOK, PostMessageResponse{Message: m})
}

// ListMessages godoc
// @ID          listMessages
// @Summary     List messages in a chat
// @Description Returns a paginated list of messages for the given chat.
// @Tags        Messages
// @Produce     json
//
// @Param       id         path   string  true  "Chat ID (UUID)"  format(uuid)
// @Param       page       query  int     false "Page number"     minimum(1) default(1)
// @Param       page_size  query  int     false "Items per page"  minimum(1) maximum(100) default(20)
//
// @Success     200  {object} handlers.ListMessagesResponse
// @Failure     400  {object} handlers.ErrorResponse "Bad request"
// @Failure     404  {object} handlers.ErrorResponse "Chat not found"
// @Failure     500  {object} handlers.ErrorResponse "Internal error"
// @Router      /chats/{id}/messages [get]
func (h *Handlers) ListMessages(c *gin.Context) {
	ctx := c.Request.Context()
	chatID := c.Param("id")

	if _, err := uuid.Parse(chatID); err != nil {
		fail(c, http.StatusBadRequest, ErrCodeBadRequest, "chat id must be a UUID")
		return
	}

	// ETag pre-check (best effort).
	var db *gorm.DB
	if svc, ok := h.msgSvc.(*services.MessageService); ok {
		db = svc.DB
	}
	if db != nil {
		count, maxTS, err := repo.MessagesStats(ctx, db, chatID)
		if err == nil {
			var ts int64
			if maxTS != nil {
				ts = maxTS.Unix()
			}
			etag := fmt.Sprintf(`W/"messages:%s:%d:%d"`, chatID, count, ts)
			c.Header("ETag", etag)
			if inm := c.GetHeader("If-None-Match"); inm != "" && inm == etag {
				c.Status(http.StatusNotModified)
				return
			}
		}
	}

	page, pageSize := clampMsgPagination(c)

	items, total, err := h.msgSvc.ListPage(ctx, chatID, page, pageSize)
	if err != nil {
		switch err {
		case services.ErrChatNotFound:
			fail(c, http.StatusNotFound, ErrCodeNotFound, "chat not found")
		default:
			fail(c, http.StatusInternalServerError, ErrCodeListFailed, err.Error())
		}
		return
	}

	totalPages := int((total + int64(pageSize) - 1) / int64(pageSize))
	ok(c, http.StatusOK, ListMessagesResponse{
		Messages: items,
		Pagination: Pagination{
			Page:       page,
			PageSize:   pageSize,
			Total:      total,
			TotalPages: totalPages,
			HasNext:    page < totalPages,
		},
	})
}

// middlewareGetIdempotencyKey extracts an idempotency key if an upstream
// middleware has already validated/stashed it. The fallback behavior reads
// the "Idempotency-Key" header directly when no dedicated middleware exists.
func middlewareGetIdempotencyKey(c *gin.Context) (string, bool) {
	if v := strings.TrimSpace(c.GetHeader("Idempotency-Key")); v != "" {
		return v, true
	}
	return "", false
}
