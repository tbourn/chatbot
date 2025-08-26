package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/tbourn/go-chat-backend/internal/domain"
	"github.com/tbourn/go-chat-backend/internal/services"
)

// ---- stubs to satisfy handlers.New() dependencies ----

type stubChatSvcFeedback struct{}

func (stubChatSvcFeedback) Create(context.Context, string, string) (*domain.Chat, error) {
	return nil, nil
}
func (stubChatSvcFeedback) List(context.Context, string) ([]domain.Chat, error) { return nil, nil }
func (stubChatSvcFeedback) ListPage(context.Context, string, int, int) ([]domain.Chat, int64, error) {
	return nil, 0, nil
}
func (stubChatSvcFeedback) UpdateTitle(context.Context, string, string, string) error { return nil }

type stubMsgSvcFeedback struct {
	answer func(ctx context.Context, userID, chatID, prompt string) (*domain.Message, error)
	list   func(ctx context.Context, chatID string, page, pageSize int) ([]domain.Message, int64, error)
}

func (s stubMsgSvcFeedback) Answer(ctx context.Context, userID, chatID, prompt string) (*domain.Message, error) {
	if s.answer != nil {
		return s.answer(ctx, userID, chatID, prompt)
	}
	return nil, nil
}

func (s stubMsgSvcFeedback) ListPage(ctx context.Context, chatID string, page, pageSize int) ([]domain.Message, int64, error) {
	if s.list != nil {
		return s.list(ctx, chatID, page, pageSize)
	}
	return nil, 0, nil
}

type stubFBSvc struct {
	fn func(ctx context.Context, userID, messageID string, value int) error
}

func (s stubFBSvc) Leave(ctx context.Context, userID, messageID string, value int) error {
	return s.fn(ctx, userID, messageID, value)
}

// ---- tests ----

func TestLeaveFeedback_BindingError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fb := stubFBSvc{fn: func(ctx context.Context, userID, messageID string, value int) error {
		t.Fatalf("service should not be called on binding error")
		return nil
	}}
	h := New(stubChatSvcFeedback{}, stubMsgSvcFeedback{}, fb)

	r := gin.New()
	r.POST("/messages/:id/feedback", h.LeaveFeedback)

	w := httptest.NewRecorder()
	// Missing "value" or invalid value → binding error
	req := httptest.NewRequest(http.MethodPost, "/messages/m1/feedback", bytes.NewBufferString(`{"value":0}`))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("binding error expected 400, got %d", w.Code)
	}
	var er ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &er); err != nil {
		t.Fatalf("json: %v", err)
	}
	if er.Message == "" {
		t.Fatalf("expected error message in response")
	}
}

func TestLeaveFeedback_ErrorMappings(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"not_found", services.ErrMessageNotFound, http.StatusNotFound},
		{"invalid", services.ErrInvalidFeedback, http.StatusBadRequest},
		{"forbidden", services.ErrForbiddenFeedback, http.StatusForbidden},
		{"duplicate", services.ErrDuplicateFeedback, http.StatusConflict},
		{"internal", context.DeadlineExceeded, http.StatusInternalServerError}, // any other error
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			fb := stubFBSvc{fn: func(ctx context.Context, userID, messageID string, value int) error {
				// ensure userID and messageID are passed through
				if userID != "u-123" {
					t.Fatalf("expected userID u-123, got %q", userID)
				}
				if messageID != "m-xyz" {
					t.Fatalf("expected messageID m-xyz, got %q", messageID)
				}
				if value != 1 {
					t.Fatalf("expected value 1, got %d", value)
				}
				return tc.err
			}}
			h := New(stubChatSvcFeedback{}, stubMsgSvcFeedback{}, fb)

			r := gin.New()
			r.POST("/messages/:id/feedback", h.LeaveFeedback)

			body := bytes.NewBufferString(`{"value":1}`)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/messages/m-xyz/feedback", body)
			req.Header.Set("X-User-ID", "u-123")
			r.ServeHTTP(w, req)

			if w.Code != tc.wantStatus {
				t.Fatalf("status=%d, want %d. body=%s", w.Code, tc.wantStatus, w.Body.String())
			}
			// For error responses, verify the envelope shape
			if tc.wantStatus != http.StatusNoContent {
				var er ErrorResponse
				if err := json.Unmarshal(w.Body.Bytes(), &er); err != nil {
					t.Fatalf("json: %v", err)
				}
				if er.Code == "" || er.Message == "" {
					t.Fatalf("error envelope missing fields: %+v", er)
				}
			}
		})
	}
}

func TestLeaveFeedback_Success204(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var got struct {
		user string
		id   string
		val  int
	}
	fb := stubFBSvc{fn: func(ctx context.Context, userID, messageID string, value int) error {
		got.user = userID
		got.id = messageID
		got.val = value
		return nil
	}}
	h := New(stubChatSvcFeedback{}, stubMsgSvcFeedback{}, fb)

	r := gin.New()
	r.POST("/messages/:id/feedback", h.LeaveFeedback)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/messages/m-123/feedback", bytes.NewBufferString(`{"value":-1}`))
	req.Header.Set("X-User-ID", "user-42")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("expected empty body for 204")
	}
	if got.user != "user-42" || got.id != "m-123" || got.val != -1 {
		t.Fatalf("service args mismatch: %+v", got)
	}
}
