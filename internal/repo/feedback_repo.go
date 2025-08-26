// Package repo implements the data persistence layer for domain entities,
// backed by GORM. This file provides repository functions for the Feedback model.
//
// The repository follows a "thin" approach: it performs persistence and simple
// query composition, leaving business rules to the services package.
//
// Error semantics:
//   - Duplicate feedback (same message_id,user_id) relies on the database
//     unique constraint and is returned as a raw DB error. The service layer
//     should translate that into a domain error (e.g., ErrDuplicateFeedback).
//   - On other DB errors (connectivity, constraints, etc.), the raw gorm
//     error is propagated.
//
// Functions:
//
//   - CreateFeedback(ctx, db, messageID, userID, value) -> error
//     Inserts a feedback row. The (message_id,user_id) pair must be unique.
//
// Usage:
//
//	// In the service layer
//	err := repo.CreateFeedback(ctx, db, msgID, userID, +1)
//	if err != nil {
//	    // detect unique-violation and translate to services.ErrDuplicateFeedback
//	}
package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/tbourn/go-chat-backend/internal/domain"
)

// CreateFeedback inserts a feedback row for the given message and user.
//
// The combination (message_id, user_id) must be unique, enforced by the
// database schema (unique index). If a duplicate exists, the database will
// return an error which should be translated by the service layer into a
// domain-level duplicate error.
//
// Value must be -1 (negative) or 1 (positive). Validation is expected to be
// enforced at higher layers (handlers/services) and/or via DB constraints.
//
// On success, it returns nil. On failure, it returns a DB error.
func CreateFeedback(ctx context.Context, db *gorm.DB, messageID, userID string, value int) error {
	fb := &domain.Feedback{
		ID:        uuid.NewString(),
		MessageID: messageID,
		UserID:    userID,
		Value:     value,
		CreatedAt: time.Now().UTC(),
	}
	return db.WithContext(ctx).Create(fb).Error
}
