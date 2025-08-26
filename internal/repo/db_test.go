package repo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/tbourn/go-chat-backend/internal/domain"
)

func TestOpenSQLite_ErrorOnBadPath(t *testing.T) {
	base := t.TempDir()
	bad := filepath.Join(base, "does-not-exist", "app.db")

	db, err := OpenSQLite(bad)
	if err == nil || db != nil {
		t.Fatalf("expected error opening %q, got db=%v err=%v", bad, db, err)
	}

	// Be tolerant across platforms/drivers:
	// - Windows: *os.PathError ("CreateFile … cannot find the file specified")
	// - SQLite:  "unable to open database file" / "out of memory (14)"
	// - Unix:    "no such file or directory"
	lower := strings.ToLower(err.Error())
	if !(os.IsNotExist(err) ||
		strings.Contains(lower, "unable to open database file") ||
		strings.Contains(lower, "no such file or directory") ||
		strings.Contains(lower, "out of memory")) {
		t.Fatalf("unexpected error opening %q: %v", bad, err)
	}
}

func TestOpenSQLite_SetsPragmas_Pool_AndAutoMigrate(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "app.db")

	db, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB(): %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	// --- Verify PRAGMAs set by OpenSQLite ---
	var (
		journalMode string
		syncVal     int
		fkOn        int
		busyMS      int
	)

	if err := db.Raw("PRAGMA journal_mode;").Row().Scan(&journalMode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if strings.ToLower(journalMode) != "wal" {
		t.Fatalf("expected journal_mode=wal, got %q", journalMode)
	}

	if err := db.Raw("PRAGMA synchronous;").Row().Scan(&syncVal); err != nil {
		t.Fatalf("PRAGMA synchronous: %v", err)
	}
	// NORMAL == 1
	if syncVal != 1 {
		t.Fatalf("expected synchronous=1 (NORMAL), got %d", syncVal)
	}

	if err := db.Raw("PRAGMA foreign_keys;").Row().Scan(&fkOn); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if fkOn != 1 {
		t.Fatalf("expected foreign_keys=1, got %d", fkOn)
	}

	if err := db.Raw("PRAGMA busy_timeout;").Row().Scan(&busyMS); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if busyMS != 5000 {
		t.Fatalf("expected busy_timeout=5000, got %d", busyMS)
	}

	// --- Verify pool tuning applied ---
	if stats := sqlDB.Stats(); stats.MaxOpenConnections != 10 {
		t.Fatalf("expected MaxOpenConnections=10, got %d", stats.MaxOpenConnections)
	}

	// --- AutoMigrate should create all tables ---
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	m := db.Migrator()
	for _, tbl := range []any{&domain.Chat{}, &domain.Message{}, &domain.Feedback{}, &domain.Idempotency{}} {
		if !m.HasTable(tbl) {
			t.Fatalf("expected table for %T to exist", tbl)
		}
	}

	// Quick insert round-trip to prove schema is usable.
	now := time.Now().UTC()
	chat := &domain.Chat{ID: "c1", UserID: "u1", Title: "t", CreatedAt: now, UpdatedAt: now}
	if err := db.Create(chat).Error; err != nil {
		t.Fatalf("insert chat: %v", err)
	}
	msg := &domain.Message{ID: "m1", ChatID: "c1", Role: "user", Content: "hi", CreatedAt: now, UpdatedAt: now}
	if err := db.Create(msg).Error; err != nil {
		t.Fatalf("insert message: %v", err)
	}
	idem := &domain.Idempotency{Key: "k1", UserID: "u1", ChatID: "c1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := db.Create(idem).Error; err != nil {
		t.Fatalf("insert idempotency: %v", err)
	}

	var got domain.Chat
	if err := db.First(&got, "id = ?", "c1").Error; err != nil || got.UserID != "u1" {
		t.Fatalf("readback chat failed: err=%v got=%+v", err, got)
	}
}

// Compile-time guard to ensure signature stability.
var _ func(string) (*gorm.DB, error) = OpenSQLite
