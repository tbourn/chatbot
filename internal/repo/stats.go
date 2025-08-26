// Package repo implements the data persistence layer for domain entities,
// backed by GORM. This file provides small aggregate/statistics queries used
// primarily for conditional responses (e.g., ETag generation) in the HTTP
// layer. Each function is context-aware and safe to call from services or
// handlers.
package repo

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/tbourn/go-chat-backend/internal/domain"
)

// ChatsStats returns aggregate metadata for a user's chats: the total number of
// rows and the maximum UpdatedAt timestamp among those rows.
//
// It executes two lightweight queries against the chats table scoped to the
// provided userID. When the user has no chats, the returned count is 0 and
// maxUpdatedAt is nil.
//
// Return values:
//   - count:        total chats for userID
//   - maxUpdatedAt: pointer to the greatest UpdatedAt, or nil if no rows
//   - err:          database error, if any
func ChatsStats(ctx context.Context, db *gorm.DB, userID string) (count int64, maxUpdatedAt *time.Time, err error) {
	q := db.WithContext(ctx).Model(&domain.Chat{}).Where("user_id = ?", userID)

	// Count
	if err = q.Count(&count).Error; err != nil {
		return 0, nil, err
	}
	if count == 0 {
		return 0, nil, nil
	}

	// Get latest updated_at (avoid MAX() -> TEXT in SQLite)
	var row struct {
		UpdatedAt time.Time
	}
	if err = q.Select("updated_at").Order("updated_at DESC").Limit(1).Scan(&row).Error; err != nil {
		return 0, nil, err
	}
	return count, &row.UpdatedAt, nil
}

// MessagesStats returns aggregate metadata for messages within a given chat:
// the total number of rows and the maximum UpdatedAt timestamp among those rows.
//
// It executes two lightweight queries against the messages table scoped to the
// provided chatID. When the chat has no messages, the returned count is 0 and
// maxUpdatedAt is nil.
//
// Return values:
//   - count:        total messages for chatID
//   - maxUpdatedAt: pointer to the greatest UpdatedAt, or nil if no rows
//   - err:          database error, if any
func MessagesStats(ctx context.Context, db *gorm.DB, chatID string) (count int64, maxUpdatedAt *time.Time, err error) {
	q := db.WithContext(ctx).Model(&domain.Message{}).Where("chat_id = ?", chatID)

	// Count
	if err = q.Count(&count).Error; err != nil {
		return 0, nil, err
	}
	if count == 0 {
		return 0, nil, nil
	}

	// Get latest updated_at (avoid MAX() -> TEXT in SQLite)
	var row struct {
		UpdatedAt time.Time
	}
	if err = q.Select("updated_at").Order("updated_at DESC").Limit(1).Scan(&row).Error; err != nil {
		return 0, nil, err
	}
	return count, &row.UpdatedAt, nil
}
