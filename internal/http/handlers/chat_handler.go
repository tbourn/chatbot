// Chat HTTP handlers.
//
// This file exposes REST endpoints for chat resources:
//   - POST   /chats               (create)
//   - GET    /chats               (list, paginated, ETag support)
//   - PUT    /chats/{id}/title    (rename)
//
// Handlers are transport-thin: they validate input, call application services,
// and translate results into HTTP responses (including conditional responses).
package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/tbourn/go-chat-backend/internal/domain"
	"github.com/tbourn/go-chat-backend/internal/repo"
	"github.com/tbourn/go-chat-backend/internal/services"
	"github.com/tbourn/go-chat-backend/internal/utils"
)

//
// Service contracts (context-aware)
//

// ChatService defines chat lifecycle operations consumed by HTTP handlers.
//
// Implementations should be safe for concurrent use and must honor the
// provided context for cancellation and timeouts.
type ChatService interface {
	// Create starts a new chat for userID with an optional title.
	Create(ctx context.Context, userID, title string) (*domain.Chat, error)
	// List returns all chats for a user (legacy, non-paginated).
	List(ctx context.Context, userID string) ([]domain.Chat, error)
	// ListPage returns a page of chats for a user and the total count.
	ListPage(ctx context.Context, userID string, page, pageSize int) ([]domain.Chat, int64, error)
	// UpdateTitle renames a chat that belongs to userID.
	UpdateTitle(ctx context.Context, userID, chatID, title string) error
}

// MessageService defines message retrieval and generation operations.
//
// Implementations should be safe for concurrent use and must honor the
// provided context for cancellation and timeouts.
type MessageService interface {
	// Answer appends a user prompt and an assistant reply to a chat atomically.
	Answer(ctx context.Context, userID, chatID, prompt string) (*domain.Message, error)
	// ListPage returns a page of messages within a chat and the total count.
	ListPage(ctx context.Context, chatID string, page, pageSize int) ([]domain.Message, int64, error)
}

// FeedbackService defines operations to capture user feedback on messages.
//
// Implementations should be safe for concurrent use and must honor the
// provided context for cancellation and timeouts.
type FeedbackService interface {
	// Leave submits a feedback value (-1 or 1) for messageID by userID.
	Leave(ctx context.Context, userID, messageID string, value int) error
}

//
// Handler wiring
//

// Handlers groups HTTP endpoints for chats, messages, and feedback.
// It depends on abstract service interfaces to keep transport concerns
// separate from business logic.
type Handlers struct {
	chatSvc ChatService
	msgSvc  MessageService
	fbSvc   FeedbackService
}

// New constructs and returns a Handlers instance bound to the given services.
func New(chatSvc ChatService, msgSvc MessageService, fbSvc FeedbackService) *Handlers {
	return &Handlers{chatSvc: chatSvc, msgSvc: msgSvc, fbSvc: fbSvc}
}

// userID extracts the authenticated user id from Gin context (set by upstream
// middleware). If absent, it falls back to "X-User-ID" header (tests use it),
// and finally to "demo-user". It never touches c.Request if it's nil.
func userID(c *gin.Context) string {
	if v, ok := c.Get("userID"); ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	if c != nil && c.Request != nil {
		if h := strings.TrimSpace(c.GetHeader("X-User-ID")); h != "" {
			return h
		}
	}
	return "demo-user"
}

//
// DTOs
//

// CreateChatRequest is the JSON payload for creating a chat.
type CreateChatRequest struct {
	// Title optionally sets the chat title; a default is used when empty.
	Title string `json:"title" example:"Customer insights UK"`
}

// UpdateChatTitleRequest is the JSON payload for updating a chat title.
type UpdateChatTitleRequest struct {
	// Title is the new chat name (1–255 chars).
	Title string `json:"title" binding:"required,min=1,max=255" example:"Penetration - 18–24 UK"`
}

// Pagination carries pagination metadata for list responses.
type Pagination struct {
	Page       int   `json:"page"`
	PageSize   int   `json:"page_size"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
	HasNext    bool  `json:"has_next"`
}

// ListChatsResponse wraps a page of chats and pagination information.
type ListChatsResponse struct {
	Chats      []domain.Chat `json:"chats"`
	Pagination Pagination    `json:"pagination"`
}

//
// Helpers
//

// clampPagination parses and bounds page and page_size query params to sane
// defaults and limits, returning (page, pageSize).
func clampPagination(c *gin.Context) (page, pageSize int) {
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

//
// Handlers
//

// CreateChat godoc
// @ID          createChat
// @Summary     Create a new chat
// @Description Creates a chat for the current user and returns the chat resource.
// @Tags        Chats
// @Accept      json
// @Produce     json
//
// @Param       X-User-ID  header  string  false "User ID (demo header)"  example(user123)
// @Param       body       body    handlers.CreateChatRequest  true  "Create chat payload"
//
// @Success     201  {object}  domain.Chat
// @Failure     400  {object}  handlers.ErrorResponse  "Bad request"
// @Failure     500  {object}  handlers.ErrorResponse  "Internal error"
// @Router      /chats [post]
func (h *Handlers) CreateChat(c *gin.Context) {
	var req CreateChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, ErrCodeBadRequest, "invalid JSON body")
		return
	}
	title := strings.TrimSpace(req.Title)

	ch, err := h.chatSvc.Create(c.Request.Context(), userID(c), title)
	if err != nil {
		fail(c, http.StatusInternalServerError, ErrCodeCreateFailed, err.Error())
		return
	}
	ok(c, http.StatusCreated, ch)
}

// ListChats godoc
// @ID          listChats
// @Summary     List chats (paginated)
// @Description Returns a page of the user's chats. Supports weak ETag via If-None-Match and may return 304.
// @Tags        Chats
// @Produce     json
//
// @Param       X-User-ID      header  string  false "User ID (demo header)"       example(user123)
// @Param       If-None-Match  header  string  false "Return 304 if ETag matches"  example(W/\"abc123\")
// @Param       page           query   int     false "Page number"                  minimum(1) default(1)
// @Param       page_size      query   int     false "Items per page"               minimum(1) maximum(100) default(20)
//
// @Success     200  {object} handlers.ListChatsResponse
// @Header      200  {string} ETag           "Weak ETag for current result"
// @Header      200  {string} Cache-Control  "Caching directives (if set)"
// @Success     304  {string} string "Not Modified"
// @Failure     400  {object} handlers.ErrorResponse "Bad request"
// @Failure     500  {object} handlers.ErrorResponse "Internal error"
// @Router      /chats [get]
func (h *Handlers) ListChats(c *gin.Context) {
	ctx := c.Request.Context()
	uid := userID(c)
	page, pageSize := clampPagination(c)

	// ETag pre-check (best effort).
	var db *gorm.DB
	if svc, ok := h.chatSvc.(*services.ChatService); ok {
		db = svc.DB
	}
	if db != nil {
		count, maxTS, err := repo.ChatsStats(ctx, db, uid)
		if err == nil {
			var ts int64
			if maxTS != nil {
				ts = maxTS.Unix()
			}
			etag := fmt.Sprintf(`W/"chats:%s:%d:%d"`, uid, count, ts)
			c.Header("ETag", etag)
			if inm := c.GetHeader("If-None-Match"); inm != "" && inm == etag {
				c.Status(http.StatusNotModified)
				return
			}
		}
	}

	// Fetch page.
	items, total, err := h.chatSvc.ListPage(ctx, uid, page, pageSize)
	if err != nil {
		fail(c, http.StatusInternalServerError, ErrCodeListFailed, err.Error())
		return
	}

	totalPages := int((total + int64(pageSize) - 1) / int64(pageSize))
	resp := ListChatsResponse{
		Chats: items,
		Pagination: Pagination{
			Page:       page,
			PageSize:   pageSize,
			Total:      total,
			TotalPages: totalPages,
			HasNext:    page < totalPages,
		},
	}
	ok(c, http.StatusOK, resp)
}

// UpdateChatTitle godoc
// @ID          updateChatTitle
// @Summary     Rename a chat
// @Description Updates the title of a chat owned by the current user.
// @Tags        Chats
// @Accept      json
// @Produce     json
//
// @Param       X-User-ID  header  string  false "User ID (demo header)"         example(user123)
// @Param       id         path    string  true  "Chat ID (UUID)"                format(uuid) example(141add05-4415-4938-b5a1-17e0d3171aff)
// @Param       body       body    handlers.UpdateChatTitleRequest  true  "New title"
//
// @Success     204  {string} string "No Content"
// @Failure     400  {object} handlers.ErrorResponse "Bad request"
// @Failure     404  {object} handlers.ErrorResponse "Chat not found"
// @Failure     500  {object} handlers.ErrorResponse "Internal error"
// @Router      /chats/{id}/title [put]
func (h *Handlers) UpdateChatTitle(c *gin.Context) {
	chatID := c.Param("id")
	if _, err := uuid.Parse(chatID); err != nil {
		fail(c, http.StatusBadRequest, ErrCodeBadRequest, "chat id must be a UUID")
		return
	}

	var req UpdateChatTitleRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Title) == "" {
		fail(c, http.StatusBadRequest, ErrCodeBadRequest, "title required (1–255 chars)")
		return
	}

	if err := h.chatSvc.UpdateTitle(c.Request.Context(), userID(c), chatID, req.Title); err != nil {
		fail(c, http.StatusNotFound, ErrCodeNotFound, "chat not found")
		return
	}

	noContent(c)
}
