package repo

import (
	"context"
	"fmt"
	"testing"
	"time"

	sqlite "github.com/glebarez/sqlite" // pure-Go SQLite
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/tbourn/go-chat-backend/internal/domain"
)

func newIdemDB(t *testing.T, migrate ...any) *gorm.DB {
	t.Helper()
	// Use a unique in-memory database per test to avoid schema leakage across tests.
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

func ensureUniqueIndex(t *testing.T, db *gorm.DB) {
	t.Helper()
	// Ensure uniqueness on (user_id, chat_id, key) so duplicate path is guaranteed.
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_idempotency_user_chat_key ON idempotencies(user_id, chat_id, key)`)
}

func TestGetIdempotency_NoChatID_ReturnsNotFound(t *testing.T) {
	db := newIdemDB(t, &domain.Idempotency{})
	now := time.Now().UTC()

	rec, err := GetIdempotency(context.Background(), db, "u1", "   ", "k1", now)
	if rec != nil || err != ErrNotFound {
		t.Fatalf("expected (nil, ErrNotFound) for empty chatID, got (%v, %v)", rec, err)
	}
}

func TestGetIdempotency_ExpiredOrMissing_ReturnsNotFound(t *testing.T) {
	db := newIdemDB(t, &domain.Idempotency{})
	now := time.Now().UTC()

	// Insert an expired record (expires_at <= now)
	exp := &domain.Idempotency{
		ID:        "expired",
		UserID:    "u1",
		ChatID:    "c1",
		Key:       "k1",
		Status:    200,
		CreatedAt: now.Add(-2 * time.Hour),
		ExpiresAt: now.Add(-time.Hour),
	}
	if err := db.Create(exp).Error; err != nil {
		t.Fatalf("seed expired: %v", err)
	}

	rec, err := GetIdempotency(context.Background(), db, "u1", "c1", "k1", now)
	if rec != nil || err != ErrNotFound {
		t.Fatalf("expected (nil, ErrNotFound) for expired, got (%v, %v)", rec, err)
	}

	// Also check a totally missing key
	rec2, err2 := GetIdempotency(context.Background(), db, "u1", "c1", "missing", now)
	if rec2 != nil || err2 != ErrNotFound {
		t.Fatalf("expected (nil, ErrNotFound) for missing, got (%v, %v)", rec2, err2)
	}
}

func TestGetIdempotency_Success(t *testing.T) {
	db := newIdemDB(t, &domain.Idempotency{})
	now := time.Now().UTC()

	ok := &domain.Idempotency{
		ID:        "ok",
		UserID:    "u1",
		ChatID:    "c2",
		Key:       "k2",
		MessageID: "m1",
		Status:    201,
		CreatedAt: now.Add(-time.Minute),
		ExpiresAt: now.Add(time.Hour),
	}
	if err := db.Create(ok).Error; err != nil {
		t.Fatalf("seed ok: %v", err)
	}

	rec, err := GetIdempotency(context.Background(), db, "u1", "c2", "k2", now)
	if err != nil {
		t.Fatalf("GetIdempotency success err: %v", err)
	}
	if rec == nil || rec.MessageID != "m1" || rec.Status != 201 {
		t.Fatalf("unexpected record: %+v", rec)
	}
}

func TestCreateIdempotency_SuccessAndDuplicate(t *testing.T) {
	db := newIdemDB(t, &domain.Idempotency{})
	ensureUniqueIndex(t, db)

	ttl := 90 * time.Minute
	start := time.Now().UTC()

	// Success
	rec, err := CreateIdempotency(context.Background(), db, "u9", "c9", "k9", "m9", 202, ttl)
	if err != nil {
		t.Fatalf("CreateIdempotency error: %v", err)
	}
	if rec == nil || rec.ID == "" || rec.UserID != "u9" || rec.ChatID != "c9" || rec.Key != "k9" || rec.MessageID != "m9" || rec.Status != 202 {
		t.Fatalf("unexpected record: %+v", rec)
	}
	// ExpiresAt should be in (start, start+2h) — loose bound to avoid timing flakes.
	if !(rec.ExpiresAt.After(start) && rec.ExpiresAt.Before(start.Add(2*time.Hour))) {
		t.Fatalf("unexpected ExpiresAt: %v", rec.ExpiresAt)
	}

	// Duplicate (same user, chat, key) should map to ErrDuplicate
	_, err2 := CreateIdempotency(context.Background(), db, "u9", "c9", "k9", "mX", 200, ttl)
	if err2 != ErrDuplicate {
		t.Fatalf("expected ErrDuplicate, got %v", err2)
	}
}

// Generic DB error path: attempt insert without migrating the table.
func TestCreateIdempotency_Error_NoTable(t *testing.T) {
	db := newIdemDB(t) // intentionally NOT migrating idempotencies
	_, err := CreateIdempotency(context.Background(), db, "uX", "cX", "kX", "mX", 200, time.Minute)
	if err == nil {
		t.Fatalf("expected error when table is missing")
	}
	if err == ErrDuplicate {
		t.Fatalf("expected non-duplicate error, got ErrDuplicate")
	}
}
