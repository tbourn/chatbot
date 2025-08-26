package repo

import (
	"context"
	"testing"
	"time"

	sqlite "github.com/glebarez/sqlite" // pure-Go SQLite (no CGO)
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/tbourn/go-chat-backend/internal/domain"
)

func newFeedbackDB(t *testing.T, migrate ...any) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:feedbackrepo?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if len(migrate) > 0 {
		if err := db.AutoMigrate(migrate...); err != nil {
			t.Fatalf("automigrate: %v", err)
		}
	}
	return db
}

// Ensure unique index so duplicate is guaranteed to error even if the model
// doesn't already define it.
func ensureFeedbackUniqueIndex(t *testing.T, db *gorm.DB) {
	t.Helper()
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_feedback_msg_user ON feedbacks(message_id, user_id)`)
}

func TestCreateFeedback_Error_NoTable(t *testing.T) {
	db := newFeedbackDB(t /* no migrations */)
	err := CreateFeedback(context.Background(), db, "m1", "u1", 1)
	if err == nil {
		t.Fatalf("expected error when feedbacks table is missing")
	}
}

func TestCreateFeedback_Success_InsertsRow(t *testing.T) {
	db := newFeedbackDB(t, &domain.Message{}, &domain.Feedback{})

	// Seed a message in case FK constraints exist in your schema
	if err := db.Create(&domain.Message{ID: "m1", ChatID: "c1", Role: "assistant", Content: "ok"}).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}

	ctx := context.WithValue(context.Background(), "req", "123")
	start := time.Now().UTC()

	if err := CreateFeedback(ctx, db, "m1", "u1", -1); err != nil {
		t.Fatalf("CreateFeedback error: %v", err)
	}

	var got domain.Feedback
	if err := db.Where("message_id = ? AND user_id = ?", "m1", "u1").First(&got).Error; err != nil {
		t.Fatalf("load feedback: %v", err)
	}
	if got.ID == "" || got.MessageID != "m1" || got.UserID != "u1" || got.Value != -1 {
		t.Fatalf("unexpected feedback row: %+v", got)
	}
	if got.CreatedAt.IsZero() || !got.CreatedAt.After(start.Add(-time.Minute)) {
		t.Fatalf("CreatedAt not set reasonably: %v", got.CreatedAt)
	}
}

func TestCreateFeedback_Duplicate_ReturnsError(t *testing.T) {
	db := newFeedbackDB(t, &domain.Message{}, &domain.Feedback{})
	ensureFeedbackUniqueIndex(t, db)

	// seed message
	if err := db.Create(&domain.Message{ID: "mdup", ChatID: "c1", Role: "assistant", Content: "ok"}).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}

	ctx := context.Background()
	if err := CreateFeedback(ctx, db, "mdup", "u1", 1); err != nil {
		t.Fatalf("first CreateFeedback should succeed: %v", err)
	}
	// Same (message_id, user_id) → unique violation → repo should return raw DB error
	if err := CreateFeedback(ctx, db, "mdup", "u1", -1); err == nil {
		t.Fatalf("expected duplicate error on second insert")
	}
}
