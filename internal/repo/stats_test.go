package repo

import (
	"context"
	"fmt"
	"testing"
	"time"

	sqlite "github.com/glebarez/sqlite" // pure-Go SQLite (no CGO)
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/tbourn/go-chat-backend/internal/domain"
)

func newTestDB(t *testing.T, migrate ...any) *gorm.DB {
	t.Helper()
	// Unique DB per test to avoid schema leaking across tests.
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
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

func TestChatsStats_CountError_NoTable(t *testing.T) {
	db := newTestDB(t /* no migrations */)
	_, _, err := ChatsStats(context.Background(), db, "u1")
	if err == nil {
		t.Fatalf("expected error due to missing chats table")
	}
}

func TestChatsStats_ZeroRows(t *testing.T) {
	db := newTestDB(t, &domain.Chat{})
	count, maxAt, err := ChatsStats(context.Background(), db, "u1")
	if err != nil {
		t.Fatalf("ChatsStats error: %v", err)
	}
	if count != 0 || maxAt != nil {
		t.Fatalf("expected (0, nil), got (%d, %v)", count, maxAt)
	}
}

func TestChatsStats_Success_FilterAndMax(t *testing.T) {
	db := newTestDB(t, &domain.Chat{})

	// Seed chats for two users; ensure UpdatedAt is exactly what we set.
	t1 := time.Date(2025, 1, 2, 15, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 3, 4, 10, 30, 0, 0, time.UTC) // max for u1
	t3 := time.Date(2025, 2, 1, 9, 0, 0, 0, time.UTC)   // for other user

	c1 := &domain.Chat{ID: "c1", UserID: "u1", Title: "a", CreatedAt: t1, UpdatedAt: t1}
	c2 := &domain.Chat{ID: "c2", UserID: "u1", Title: "b", CreatedAt: t2, UpdatedAt: t2}
	c3 := &domain.Chat{ID: "c3", UserID: "u2", Title: "x", CreatedAt: t3, UpdatedAt: t3}

	if err := db.Create(c1).Error; err != nil {
		t.Fatalf("seed c1: %v", err)
	}
	if err := db.Create(c2).Error; err != nil {
		t.Fatalf("seed c2: %v", err)
	}
	if err := db.Create(c3).Error; err != nil {
		t.Fatalf("seed c3: %v", err)
	}

	count, maxAt, err := ChatsStats(context.Background(), db, "u1")
	if err != nil {
		t.Fatalf("ChatsStats error: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected count 2, got %d", count)
	}
	if maxAt == nil || !maxAt.Equal(t2) {
		t.Fatalf("expected maxUpdatedAt %v, got %v", t2, maxAt)
	}
}

// Force the second query (SELECT updated_at ...) to fail by renaming the column.
func TestChatsStats_SelectLatest_ErrorPath(t *testing.T) {
	db := newTestDB(t, &domain.Chat{})

	// Seed at least one row so count > 0
	now := time.Now().UTC()
	if err := db.Create(&domain.Chat{
		ID:        "cx",
		UserID:    "uerr",
		Title:     "x",
		CreatedAt: now,
		UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	// Break the follow-up select by removing/renaming updated_at.
	if err := db.Exec(`ALTER TABLE chats RENAME COLUMN updated_at TO updated_at_old`).Error; err != nil {
		t.Fatalf("rename column: %v", err)
	}

	_, _, err := ChatsStats(context.Background(), db, "uerr")
	if err == nil {
		t.Fatalf("expected error from latest-updated select after column rename")
	}
}

func TestMessagesStats_CountError_NoTable(t *testing.T) {
	db := newTestDB(t /* no migrations */)
	_, _, err := MessagesStats(context.Background(), db, "c1")
	if err == nil {
		t.Fatalf("expected error due to missing messages table")
	}
}

func TestMessagesStats_ZeroRows(t *testing.T) {
	db := newTestDB(t, &domain.Message{})
	count, maxAt, err := MessagesStats(context.Background(), db, "c1")
	if err != nil {
		t.Fatalf("MessagesStats error: %v", err)
	}
	if count != 0 || maxAt != nil {
		t.Fatalf("expected (0, nil), got (%d, %v)", count, maxAt)
	}
}

func TestMessagesStats_Success_FilterAndMax(t *testing.T) {
	db := newTestDB(t, &domain.Message{})

	// Seed messages in two chats with precise UpdatedAt.
	t1 := time.Date(2025, 4, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 4, 1, 12, 5, 0, 0, time.UTC) // max for cX
	t3 := time.Date(2025, 4, 2, 8, 0, 0, 0, time.UTC)  // other chat

	m1 := &domain.Message{ID: "m1", ChatID: "cX", Role: "user", Content: "hi", CreatedAt: t1, UpdatedAt: t1}
	m2 := &domain.Message{ID: "m2", ChatID: "cX", Role: "assistant", Content: "hey", CreatedAt: t2, UpdatedAt: t2}
	m3 := &domain.Message{ID: "m3", ChatID: "cY", Role: "user", Content: "yo", CreatedAt: t3, UpdatedAt: t3}

	if err := db.Create(m1).Error; err != nil {
		t.Fatalf("seed m1: %v", err)
	}
	if err := db.Create(m2).Error; err != nil {
		t.Fatalf("seed m2: %v", err)
	}
	if err := db.Create(m3).Error; err != nil {
		t.Fatalf("seed m3: %v", err)
	}

	count, maxAt, err := MessagesStats(context.Background(), db, "cX")
	if err != nil {
		t.Fatalf("MessagesStats error: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected count 2, got %d", count)
	}
	if maxAt == nil || !maxAt.Equal(t2) {
		t.Fatalf("expected maxUpdatedAt %v, got %v", t2, maxAt)
	}
}

// Force the second query (SELECT updated_at ...) to fail by renaming the column.
func TestMessagesStats_SelectLatest_ErrorPath(t *testing.T) {
	db := newTestDB(t, &domain.Message{})

	// Seed at least one row so count > 0
	now := time.Now().UTC()
	if err := db.Create(&domain.Message{
		ID:        "mx",
		ChatID:    "cerr",
		Role:      "user",
		Content:   "x",
		CreatedAt: now,
		UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed msg: %v", err)
	}

	// Break the follow-up select by removing/renaming updated_at.
	if err := db.Exec(`ALTER TABLE messages RENAME COLUMN updated_at TO updated_at_old`).Error; err != nil {
		t.Fatalf("rename column: %v", err)
	}

	_, _, err := MessagesStats(context.Background(), db, "cerr")
	if err == nil {
		t.Fatalf("expected error from latest-updated select after column rename")
	}
}
