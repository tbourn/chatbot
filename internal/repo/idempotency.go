// Package repo implements the data persistence layer for domain entities,
// backed by GORM. This file provides repository helpers for the Idempotency
// model used to implement safe-retry semantics for POST endpoints.
package repo

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/tbourn/go-chat-backend/internal/domain"
)

// ErrDuplicate indicates that an idempotency record already exists for the
// given (user_id, chat_id, key) tuple.
var ErrDuplicate = errors.New("duplicate")

// GetIdempotency returns a non-expired record or ErrNotFound.
func GetIdempotency(ctx context.Context, db *gorm.DB, userID, chatID, key string, now time.Time) (*domain.Idempotency, error) {
	if strings.TrimSpace(chatID) == "" {
		return nil, ErrNotFound
	}
	var rec domain.Idempotency
	err := db.WithContext(ctx).
		Where("user_id = ? AND chat_id = ? AND key = ? AND expires_at > ?", userID, chatID, key, now).
		First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &rec, err
}

// CreateIdempotency inserts a record and returns ErrDuplicate on unique violation.
func CreateIdempotency(ctx context.Context, db *gorm.DB, userID, chatID, key, messageID string, status int, ttl time.Duration) (*domain.Idempotency, error) {
	now := time.Now().UTC()
	rec := &domain.Idempotency{
		ID:        uuid.NewString(),
		UserID:    userID,
		ChatID:    chatID,
		Key:       key,
		MessageID: messageID,
		Status:    status,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	if err := db.WithContext(ctx).Create(rec).Error; err != nil {
		// glebarez/sqlite often returns plain-text errors for UNIQUE violations.
		low := strings.ToLower(err.Error())
		if errors.Is(err, gorm.ErrDuplicatedKey) ||
			strings.Contains(low, "unique constraint failed") ||
			strings.Contains(low, "constraint failed: unique") {
			return nil, ErrDuplicate
		}
		return nil, err
	}
	return rec, nil
}
