// Package repo implements the data persistence layer for domain entities,
// backed by GORM. This file contains database bootstrapping helpers for
// SQLite (pure Go driver) and schema migrations.
package repo

import (
	"os"
	"path/filepath"
	"time"

	sqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/tbourn/go-chat-backend/internal/domain"
)

// OpenSQLite opens (or creates) a SQLite database and applies PRAGMAs.
func OpenSQLite(path string) (*gorm.DB, error) {
	// Fail early if parent directory does not exist (instead of sqlite "out of memory (14)" on Windows).
	if dir := filepath.Dir(path); dir != "." {
		if _, err := os.Stat(dir); err != nil {
			return nil, err
		}
	}

	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		return nil, err
	}

	// PRAGMAs
	db.Exec("PRAGMA journal_mode=WAL;")
	db.Exec("PRAGMA synchronous=NORMAL;")
	db.Exec("PRAGMA foreign_keys=ON;")
	db.Exec("PRAGMA busy_timeout=5000;")

	// Pool
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(10)
		sqlDB.SetMaxIdleConns(10)
		sqlDB.SetConnMaxIdleTime(5 * time.Minute)
		sqlDB.SetConnMaxLifetime(30 * time.Minute)
	}

	return db, nil
}

// AutoMigrate keeps as you had it.
func AutoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&domain.Chat{},
		&domain.Message{},
		&domain.Feedback{},
		&domain.Idempotency{},
	)
}
