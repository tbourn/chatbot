// Package repo implements the data persistence layer for domain entities,
// backed by GORM. This file provides repository functions for the Chat model.
//
// All functions are context-aware and accept a *gorm.DB handle, making them
// safe for use within transactions or connection-scoped operations.
// They follow the "thin repository" approach: no business logic, only CRUD
// persistence and query composition.
//
// Error semantics:
//   - When a chat is not found, functions return gorm.ErrRecordNotFound
//     (also exported here as ErrNotFound for convenience).
//   - On DB errors (constraint violations, connectivity issues, etc.),
//     the raw gorm error is propagated.
//
// Functions:
//
//   - CreateChat(ctx, db, userID, title) -> *domain.Chat, error
//     Inserts a new Chat row with UUID primary key and UTC timestamp.
//
//   - ListChats(ctx, db, userID) -> []domain.Chat, error
//     Returns all chats for a user, ordered by creation time descending.
//
//   - CountChats(ctx, db, userID) -> (int64, error)
//     Returns the total number of chats owned by the user.
//
//   - ListChatsPage(ctx, db, userID, offset, limit) -> []domain.Chat, error
//     Returns a paginated slice of chats for a user.
//
//   - GetChat(ctx, db, id, userID) -> *domain.Chat, error
//     Fetches a single chat by ID/userID, or ErrNotFound if missing.
//
//   - UpdateChatTitle(ctx, db, id, userID, title) -> error
//     Updates the title of a chat, enforcing user ownership.
//     Returns ErrNotFound if the chat does not exist.
//
// Usage:
//
//	// Within a service layer
//	chat, err := repo.CreateChat(ctx, db, userID, "My first chat")
//	if errors.Is(err, repo.ErrNotFound) {
//	    // handle missing
//	} else if err != nil {
//	    // handle DB failure
//	}
//
// This repository is designed to be wrapped by a higher-level service
// (see services.ChatService) which enforces business rules, caching,
// or cross-aggregate behavior.
package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/tbourn/go-chat-backend/internal/domain"
)

// ErrNotFound is returned when a requested record does not exist.
// It aliases gorm.ErrRecordNotFound for convenience and consistency
// across the service layer and handlers.
var ErrNotFound = gorm.ErrRecordNotFound

// CreateChat inserts a new Chat row owned by userID with the given title.
// The chat ID is a randomly generated UUID (string), and CreatedAt is set to UTC.
//
// On success, it returns the persisted Chat. On failure, it returns a DB error.
func CreateChat(ctx context.Context, db *gorm.DB, userID, title string) (*domain.Chat, error) {
	c := &domain.Chat{
		ID:        uuid.NewString(),
		UserID:    userID,
		Title:     title,
		CreatedAt: time.Now().UTC(),
	}
	if err := db.WithContext(ctx).Create(c).Error; err != nil {
		return nil, err
	}
	return c, nil
}

// ListChats returns all chats belonging to userID, ordered by creation time
// descending (most recent first). It returns an empty slice if the user has
// no chats. On DB error, it returns the error.
func ListChats(ctx context.Context, db *gorm.DB, userID string) ([]domain.Chat, error) {
	var out []domain.Chat
	err := db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at desc").
		Find(&out).Error
	return out, err
}

// CountChats returns the total number of chats owned by userID.
// On DB error, it returns the error.
func CountChats(ctx context.Context, db *gorm.DB, userID string) (int64, error) {
	var total int64
	err := db.WithContext(ctx).
		Model(&domain.Chat{}).
		Where("user_id = ?", userID).
		Count(&total).Error
	return total, err
}

// ListChatsPage returns a paginated slice of chats for userID, ordered by
// creation time descending. Use CountChats to obtain the total for pagination
// metadata. On DB error, it returns the error.
//
// The caller is responsible for computing offset and limit (e.g., (page-1)*pageSize).
func ListChatsPage(ctx context.Context, db *gorm.DB, userID string, offset, limit int) ([]domain.Chat, error) {
	var out []domain.Chat
	err := db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at desc").
		Offset(offset).
		Limit(limit).
		Find(&out).Error
	return out, err
}

// GetChat fetches a single chat by its ID and owner (userID). If the record
// does not exist, it returns ErrNotFound. On other DB errors, the raw error
// is returned.
func GetChat(ctx context.Context, db *gorm.DB, id, userID string) (*domain.Chat, error) {
	var c domain.Chat
	err := db.WithContext(ctx).
		Where("id = ? AND user_id = ?", id, userID).
		First(&c).Error
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// UpdateChatTitle updates the title of a chat identified by id and owned by
// userID. If no rows are affected (chat missing or not owned by userID),
// it returns ErrNotFound. On DB error, the raw error is returned.
func UpdateChatTitle(ctx context.Context, db *gorm.DB, id, userID, title string) error {
	res := db.WithContext(ctx).
		Model(&domain.Chat{}).
		Where("id = ? AND user_id = ?", id, userID).
		Update("title", title)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
