package domain

import (
	"testing"
	"time"

	sqlite "github.com/glebarez/sqlite" // pure-Go SQLite (no CGO)
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newDomainDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:domain_models?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// Enforce FKs so cascades actually execute.
	db.Exec("PRAGMA foreign_keys=ON;")
	return db
}

func TestTableNames(t *testing.T) {
	if (Chat{}).TableName() != "chats" {
		t.Fatalf("Chat.TableName() = %q; want %q", (Chat{}).TableName(), "chats")
	}
	if (Message{}).TableName() != "messages" {
		t.Fatalf("Message.TableName() = %q; want %q", (Message{}).TableName(), "messages")
	}
	if (Feedback{}).TableName() != "feedback" {
		t.Fatalf("Feedback.TableName() = %q; want %q", (Feedback{}).TableName(), "feedback")
	}
}

func TestMigrations_Indexes_AndCascades(t *testing.T) {
	db := newDomainDB(t)

	// Auto-migrate all three
	if err := db.AutoMigrate(&Chat{}, &Message{}, &Feedback{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	m := db.Migrator()

	// Tables exist
	for _, tbl := range []any{&Chat{}, &Message{}, &Feedback{}} {
		if !m.HasTable(tbl) {
			t.Fatalf("expected table for %T to exist", tbl)
		}
	}

	// Indexes from tags exist
	if !m.HasIndex(&Chat{}, "idx_user_chats") {
		t.Fatalf("expected index idx_user_chats on chats")
	}
	if !m.HasIndex(&Message{}, "idx_chat_msgs") {
		t.Fatalf("expected index idx_chat_msgs on messages")
	}
	if !m.HasIndex(&Feedback{}, "ux_feedback_message_user") {
		t.Fatalf("expected unique index ux_feedback_message_user on feedback")
	}

	// Seed a chat, two messages, and a feedback tied to one message
	now := time.Now().UTC()

	ch := &Chat{ID: "c1", UserID: "u1", Title: "T", CreatedAt: now, UpdatedAt: now}
	if err := db.Create(ch).Error; err != nil {
		t.Fatalf("insert chat: %v", err)
	}

	m1 := &Message{ID: "m1", ChatID: "c1", Role: "user", Content: "hello", CreatedAt: now, UpdatedAt: now}
	m2 := &Message{ID: "m2", ChatID: "c1", Role: "assistant", Content: "world", CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second)}
	if err := db.Create(m1).Error; err != nil {
		t.Fatalf("insert m1: %v", err)
	}
	if err := db.Create(m2).Error; err != nil {
		t.Fatalf("insert m2: %v", err)
	}

	fb := &Feedback{ID: "f1", MessageID: "m2", UserID: "u1", Value: 1, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(fb).Error; err != nil {
		t.Fatalf("insert feedback: %v", err)
	}

	// CASCADE: deleting a message should delete its feedback
	if err := db.Unscoped().Delete(&Message{}, "id = ?", "m2").Error; err != nil {
		t.Fatalf("delete m2: %v", err)
	}
	var cnt int64
	if err := db.Model(&Feedback{}).Where("message_id = ?", "m2").Count(&cnt).Error; err != nil {
		t.Fatalf("count feedback after message delete: %v", err)
	}
	if cnt != 0 {
		t.Fatalf("expected feedback to cascade-delete when message deleted, got count=%d", cnt)
	}

	// CASCADE: deleting the chat should delete remaining messages
	if err := db.Unscoped().Delete(&Chat{}, "id = ?", "c1").Error; err != nil {
		t.Fatalf("delete chat: %v", err)
	}
	if err := db.Model(&Message{}).Where("chat_id = ?", "c1").Count(&cnt).Error; err != nil {
		t.Fatalf("count messages after chat delete: %v", err)
	}
	if cnt != 0 {
		t.Fatalf("expected messages to cascade-delete when chat deleted, got count=%d", cnt)
	}
}
