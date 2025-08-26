package repo

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	sqlite "github.com/glebarez/sqlite" // pure-Go SQLite
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/tbourn/go-chat-backend/internal/domain"
)

func newChatRepoDB(t *testing.T, migrate ...any) *gorm.DB {
	t.Helper()

	dsn := filepath.Join(t.TempDir(), fmt.Sprintf("chat_repo_test_%d.db", time.Now().UnixNano()))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	// Ensure the file handle is released before TempDir cleanup (Windows needs this).
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

func TestCreateChat_Error_NoTable(t *testing.T) {
	db := newChatRepoDB(t /* no migrations */)
	chat, err := CreateChat(context.Background(), db, "u1", "t")
	if err == nil || chat != nil {
		t.Fatalf("expected error creating without table, got chat=%v err=%v", chat, err)
	}
}

func TestCreateChat_Success_PersistsAndSetsFields(t *testing.T) {
	db := newChatRepoDB(t, &domain.Chat{})

	start := time.Now().UTC().Add(-time.Minute)
	chat, err := CreateChat(context.Background(), db, "u1", "My Chat")
	if err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	if chat.ID == "" || chat.UserID != "u1" || chat.Title != "My Chat" {
		t.Fatalf("unexpected Chat fields: %+v", chat)
	}
	if chat.CreatedAt.Before(start) {
		t.Fatalf("CreatedAt seems unset/really old: %v", chat.CreatedAt)
	}
	// round-trip
	var got domain.Chat
	if err := db.First(&got, "id = ?", chat.ID).Error; err != nil {
		t.Fatalf("load created chat: %v", err)
	}
	if got.UserID != "u1" || got.Title != "My Chat" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestListChats_OrderDescendingAndFilter(t *testing.T) {
	db := newChatRepoDB(t, &domain.Chat{})

	// Seed with known CreatedAt so order is deterministic.
	t1 := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC) // oldest
	t2 := t1.Add(1 * time.Hour)
	t3 := t2.Add(1 * time.Hour) // newest for u1
	c1 := domain.Chat{ID: "c1", UserID: "u1", Title: "A", CreatedAt: t1}
	c2 := domain.Chat{ID: "c2", UserID: "u1", Title: "B", CreatedAt: t2}
	c3 := domain.Chat{ID: "c3", UserID: "u1", Title: "C", CreatedAt: t3}
	cx := domain.Chat{ID: "cx", UserID: "u2", Title: "Other", CreatedAt: t2}

	for _, c := range []domain.Chat{c1, c2, c3, cx} {
		if err := db.Create(&c).Error; err != nil {
			t.Fatalf("seed %s: %v", c.ID, err)
		}
	}

	list, err := ListChats(context.Background(), db, "u1")
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 chats for u1, got %d", len(list))
	}
	// Must be descending by CreatedAt: c3, c2, c1
	if list[0].ID != "c3" || list[1].ID != "c2" || list[2].ID != "c1" {
		t.Fatalf("unexpected order: %#v", list)
	}
}

func TestCountChats_Error_NoTable(t *testing.T) {
	db := newChatRepoDB(t /* no migrations */)
	if _, err := CountChats(context.Background(), db, "u1"); err == nil {
		t.Fatalf("expected error when table missing")
	}
}

func TestCountChats_Success(t *testing.T) {
	db := newChatRepoDB(t, &domain.Chat{})
	// u1 has 2, u2 has 1
	if err := db.Create(&domain.Chat{ID: "a", UserID: "u1", Title: "t"}).Error; err != nil {
		t.Fatalf("seed a: %v", err)
	}
	if err := db.Create(&domain.Chat{ID: "b", UserID: "u1", Title: "t"}).Error; err != nil {
		t.Fatalf("seed b: %v", err)
	}
	if err := db.Create(&domain.Chat{ID: "x", UserID: "u2", Title: "t"}).Error; err != nil {
		t.Fatalf("seed x: %v", err)
	}

	total, err := CountChats(context.Background(), db, "u1")
	if err != nil {
		t.Fatalf("CountChats: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected 2, got %d", total)
	}
}

func TestListChatsPage_PaginationAndOrder(t *testing.T) {
	db := newChatRepoDB(t, &domain.Chat{})

	// Seed 5 chats with increasing CreatedAt, so desc order is 5,4,3,2,1
	base := time.Date(2025, 2, 1, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= 5; i++ {
		c := domain.Chat{
			ID:        string(rune('a' + i - 1)),
			UserID:    "u1",
			Title:     "t",
			CreatedAt: base.Add(time.Duration(i) * time.Second),
		}
		if err := db.Create(&c).Error; err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	// Offset 1, limit 2 => should return the 2nd and 3rd newest => IDs 'd','c'
	page, err := ListChatsPage(context.Background(), db, "u1", 1, 2)
	if err != nil {
		t.Fatalf("ListChatsPage: %v", err)
	}
	if len(page) != 2 || page[0].ID != "d" || page[1].ID != "c" {
		t.Fatalf("unexpected page slice: %+v", page)
	}
}

func TestGetChat_FoundAndNotFound(t *testing.T) {
	db := newChatRepoDB(t, &domain.Chat{})

	// Not found
	if _, err := GetChat(context.Background(), db, "nope", "u1"); err == nil {
		t.Fatalf("expected ErrRecordNotFound for missing chat")
	}

	// Insert & fetch
	c := &domain.Chat{ID: "cid", UserID: "owner", Title: "x"}
	if err := db.Create(c).Error; err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	got, err := GetChat(context.Background(), db, "cid", "owner")
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if got.ID != "cid" || got.UserID != "owner" {
		t.Fatalf("unexpected chat: %+v", got)
	}
}

func TestUpdateChatTitle_SuccessAndNotFound(t *testing.T) {
	db := newChatRepoDB(t, &domain.Chat{})

	// Seed one chat
	c := &domain.Chat{ID: "c1", UserID: "u1", Title: "old"}
	if err := db.Create(c).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Success
	if err := UpdateChatTitle(context.Background(), db, "c1", "u1", "new"); err != nil {
		t.Fatalf("UpdateChatTitle: %v", err)
	}
	var got domain.Chat
	if err := db.First(&got, "id = ?", "c1").Error; err != nil {
		t.Fatalf("load updated: %v", err)
	}
	if got.Title != "new" {
		t.Fatalf("expected title 'new', got %q", got.Title)
	}

	// Not found (wrong user or id) -> gorm.ErrRecordNotFound
	if err := UpdateChatTitle(context.Background(), db, "c1", "other", "x"); err == nil {
		t.Fatalf("expected ErrRecordNotFound when user mismatches")
	}
	if err := UpdateChatTitle(context.Background(), db, "missing", "u1", "x"); err == nil {
		t.Fatalf("expected ErrRecordNotFound when id missing")
	}
}

func TestUpdateChatTitle_Error_NoTable(t *testing.T) {
	db := newChatRepoDB(t /* no migrations */)

	err := UpdateChatTitle(context.Background(), db, "anyid", "anyuser", "newtitle")
	if err == nil {
		t.Fatalf("expected error when table does not exist")
	}
}
