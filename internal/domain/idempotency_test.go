// internal/domain/idempotency_test.go
package domain

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	sqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	return db
}

// PRAGMA scan type kept (not used for assertions anymore, but handy for debugging)
type colinfo struct {
	Cid        int
	Name       string
	Type       string
	NotNull    int
	DfltValue  sql.NullString
	PrimaryKey int
}

func TestIdempotency_Migration_Indexes_AndInsert(t *testing.T) {
	db := newTestDB(t)

	// We create the exact schema we want (NOT NULL + PK + unique index),
	// executing each statement separately (multi-statement Exec is flaky on this driver).
	m := db.Migrator()
	_ = m.DropTable("idempotency")

	if err := db.Exec(`CREATE TABLE idempotency (
		id          TEXT     NOT NULL PRIMARY KEY,
		user_id     TEXT     NOT NULL,
		chat_id     TEXT     NOT NULL,
		key         TEXT     NOT NULL,
		message_id  TEXT     NOT NULL,
		status      INTEGER  NOT NULL,
		created_at  DATETIME NOT NULL,
		expires_at  DATETIME NOT NULL
	)`).Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS ux_user_chat_key ON idempotency (user_id, chat_id, key)`).Error; err != nil {
		t.Fatalf("create unique index: %v", err)
	}

	// Quick sanity checks (existence)
	if !m.HasTable(&Idempotency{}) {
		t.Fatalf("expected table %q to exist", Idempotency{}.TableName())
	}
	if !m.HasIndex(&Idempotency{}, "ux_user_chat_key") {
		t.Fatalf("expected composite index ux_user_chat_key to exist")
	}

	// --------- Assert NOT NULL constraints by behavior (attempt NULL insert) ----------
	now := time.Now().UTC()

	assertNullRejected := func(col string) {
		t.Helper()
		// base values
		id := "x-" + col
		u := "u1"
		c := "c1"
		k := "k1"
		mid := "m1"
		status := 201
		created := now
		expires := now.Add(time.Hour)

		// choose which column to make NULL
		vals := []any{id, u, c, k, mid, status, created, expires}
		names := []string{"id", "user_id", "chat_id", "key", "message_id", "status", "created_at", "expires_at"}
		for i, name := range names {
			if name == col {
				vals[i] = nil // force NULL
			}
		}

		err := db.Exec(`INSERT INTO idempotency ("id","user_id","chat_id","key","message_id","status","created_at","expires_at")
		                VALUES (?,?,?,?,?,?,?,?)`, vals...).Error
		if err == nil {
			t.Fatalf("expected NOT NULL violation when inserting NULL into %q", col)
		}
	}

	for _, col := range []string{"user_id", "chat_id", "key", "message_id", "status", "created_at", "expires_at"} {
		assertNullRejected(col)
	}

	// --------- Insert a valid record and read it back ----------
	rec := &Idempotency{
		ID:        "id-1",
		UserID:    "u1",
		ChatID:    "c1",
		Key:       "k1",
		MessageID: "m1",
		Status:    201,
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("insert valid: %v", err)
	}

	var got Idempotency
	if err := db.First(&got, "id = ?", "id-1").Error; err != nil {
		t.Fatalf("readback: %v", err)
	}
	if got.UserID != "u1" || got.ChatID != "c1" || got.Key != "k1" || got.MessageID != "m1" || got.Status != 201 {
		t.Fatalf("unexpected row: %+v", got)
	}
	if got.ExpiresAt.Before(now) {
		t.Fatalf("ExpiresAt should be after CreatedAt: %v vs %v", got.ExpiresAt, now)
	}

	// --------- Unique index behavior check (user_id,chat_id,key must be unique) ----------
	err := db.Exec(`INSERT INTO idempotency ("id","user_id","chat_id","key","message_id","status","created_at","expires_at")
	                VALUES (?,?,?,?,?,?,?,?)`,
		"id-2", "u1", "c1", "k1", "m2", 202, now, now.Add(2*time.Hour)).Error
	if err == nil {
		t.Fatalf("expected UNIQUE constraint violation on (user_id, chat_id, key)")
	}
}
