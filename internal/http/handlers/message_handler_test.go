package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	sqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/tbourn/go-chat-backend/internal/domain"
	"github.com/tbourn/go-chat-backend/internal/repo"
	"github.com/tbourn/go-chat-backend/internal/services"
)

// ---------- test plumbing ----------

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:msg_handlers?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.Exec("PRAGMA foreign_keys=ON;")
	if err := db.AutoMigrate(&domain.Chat{}, &domain.Message{}, &domain.Feedback{}, &domain.Idempotency{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Logger
	t.Cleanup(func() { log.Logger = prev })
	log.Logger = zerolog.New(&buf)
	return &buf
}

// Handlers.New expects interfaces in this package; we satisfy them with stubs.

type stubMsgSvc struct {
	answer func(ctx context.Context, userID, chatID, prompt string) (*domain.Message, error)
	list   func(ctx context.Context, chatID string, page, pageSize int) ([]domain.Message, int64, error)
}

func (s stubMsgSvc) Answer(ctx context.Context, userID, chatID, prompt string) (*domain.Message, error) {
	return s.answer(ctx, userID, chatID, prompt)
}

func (s stubMsgSvc) ListPage(ctx context.Context, chatID string, page, pageSize int) ([]domain.Message, int64, error) {
	return s.list(ctx, chatID, page, pageSize)
}

type (
	stubChatSvc struct{}
)

// we only need New(...) to succeed; chat/feedback handlers aren’t used here.
func (stubChatSvc) Create(context.Context, string, string) (*domain.Chat, error) { return nil, nil }
func (stubChatSvc) List(context.Context, string) ([]domain.Chat, error)          { return nil, nil }
func (stubChatSvc) ListPage(context.Context, string, int, int) ([]domain.Chat, int64, error) {
	return nil, 0, nil
}
func (stubChatSvc) UpdateTitle(context.Context, string, string, string) error { return nil }

// ---------- helpers-only unit tests ----------

func Test_sanitizeContent_and_clamp_and_idemKey(t *testing.T) {
	// sanitizeContent:
	raw := "  line1\r\n\r\n\r\n\r\nline2\rline3  "
	got := sanitizeContent(raw)
	want := "line1\n\nline2\nline3"
	if got != want {
		t.Fatalf("sanitizeContent: got %q want %q", got, want)
	}
	// Also ensure it trims to empty
	if sanitizeContent(" \r\n\t ") != "" {
		t.Fatalf("sanitizeContent should trim to empty")
	}

	// clampMsgPagination:
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest("GET", "/?page=-3&page_size=9999", nil)
	c.Request = req
	p, ps := clampMsgPagination(c)
	if p != 1 || ps != 100 {
		t.Fatalf("clamp: got page=%d size=%d; want 1,100", p, ps)
	}
	c, _ = gin.CreateTestContext(httptest.NewRecorder())
	req = httptest.NewRequest("GET", "/?page=&page_size=0", nil)
	c.Request = req
	p, ps = clampMsgPagination(c)
	if p != 1 || ps != 1 {
		t.Fatalf("clamp defaults: got %d,%d", p, ps)
	}

	// middlewareGetIdempotencyKey
	c, _ = gin.CreateTestContext(httptest.NewRecorder())
	req = httptest.NewRequest("POST", "/", nil)
	req.Header.Set("Idempotency-Key", "k-1")
	c.Request = req
	k, ok := middlewareGetIdempotencyKey(c)
	if !ok || k != "k-1" {
		t.Fatalf("idem key: %v %q", ok, k)
	}
}

// ---------- PostMessage ----------

func TestPostMessage_InvalidUUID_and_Binding_and_TooLong(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// we don't need chat/feedback services; stub message service never called for the first two cases
	h := New(stubChatSvc{}, stubMsgSvc{
		answer: func(ctx context.Context, userID, chatID, prompt string) (*domain.Message, error) {
			return &domain.Message{ID: "m1", ChatID: chatID, Role: "assistant", Content: "ok"}, nil
		},
		list: nil,
	}, &services.FeedbackService{DB: nil})

	r.POST("/chats/:id/messages", h.PostMessage)

	// invalid UUID
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/chats/not-a-uuid/messages", bytes.NewBufferString(`{"content":"x"}`))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid uuid -> %d", w.Code)
	}

	// binding error (missing content)
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/chats/"+uuid.NewString()+"/messages", bytes.NewBufferString(`{}`))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("binding error -> %d", w.Code)
	}

	// too long content (discoverMaxPromptRunes uses *services.MessageService)
	db := newTestDB(t)
	ms := &services.MessageService{DB: db, MaxPromptRunes: 5}
	h2 := New(stubChatSvc{}, ms, &services.FeedbackService{DB: db})
	r2 := gin.New()
	r2.POST("/chats/:id/messages", h2.PostMessage)
	long := "123456"
	if utf8.RuneCountInString(long) != 6 {
		t.Fatalf("test precondition wrong")
	}
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/chats/"+uuid.NewString()+"/messages", bytes.NewBufferString(`{"content":"`+long+`"}`))
	r2.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("too long -> %d", w.Code)
	}
	if !regexp.MustCompile(`max 5`).Match(w.Body.Bytes()) {
		t.Fatalf("expected max count in message, got %s", w.Body.String())
	}
}

func TestPostMessage_Idempotency_Replay_and_Store(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTestDB(t)

	// seed chat + message + idempotency record for replay
	userID := "u1"
	chatID := uuid.NewString()
	now := time.Now().UTC()

	if err := db.Create(&domain.Chat{ID: chatID, UserID: userID, Title: "T", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	prev := &domain.Message{ID: "m-prev", ChatID: chatID, Role: "assistant", Content: "previous", CreatedAt: now, UpdatedAt: now}
	if err := db.Create(prev).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if _, err := repo.CreateIdempotency(context.Background(), db, userID, chatID, "key-replay", prev.ID, 200, time.Hour); err != nil {
		t.Fatalf("seed idem: %v", err)
	}

	ms := &services.MessageService{DB: db, MaxPromptRunes: 2000}
	h := New(stubChatSvc{}, ms, &services.FeedbackService{DB: db})

	r := gin.New()
	r.POST("/chats/:id/messages", h.PostMessage)

	// replay request
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/chats/"+chatID+"/messages", bytes.NewBufferString(`{"content":" hello "}`))
	req.Header.Set("X-User-ID", userID)
	req.Header.Set("Idempotency-Key", "key-replay")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("replay -> %d", w.Code)
	}
	if w.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("expected replay header")
	}
	var resp PostMessageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp.Message == nil || resp.Message.ID != prev.ID || resp.Message.Content != "previous" {
		t.Fatalf("unexpected replay body: %#v", resp)
	}

	// ----------- store path -----------
	// Use a fresh key; there is no record, so Answer runs and then CreateIdempotency should write a record.
	// Also seed chat for this case
	chat2 := uuid.NewString()
	if err := db.Create(&domain.Chat{ID: chat2, UserID: userID, Title: "T2", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed chat2: %v", err)
	}

	r2 := gin.New()
	r2.POST("/chats/:id/messages", h.PostMessage)

	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/chats/"+chat2+"/messages", bytes.NewBufferString(`{"content":"question?"}`))
	req2.Header.Set("X-User-ID", userID)
	req2.Header.Set("Idempotency-Key", "key-store")
	r2.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("store -> %d body=%s", w2.Code, w2.Body.String())
	}
	var resp2 PostMessageResponse
	if err := json.Unmarshal(w2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("json2: %v", err)
	}
	if resp2.Message == nil || resp2.Message.ChatID != chat2 || resp2.Message.Role != "assistant" {
		t.Fatalf("assistant msg missing: %#v", resp2)
	}
	// verify idempotency row exists
	rec, err := repo.GetIdempotency(context.Background(), db, userID, chat2, "key-store", time.Now().UTC().Add(-time.Second))
	if err != nil || rec == nil || rec.MessageID != resp2.Message.ID {
		t.Fatalf("idempotency not stored: rec=%+v err=%v", rec, err)
	}
}

// ---------- ListMessages ----------

func TestListMessages_UUID_And_ETag304(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTestDB(t)
	buf := captureLogs(t) // so 5xx paths would log if they happen

	// seed chat + messages for ETag
	chatID := uuid.NewString()
	now := time.Now().UTC()
	if err := db.Create(&domain.Chat{ID: chatID, UserID: "u1", Title: "T", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	msg := &domain.Message{ID: "m1", ChatID: chatID, Role: "assistant", Content: "hello", CreatedAt: now, UpdatedAt: now}
	if err := db.Create(msg).Error; err != nil {
		t.Fatalf("seed msg: %v", err)
	}

	ms := &services.MessageService{DB: db}
	h := New(stubChatSvc{}, ms, &services.FeedbackService{DB: db})

	r := gin.New()
	r.GET("/chats/:id/messages", h.ListMessages)

	// invalid uuid
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/chats/not-uuid/messages", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("uuid 400 -> %d", w.Code)
	}

	// ETag pre-check: compute expected tag
	count, maxTS, err := repo.MessagesStats(context.Background(), db, chatID)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	var ts int64
	if maxTS != nil {
		ts = maxTS.Unix()
	}
	etag := `W/"messages:` + chatID + `:` + intToStr(count) + `:` + intToStr64(ts) + `"`

	// 304 path
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/chats/"+chatID+"/messages", nil)
	req.Header.Set("If-None-Match", etag)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotModified {
		t.Fatalf("etag 304 -> %d headers=%v logs=%s", w.Code, w.Header(), buf.String())
	}
}

func TestListMessages_Success_And_Errors(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// stub service for success
	items := []domain.Message{
		{ID: "m1", ChatID: "c", Role: "user", Content: "hi"},
		{ID: "m2", ChatID: "c", Role: "assistant", Content: "yo"},
	}
	svcOK := stubMsgSvc{
		list: func(ctx context.Context, chatID string, page, pageSize int) ([]domain.Message, int64, error) {
			if chatID == "" || page < 1 || pageSize < 1 {
				t.Fatalf("bad args to ListPage: chat=%q page=%d size=%d", chatID, page, pageSize)
			}
			return items, 5, nil
		},
		answer: nil,
	}
	hOK := New(stubChatSvc{}, svcOK, &services.FeedbackService{DB: nil})
	r := gin.New()
	r.GET("/chats/:id/messages", hOK.ListMessages)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/chats/"+uuid.NewString()+"/messages?page=2&page_size=2", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list ok -> %d", w.Code)
	}
	var out ListMessagesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(out.Messages) != 2 || out.Pagination.Page != 2 || out.Pagination.PageSize != 2 ||
		out.Pagination.Total != 5 || out.Pagination.TotalPages != 3 || out.Pagination.HasNext != true {
		t.Fatalf("pagination wrong: %#v", out.Pagination)
	}

	// ErrChatNotFound -> 404
	svc404 := stubMsgSvc{
		list: func(ctx context.Context, chatID string, page, pageSize int) ([]domain.Message, int64, error) {
			return nil, 0, services.ErrChatNotFound
		},
		answer: nil,
	}
	h404 := New(stubChatSvc{}, svc404, &services.FeedbackService{DB: nil})
	r2 := gin.New()
	r2.GET("/chats/:id/messages", h404.ListMessages)

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/chats/"+uuid.NewString()+"/messages", nil)
	r2.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}

	// generic error -> 500
	svc500 := stubMsgSvc{
		list: func(ctx context.Context, chatID string, page, pageSize int) ([]domain.Message, int64, error) {
			return nil, 0, gorm.ErrInvalidField
		},
		answer: nil,
	}
	h500 := New(stubChatSvc{}, svc500, &services.FeedbackService{DB: nil})
	r3 := gin.New()
	r3.GET("/chats/:id/messages", h500.ListMessages)

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/chats/"+uuid.NewString()+"/messages", nil)
	r3.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
}

// ---------- tiny helpers for ETag ints (avoid importing strconv for clarity) ----------

func intToStr(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [32]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + (n % 10))
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
func intToStr64(n int64) string { return intToStr(n) }

func Test_discoverMaxPromptRunes_AllPaths(t *testing.T) {
	// non-*MessageService -> fallback
	if got := discoverMaxPromptRunes(stubMsgSvc{}); got != 4000 {
		t.Fatalf("fallback for non-*MessageService, got %d", got)
	}
	// *MessageService with MaxPromptRunes <= 0 -> fallback
	if got := discoverMaxPromptRunes(&services.MessageService{MaxPromptRunes: 0}); got != 4000 {
		t.Fatalf("fallback when MaxPromptRunes<=0, got %d", got)
	}
	// *MessageService with MaxPromptRunes > 0
	if got := discoverMaxPromptRunes(&services.MessageService{MaxPromptRunes: 123}); got != 123 {
		t.Fatalf("expected 123, got %d", got)
	}
}

func Test_middlewareGetIdempotencyKey_MissingHeader(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/", nil)
	k, ok := middlewareGetIdempotencyKey(c)
	if ok || k != "" {
		t.Fatalf("expected no idempotency key, got ok=%v key=%q", ok, k)
	}
}

func TestPostMessage_EmptyAfterSanitize(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := New(stubChatSvc{}, stubMsgSvc{
		// should not be called
		answer: func(ctx context.Context, u, cID, p string) (*domain.Message, error) {
			t.Fatalf("Answer should not be called for empty content")
			return nil, nil
		},
		list: nil,
	}, &services.FeedbackService{DB: nil})

	r := gin.New()
	r.POST("/chats/:id/messages", h.PostMessage)

	w := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"content":"  \r\n \n\t "}`) // sanitizes to empty
	req := httptest.NewRequest(http.MethodPost, "/chats/"+uuid.NewString()+"/messages", body)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty-after-sanitize, got %d", w.Code)
	}
}

func TestPostMessage_ErrorMappings(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name string
		err  error
		want int
	}{
		{"chat_not_found", services.ErrChatNotFound, http.StatusNotFound},
		{"too_long", services.ErrTooLong, http.StatusBadRequest},
		{"empty_prompt", services.ErrEmptyPrompt, http.StatusBadRequest},
		{"generic_500", gorm.ErrInvalidField, http.StatusInternalServerError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := stubMsgSvc{
				answer: func(ctx context.Context, u, cID, p string) (*domain.Message, error) {
					return nil, tc.err
				},
				list: nil,
			}
			h := New(stubChatSvc{}, svc, &services.FeedbackService{DB: nil})

			r := gin.New()
			r.POST("/chats/:id/messages", h.PostMessage)

			w := httptest.NewRecorder()
			body := bytes.NewBufferString(`{"content":"hello"}`)
			req := httptest.NewRequest(http.MethodPost, "/chats/"+uuid.NewString()+"/messages", body)
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)

			if w.Code != tc.want {
				t.Fatalf("want %d, got %d body=%s", tc.want, w.Code, w.Body.String())
			}
		})
	}
}
