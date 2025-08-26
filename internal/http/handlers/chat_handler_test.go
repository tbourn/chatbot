package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	sqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/tbourn/go-chat-backend/internal/domain"
	"github.com/tbourn/go-chat-backend/internal/repo"
	"github.com/tbourn/go-chat-backend/internal/services"
)

// ---------- test DB + repo shim ----------

func newChatDB(t *testing.T) *gorm.DB {
	t.Helper()

	// Unique DSN per call to avoid cross-test contamination
	dsn := fmt.Sprintf("file:chat_handlers_%s?mode=memory&cache=shared", uuid.NewString())

	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	// Enforce FKs and migrate schemas
	db.Exec("PRAGMA foreign_keys=ON;")
	if err := db.AutoMigrate(&domain.Chat{}, &domain.Message{}, &domain.Feedback{}, &domain.Idempotency{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// Minimal shim implementing services.ChatRepo using repo package (like router.go)
type testChatRepo struct{}

func (testChatRepo) CreateChat(ctx context.Context, db *gorm.DB, userID, title string) (*domain.Chat, error) {
	return repo.CreateChat(ctx, db, userID, title)
}

func (testChatRepo) ListChats(ctx context.Context, db *gorm.DB, userID string) ([]domain.Chat, error) {
	return repo.ListChats(ctx, db, userID)
}

func (testChatRepo) GetChat(ctx context.Context, db *gorm.DB, id, userID string) (*domain.Chat, error) {
	return repo.GetChat(ctx, db, id, userID)
}

func (testChatRepo) UpdateChatTitle(ctx context.Context, db *gorm.DB, id, userID, title string) error {
	return repo.UpdateChatTitle(ctx, db, id, userID, title)
}

func (testChatRepo) CountChats(ctx context.Context, db *gorm.DB, userID string) (int64, error) {
	return repo.CountChats(ctx, db, userID)
}

func (testChatRepo) ListChatsPage(ctx context.Context, db *gorm.DB, userID string, offset, limit int) ([]domain.Chat, error) {
	return repo.ListChatsPage(ctx, db, userID, offset, limit)
}

// ---------- tiny stubs for other services ----------

type stubMsgSvcChat struct{}

func (stubMsgSvcChat) Answer(ctx context.Context, userID, chatID, prompt string) (*domain.Message, error) {
	return nil, nil
}

func (stubMsgSvcChat) ListPage(ctx context.Context, chatID string, page, pageSize int) ([]domain.Message, int64, error) {
	return nil, 0, nil
}

type stubFBSvcChat struct{}

func (stubFBSvcChat) Leave(ctx context.Context, userID, messageID string, value int) error {
	return nil
}

// Flexible chat service stub for UpdateTitle tests
type stubChatSvcChat struct {
	create    func(context.Context, string, string) (*domain.Chat, error)
	list      func(context.Context, string) ([]domain.Chat, error)
	listPage  func(context.Context, string, int, int) ([]domain.Chat, int64, error)
	updateTit func(context.Context, string, string, string) error
}

func (s stubChatSvcChat) Create(ctx context.Context, u, t string) (*domain.Chat, error) {
	if s.create != nil {
		return s.create(ctx, u, t)
	}
	return &domain.Chat{ID: "c", UserID: u, Title: t}, nil
}

func (s stubChatSvcChat) List(ctx context.Context, u string) ([]domain.Chat, error) {
	if s.list != nil {
		return s.list(ctx, u)
	}
	return nil, nil
}

func (s stubChatSvcChat) ListPage(ctx context.Context, u string, p, ps int) ([]domain.Chat, int64, error) {
	if s.listPage != nil {
		return s.listPage(ctx, u, p, ps)
	}
	return nil, 0, nil
}

func (s stubChatSvcChat) UpdateTitle(ctx context.Context, u, id, t string) error {
	if s.updateTit != nil {
		return s.updateTit(ctx, u, id, t)
	}
	return nil
}

// ---------- helpers-only tests ----------

func Test_userID_and_clampPagination(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// userID helper
	rc := gin.CreateTestContextOnly(httptest.NewRecorder(), gin.New())
	if got := userID(rc); got != "demo-user" {
		t.Fatalf("fallback userID = %q", got)
	}
	rc.Set("userID", "u1")
	if got := userID(rc); got != "u1" {
		t.Fatalf("ctx userID = %q", got)
	}
	rc.Set("userID", 123) // wrong type → fallback
	if got := userID(rc); got != "demo-user" {
		t.Fatalf("wrong-type fallback userID = %q", got)
	}

	// header fallback
	cH, _ := gin.CreateTestContext(httptest.NewRecorder())
	reqH := httptest.NewRequest("GET", "/", nil)
	reqH.Header.Set("X-User-ID", "u-123")
	cH.Request = reqH
	if got := userID(cH); got != "u-123" {
		t.Fatalf("header fallback userID = %q", got)
	}

	// clampPagination bounds
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest("GET", "/?page=-5&page_size=9999", nil)
	c.Request = req
	p, ps := clampPagination(c)
	if p != 1 || ps != 100 {
		t.Fatalf("clamp bounds got p=%d ps=%d", p, ps)
	}
	c, _ = gin.CreateTestContext(httptest.NewRecorder())
	req = httptest.NewRequest("GET", "/?page=&page_size=0", nil)
	c.Request = req
	p, ps = clampPagination(c)
	if p != 1 || ps != 1 {
		t.Fatalf("clamp defaults got p=%d ps=%d", p, ps)
	}
}

// ---------- CreateChat ----------

func TestCreateChat_BadJSON_Success_Internal(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Bad JSON -> 400
	{
		h := New(stubChatSvcChat{}, stubMsgSvcChat{}, stubFBSvcChat{})
		r := gin.New()
		r.POST("/chats", h.CreateChat)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/chats", bytes.NewBufferString("{bad"))
		req.Header.Set("X-User-ID", "u1")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("bad json -> %d", w.Code)
		}
	}

	// Success -> 201, title trimmed
	{
		db := newChatDB(t)
		svc := services.NewChatService(db, testChatRepo{})
		h := New(svc, stubMsgSvcChat{}, stubFBSvcChat{})
		r := gin.New()
		r.POST("/chats", h.CreateChat)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/chats", bytes.NewBufferString(`{"title":"   Hello  "}`))
		req.Header.Set("X-User-ID", "u1")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("create -> %d body=%s", w.Code, w.Body.String())
		}
		var out domain.Chat
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("json: %v", err)
		}
		if out.UserID != "u1" || out.Title != "Hello" {
			t.Fatalf("unexpected chat: %#v", out)
		}
	}

	// Internal error -> 500
	{
		errSvc := stubChatSvcChat{
			create: func(ctx context.Context, u, t string) (*domain.Chat, error) {
				return nil, gorm.ErrInvalidField
			},
		}
		h := New(errSvc, stubMsgSvcChat{}, stubFBSvcChat{})
		r := gin.New()
		r.POST("/chats", h.CreateChat)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/chats", bytes.NewBufferString(`{"title":"X"}`))
		req.Header.Set("X-User-ID", "uX")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("internal -> %d", w.Code)
		}
	}
}

// ---------- ListChats ----------

func TestListChats_ETag304_and_SuccessPage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newChatDB(t)
	repoShim := testChatRepo{}
	svc := services.NewChatService(db, repoShim)
	h := New(svc, stubMsgSvcChat{}, stubFBSvcChat{})

	// Seed chats for user u1
	now := time.Now().UTC()
	c1 := &domain.Chat{ID: uuid.NewString(), UserID: "u1", Title: "A", CreatedAt: now, UpdatedAt: now}
	c2 := &domain.Chat{ID: uuid.NewString(), UserID: "u1", Title: "B", CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second)}
	if err := db.Create(c1).Error; err != nil {
		t.Fatalf("seed c1: %v", err)
	}
	if err := db.Create(c2).Error; err != nil {
		t.Fatalf("seed c2: %v", err)
	}

	r := gin.New()
	r.GET("/chats", h.ListChats)

	// Compute expected ETag
	count, maxTS, err := repo.ChatsStats(context.Background(), db, "u1")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	var ts int64
	if maxTS != nil {
		ts = maxTS.Unix()
	}
	etag := fmt.Sprintf(`W/"chats:%s:%d:%d"`, "u1", count, ts)

	// 304 path
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/chats", nil)
	req.Header.Set("X-User-ID", "u1")
	req.Header.Set("If-None-Match", etag)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotModified {
		t.Fatalf("etag 304 -> %d", w.Code)
	}

	// 200 success with pagination
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/chats?page=1&page_size=1", nil)
	req.Header.Set("X-User-ID", "u1")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list 200 -> %d body=%s", w.Code, w.Body.String())
	}
	var out ListChatsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("json: %v", err)
	}
	if out.Pagination.Page != 1 || out.Pagination.PageSize != 1 || out.Pagination.Total != count {
		t.Fatalf("pagination mismatch: %#v", out.Pagination)
	}
	if out.Pagination.TotalPages != 2 || out.Pagination.HasNext != true {
		t.Fatalf("pages/hasnext mismatch: %#v", out.Pagination)
	}
	if len(out.Chats) != 1 {
		t.Fatalf("expected 1 chat on page 1")
	}
}

// ---------- UpdateChatTitle ----------

func TestUpdateChatTitle_UUID_Binding_Success_NotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// bad UUID
	{
		h := New(stubChatSvcChat{}, stubMsgSvcChat{}, stubFBSvcChat{})
		r := gin.New()
		r.PUT("/chats/:id/title", h.UpdateChatTitle)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/chats/not-uuid/title", bytes.NewBufferString(`{"title":"x"}`))
		req.Header.Set("X-User-ID", "u1")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("uuid 400 -> %d", w.Code)
		}
	}

	// empty title -> 400
	{
		h := New(stubChatSvcChat{}, stubMsgSvcChat{}, stubFBSvcChat{})
		r := gin.New()
		r.PUT("/chats/:id/title", h.UpdateChatTitle)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/chats/"+uuid.NewString()+"/title", bytes.NewBufferString(`{"title":"   "}`))
		req.Header.Set("X-User-ID", "u1")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("empty title 400 -> %d", w.Code)
		}
	}

	// success 204, ensure args passed to service
	{
		var got struct{ uid, id, title string }
		okSvc := stubChatSvcChat{
			updateTit: func(ctx context.Context, u, id, t string) error {
				got.uid, got.id, got.title = u, id, t
				return nil
			},
		}
		h := New(okSvc, stubMsgSvcChat{}, stubFBSvcChat{})
		r := gin.New()
		r.PUT("/chats/:id/title", h.UpdateChatTitle)

		chatID := uuid.NewString()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/chats/"+chatID+"/title", bytes.NewBufferString(`{"title":"New Name"}`))
		req.Header.Set("X-User-ID", "U-9")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNoContent {
			t.Fatalf("204 -> %d", w.Code)
		}
		if got.uid != "U-9" || got.id != chatID || got.title != "New Name" {
			t.Fatalf("service args mismatch: %+v", got)
		}
	}

	// not found / any error -> 404
	{
		errSvc := stubChatSvcChat{
			updateTit: func(context.Context, string, string, string) error { return gorm.ErrRecordNotFound },
		}
		h := New(errSvc, stubMsgSvcChat{}, stubFBSvcChat{})
		r := gin.New()
		r.PUT("/chats/:id/title", h.UpdateChatTitle)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/chats/"+uuid.NewString()+"/title", bytes.NewBufferString(`{"title":"X"}`))
		req.Header.Set("X-User-ID", "u1")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("not found -> %d", w.Code)
		}
	}
}

func TestListChats_SkipETagPrecheck_And_ListError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Use the stub service (not *services.ChatService) so db==nil → ETag pre-check is skipped.
	svc := stubChatSvcChat{
		listPage: func(ctx context.Context, u string, p, ps int) ([]domain.Chat, int64, error) {
			return nil, 0, gorm.ErrInvalidField
		},
	}
	h := New(svc, stubMsgSvcChat{}, stubFBSvcChat{})

	r := gin.New()
	r.GET("/chats", h.ListChats)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/chats?page=1&page_size=5", nil)
	req.Header.Set("X-User-ID", "uX")
	// Provide a bogus If-None-Match to also exercise the inm != "" && inm != etag path
	req.Header.Set("If-None-Match", `W/"nope"`)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on list error; got %d body=%s", w.Code, w.Body.String())
	}
}

func TestListChats_EmptyState_SetsETag_WithZeroTS(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Real service with migrated DB, but no chats for this user → count=0, maxTS=nil.
	db := newChatDB(t)
	svc := services.NewChatService(db, testChatRepo{})
	h := New(svc, stubMsgSvcChat{}, stubFBSvcChat{})

	r := gin.New()
	r.GET("/chats", h.ListChats)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/chats", nil)
	req.Header.Set("X-User-ID", "u2") // user with no chats
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on empty list; got %d body=%s", w.Code, w.Body.String())
	}
	if et := w.Header().Get("ETag"); et != `W/"chats:u2:0:0"` {
		t.Fatalf(`expected ETag W/"chats:u2:0:0", got %q`, et)
	}

	var out ListChatsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("json: %v", err)
	}
	if out.Pagination.Total != 0 || out.Pagination.TotalPages != 0 || out.Pagination.HasNext {
		t.Fatalf("unexpected pagination: %#v", out.Pagination)
	}
}
