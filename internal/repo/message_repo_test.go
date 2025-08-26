package repo

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	sqlite "github.com/glebarez/sqlite" // pure-Go SQLite (no CGO)
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/tbourn/go-chat-backend/internal/domain"
)

// test DB helper
func newMsgRepoDB(t *testing.T, migrate ...any) *gorm.DB {
	t.Helper()

	dsn := filepath.Join(t.TempDir(), fmt.Sprintf("msg_repo_%d.db", time.Now().UnixNano()))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if len(migrate) > 0 {
		if err := db.AutoMigrate(migrate...); err != nil {
			t.Fatalf("automigrate: %v", err)
		}
	}
	return db
}

func TestCreateMessage_InsertsAndStoresScore(t *testing.T) {
	db := newMsgRepoDB(t, &domain.Chat{}, &domain.Message{})

	// seed chat in case you enforce FK in your schema
	if err := db.Create(&domain.Chat{ID: "c1", UserID: "u1", Title: "t"}).Error; err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	score := 0.42
	msg, err := CreateMessage(db, "c1", "assistant", "hello", &score)
	if err != nil {
		t.Fatalf("CreateMessage error: %v", err)
	}
	if msg.ID == "" || msg.ChatID != "c1" || msg.Role != "assistant" || msg.Content != "hello" {
		t.Fatalf("unexpected message: %+v", msg)
	}
	if msg.Score == nil || *msg.Score != score {
		t.Fatalf("score not stored correctly: %+v", msg)
	}
	if msg.CreatedAt.IsZero() || time.Since(msg.CreatedAt) > time.Minute {
		t.Fatalf("CreatedAt not set reasonably: %v", msg.CreatedAt)
	}

	// read it back
	got, err := GetMessage(db, msg.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got.ID != msg.ID {
		t.Fatalf("roundtrip mismatch: %+v vs %+v", got, msg)
	}
}

func TestListMessages_OrderAndLimit(t *testing.T) {
	db := newMsgRepoDB(t, &domain.Message{})

	// craft deterministic ordering:
	// same CreatedAt for first two; ID "a" should come before "b"
	t0 := time.Date(2025, 7, 1, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Second)

	mA := domain.Message{ID: "a", ChatID: "c2", Role: "user", Content: "x", CreatedAt: t0}
	mB := domain.Message{ID: "b", ChatID: "c2", Role: "user", Content: "y", CreatedAt: t0}
	mZ := domain.Message{ID: "z", ChatID: "c2", Role: "assistant", Content: "z", CreatedAt: t1}
	if err := db.Create(&mB).Error; err != nil { // insert out of order on purpose
		t.Fatalf("seed mB: %v", err)
	}
	if err := db.Create(&mA).Error; err != nil {
		t.Fatalf("seed mA: %v", err)
	}
	if err := db.Create(&mZ).Error; err != nil {
		t.Fatalf("seed mZ: %v", err)
	}

	// limit <= 0 → all
	all, err := ListMessages(db, "c2", 0)
	if err != nil {
		t.Fatalf("ListMessages(all) error: %v", err)
	}
	if len(all) != 3 || all[0].ID != "a" || all[1].ID != "b" || all[2].ID != "z" {
		t.Fatalf("unexpected order/all: %+v", all)
	}

	// limit > 0
	top2, err := ListMessages(db, "c2", 2)
	if err != nil {
		t.Fatalf("ListMessages(limit) error: %v", err)
	}
	if len(top2) != 2 || top2[0].ID != "a" || top2[1].ID != "b" {
		t.Fatalf("unexpected order/limit: %+v", top2)
	}
}

func TestCountMessages_Error_NoTable(t *testing.T) {
	db := newMsgRepoDB(t /* no migration for Message */)
	if _, err := CountMessages(db, "cx"); err == nil {
		t.Fatalf("expected error due to missing messages table")
	}
}

func TestCountMessages_Success(t *testing.T) {
	db := newMsgRepoDB(t, &domain.Message{})

	// two messages in cx, one in cy
	if err := db.Create(&domain.Message{ID: "m1", ChatID: "cx", Role: "user", Content: "1"}).Error; err != nil {
		t.Fatalf("seed m1: %v", err)
	}
	if err := db.Create(&domain.Message{ID: "m2", ChatID: "cx", Role: "assistant", Content: "2"}).Error; err != nil {
		t.Fatalf("seed m2: %v", err)
	}
	if err := db.Create(&domain.Message{ID: "m3", ChatID: "cy", Role: "user", Content: "3"}).Error; err != nil {
		t.Fatalf("seed m3: %v", err)
	}

	total, err := CountMessages(db, "cx")
	if err != nil {
		t.Fatalf("CountMessages error: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected 2, got %d", total)
	}
}

func TestListMessagesPage_Pagination(t *testing.T) {
	db := newMsgRepoDB(t, &domain.Message{})

	// five messages with ascending CreatedAt + IDs
	base := time.Date(2025, 7, 1, 11, 0, 0, 0, time.UTC)
	for i := 1; i <= 5; i++ {
		m := domain.Message{
			ID:        string(rune('a' + i - 1)),
			ChatID:    "c3",
			Role:      "user",
			Content:   "x",
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		}
		if err := db.Create(&m).Error; err != nil {
			t.Fatalf("seed m%d: %v", i, err)
		}
	}

	out, err := ListMessagesPage(db, "c3", 1, 2) // expect 2nd and 3rd in order
	if err != nil {
		t.Fatalf("ListMessagesPage error: %v", err)
	}
	if len(out) != 2 || out[0].ID != "b" || out[1].ID != "c" {
		t.Fatalf("unexpected page slice: %+v", out)
	}
}

func TestLeaveFeedback_InsertsRow(t *testing.T) {
	db := newMsgRepoDB(t, &domain.Message{}, &domain.Feedback{})

	// seed message to attach feedback to (in case of FK)
	m := &domain.Message{ID: "mfb", ChatID: "c4", Role: "assistant", Content: "ok"}
	if err := db.Create(m).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}

	if err := LeaveFeedback(db, "mfb", 1); err != nil {
		t.Fatalf("LeaveFeedback error: %v", err)
	}

	var got domain.Feedback
	if err := db.Where("message_id = ?", "mfb").First(&got).Error; err != nil {
		t.Fatalf("load feedback: %v", err)
	}
	if got.Value != 1 || got.ID == "" || got.CreatedAt.IsZero() {
		t.Fatalf("unexpected feedback: %+v", got)
	}
}

func TestGetMessage_FoundAndNotFound(t *testing.T) {
	db := newMsgRepoDB(t, &domain.Message{})

	// not found
	if _, err := GetMessage(db, "nope"); err == nil {
		t.Fatalf("expected gorm.ErrRecordNotFound")
	}

	// insert & get
	msg := &domain.Message{ID: "mid", ChatID: "c9", Role: "user", Content: "hi"}
	if err := db.Create(msg).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}
	got, err := GetMessage(db, "mid")
	if err != nil {
		t.Fatalf("GetMessage error: %v", err)
	}
	if got.ID != "mid" || got.ChatID != "c9" {
		t.Fatalf("unexpected message: %+v", got)
	}
}

// sanity: the repository funcs accept a *gorm.DB that may have context/tx set;
// ensure they work with a context-scoped DB too
func TestRepoWithContextHandles(t *testing.T) {
	db := newMsgRepoDB(t, &domain.Message{})
	ctx := context.WithValue(context.Background(), "k", "v")
	tdb := db.WithContext(ctx)

	if _, err := CreateMessage(tdb, "cX", "user", "hello", nil); err != nil {
		t.Fatalf("CreateMessage with context: %v", err)
	}
	if _, err := ListMessages(tdb, "cX", 10); err != nil {
		t.Fatalf("ListMessages with context: %v", err)
	}
	if _, err := CountMessages(tdb, "cX"); err != nil {
		t.Fatalf("CountMessages with context: %v", err)
	}
	if _, err := ListMessagesPage(tdb, "cX", 0, 1); err != nil {
		t.Fatalf("ListMessagesPage with context: %v", err)
	}
}
