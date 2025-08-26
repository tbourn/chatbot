package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	sqlite "github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/tbourn/go-chat-backend/internal/domain"
	"github.com/tbourn/go-chat-backend/internal/repo"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:feedbacksvc_%s?mode=memory&cache=shared", uuid.NewString())

	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.Exec("PRAGMA foreign_keys=ON;")
	if err := db.AutoMigrate(&domain.Chat{}, &domain.Message{}, &domain.Feedback{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

func TestFeedback_Leave_InvalidValue(t *testing.T) {
	db := newTestDB(t)
	svc := &FeedbackService{DB: db}

	err := svc.Leave(context.Background(), "u1", "m1", 0) // not -1 or 1
	if !errors.Is(err, ErrInvalidFeedback) {
		t.Fatalf("expected ErrInvalidFeedback, got %v", err)
	}
}

func TestFeedback_Leave_MessageNotFound(t *testing.T) {
	db := newTestDB(t)
	svc := &FeedbackService{DB: db}

	// no messages seeded -> GetMessage should return not found
	err := svc.Leave(context.Background(), "u1", "missing", 1)
	if !errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("expected ErrMessageNotFound, got %v", err)
	}
}

func TestFeedback_Leave_ChatNotOwned(t *testing.T) {
	db := newTestDB(t)

	// Chat owned by a different user
	chat := &domain.Chat{ID: "c1", UserID: "ownerA", Title: "t"}
	if err := db.Create(chat).Error; err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	// Assistant message in that chat
	msg := &domain.Message{ID: "m1", ChatID: chat.ID, Role: "assistant", Content: "hi"}
	if err := db.Create(msg).Error; err != nil {
		t.Fatalf("seed msg: %v", err)
	}

	svc := &FeedbackService{DB: db}
	err := svc.Leave(context.Background(), "uX", msg.ID, 1) // uX does NOT own c1
	if !errors.Is(err, ErrForbiddenFeedback) {
		t.Fatalf("expected ErrForbiddenFeedback (not owner), got %v", err)
	}
}

func TestFeedback_Leave_NotAssistantRole(t *testing.T) {
	db := newTestDB(t)

	chat := &domain.Chat{ID: "c2", UserID: "u1", Title: "t"}
	if err := db.Create(chat).Error; err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	// User message (not assistant)
	msg := &domain.Message{ID: "m2", ChatID: chat.ID, Role: "user", Content: "hello"}
	if err := db.Create(msg).Error; err != nil {
		t.Fatalf("seed msg: %v", err)
	}

	svc := &FeedbackService{DB: db}
	err := svc.Leave(context.Background(), "u1", msg.ID, -1)
	if !errors.Is(err, ErrForbiddenFeedback) {
		t.Fatalf("expected ErrForbiddenFeedback (role=user), got %v", err)
	}
}

func TestFeedback_Leave_DuplicateFeedback(t *testing.T) {
	db := newTestDB(t)

	chat := &domain.Chat{ID: "c3", UserID: "u1", Title: "t"}
	if err := db.Create(chat).Error; err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	msg := &domain.Message{ID: "m3", ChatID: chat.ID, Role: "assistant", Content: "answer"}
	if err := db.Create(msg).Error; err != nil {
		t.Fatalf("seed msg: %v", err)
	}

	svc := &FeedbackService{DB: db}

	// First leave: should succeed
	if err := svc.Leave(context.Background(), "u1", msg.ID, 1); err != nil {
		t.Fatalf("first Leave failed: %v", err)
	}

	// Second leave (same user + message): should trip unique constraint
	err := svc.Leave(context.Background(), "u1", msg.ID, -1)
	if !errors.Is(err, ErrDuplicateFeedback) {
		t.Fatalf("expected ErrDuplicateFeedback, got %v", err)
	}
}

func TestFeedback_Leave_Success(t *testing.T) {
	db := newTestDB(t)

	chat := &domain.Chat{ID: "c4", UserID: "u9", Title: "t"}
	if err := db.Create(chat).Error; err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	msg := &domain.Message{ID: "m4", ChatID: chat.ID, Role: "assistant", Content: "ok"}
	if err := db.Create(msg).Error; err != nil {
		t.Fatalf("seed msg: %v", err)
	}

	svc := &FeedbackService{DB: db}
	if err := svc.Leave(context.Background(), "u9", msg.ID, -1); err != nil {
		t.Fatalf("Leave success returned error: %v", err)
	}

	// Verify a feedback row exists for (message_id, user_id)
	var got domain.Feedback
	if err := db.Where("message_id = ? AND user_id = ?", msg.ID, "u9").First(&got).Error; err != nil {
		t.Fatalf("load feedback: %v", err)
	}
	if got.Value != -1 {
		t.Fatalf("expected value -1, got %d", got.Value)
	}
	// sanity: CreatedAt is set (allowing slight time skew)
	if got.CreatedAt.IsZero() || time.Since(got.CreatedAt) > time.Minute {
		t.Fatalf("unexpected CreatedAt: %v", got.CreatedAt)
	}
}

func Test_isNotFound_and_isDuplicate(t *testing.T) {
	// repo-level sentinel should be detected
	if !isNotFound(repo.ErrNotFound) {
		t.Fatalf("isNotFound(repo.ErrNotFound) = false; want true")
	}
	// unrelated error -> false
	if isNotFound(errors.New("nope")) {
		t.Fatalf("isNotFound(random) = true; want false")
	}

	// string-based duplicate patterns
	if !isDuplicate(errors.New("UNIQUE constraint failed: feedbacks.message_id, feedbacks.user_id")) {
		t.Fatalf("isDuplicate(sqlite unique) = false; want true")
	}
	if !isDuplicate(errors.New("duplicate key value violates unique constraint \"uniq_msg_user\"")) {
		t.Fatalf("isDuplicate(pg duplicate) = false; want true")
	}
	if isDuplicate(errors.New("some other error")) {
		t.Fatalf("isDuplicate(other) = true; want false")
	}
}

// Helper: open an in-memory DB and migrate only selected tables.
// Use this to induce specific unexpected DB errors.
func newTestDBPartial(t *testing.T, migrateChat, migrateMessage, migrateFeedback bool) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:feedbacksvc_partial_%s?mode=memory&cache=shared", uuid.NewString())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.Exec("PRAGMA foreign_keys=ON;")

	if migrateChat {
		if err := db.AutoMigrate(&domain.Chat{}); err != nil {
			t.Fatalf("automigrate chat: %v", err)
		}
	}
	if migrateMessage {
		if err := db.AutoMigrate(&domain.Message{}); err != nil {
			t.Fatalf("automigrate message: %v", err)
		}
	}
	if migrateFeedback {
		if err := db.AutoMigrate(&domain.Feedback{}); err != nil {
			t.Fatalf("automigrate feedback: %v", err)
		}
	}
	return db
}

// Force a non-not-found error during GetMessage via a GORM Query callback.
// This hits the "unexpected DB error" path inside Leave() right after GetMessage.
func TestFeedback_Leave_GetMessageUnexpectedDBError(t *testing.T) {
	db := newTestDB(t) // migrate all tables (chat, message, feedback)

	// Inject a query-time error ONLY for the "messages" table.
	if err := db.Callback().Query().Before("gorm:query").Register("force_err_on_messages", func(tx *gorm.DB) {
		if tx.Statement != nil && strings.Contains(tx.Statement.Table, "messages") {
			tx.AddError(errors.New("forced-getmessage-error"))
		}
	}); err != nil {
		t.Fatalf("register query callback: %v", err)
	}

	svc := &FeedbackService{DB: db}
	err := svc.Leave(context.Background(), "u1", "m-any", 1)
	if err == nil {
		t.Fatalf("expected error from forced query callback; got nil")
	}
	// MUST NOT be mapped to ErrMessageNotFound — it should bubble the raw error.
	if errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("unexpected mapping to ErrMessageNotFound: %v", err)
	}
}

// 2) Force unexpected DB error on Create (feedbacks table missing) –
// should bubble the raw DB error (not duplicate/forbidden/etc).
func TestFeedback_Leave_CreateUnexpectedDBError(t *testing.T) {
	// Migrate chat + message, but NOT feedback → insert hits "no such table".
	db := newTestDBPartial(t, true /*chat*/, true /*message*/, false /*feedback*/)

	chat := &domain.Chat{ID: "cX", UserID: "uX", Title: "ok"}
	if err := db.Create(chat).Error; err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	msg := &domain.Message{ID: "mX", ChatID: chat.ID, Role: "assistant", Content: "ok"}
	if err := db.Create(msg).Error; err != nil {
		t.Fatalf("seed msg: %v", err)
	}

	svc := &FeedbackService{DB: db}
	err := svc.Leave(context.Background(), "uX", msg.ID, 1)
	if err == nil {
		t.Fatalf("expected error when feedbacks table is missing; got nil")
	}
	// Not a service sentinel; it should be the raw DB error.
	if errors.Is(err, ErrDuplicateFeedback) || errors.Is(err, ErrForbiddenFeedback) ||
		errors.Is(err, ErrInvalidFeedback) || errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("unexpected mapping to service sentinel error: %v", err)
	}
}

// 3) Explicitly exercise gorm.ErrDuplicatedKey branch via a GORM callback.
func TestFeedback_Leave_DuplicateFeedback_GormErrDuplicatedKey(t *testing.T) {
	db := newTestDBPartial(t, true, true, true)

	chat := &domain.Chat{ID: "cY", UserID: "uY", Title: "t"}
	if err := db.Create(chat).Error; err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	msg := &domain.Message{ID: "mY", ChatID: chat.ID, Role: "assistant", Content: "ok"}
	if err := db.Create(msg).Error; err != nil {
		t.Fatalf("seed msg: %v", err)
	}

	// Register AFTER seeding so it only affects feedback inserts.
	if err := db.Callback().Create().Before("gorm:create").Register("force_dup_for_feedback", func(tx *gorm.DB) {
		// Narrow to feedback table only.
		if tx.Statement != nil && strings.Contains(tx.Statement.Table, "feedback") {
			tx.AddError(gorm.ErrDuplicatedKey)
		}
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}

	svc := &FeedbackService{DB: db}
	got := svc.Leave(context.Background(), "uY", msg.ID, 1)
	if !errors.Is(got, ErrDuplicateFeedback) {
		t.Fatalf("expected ErrDuplicateFeedback via gorm.ErrDuplicatedKey, got %v", got)
	}
}
