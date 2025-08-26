// Package services – FeedbackService
//
// This file implements the FeedbackService, which governs how users leave
// feedback (-1 or +1) on assistant messages. It enforces business rules
// (message existence, chat ownership, assistant-only restriction, uniqueness)
// and persists feedback atomically in the database. Service-level errors
// (e.g. ErrInvalidFeedback, ErrMessageNotFound, ErrForbiddenFeedback,
// ErrDuplicateFeedback) are returned for predictable cases so handlers can
// map them to HTTP results consistently.
package services

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/tbourn/go-chat-backend/internal/domain"
	"github.com/tbourn/go-chat-backend/internal/repo"
)

// FeedbackService implements the use-cases around message feedback.
// It validates the operation (ownership, message role, uniqueness) and persists
// the feedback using the provided GORM handle. The service is context-aware and
// safe to use inside transactions (it will open its own transaction per call).
type FeedbackService struct {
	// DB is the database handle used for all feedback operations.
	// The handle may be a plain *gorm.DB or a transaction-bound handle.
	DB *gorm.DB
}

// Leave records a feedback value for messageID on behalf of userID.
//
// Semantics and validation:
//   - value must be exactly -1 (negative) or 1 (positive); otherwise ErrInvalidFeedback.
//   - messageID must exist; otherwise ErrMessageNotFound.
//   - The message must belong to a chat owned by userID; otherwise ErrForbiddenFeedback.
//   - Feedback is allowed only for assistant messages; user messages are rejected
//     with ErrForbiddenFeedback.
//   - A user may leave at most one feedback per message; attempting to do so
//     again yields ErrDuplicateFeedback.
//
// Concurrency & atomicity:
//   - The operation runs inside a transaction to ensure the existence/ownership
//     checks and the insert are atomic.
//
// Errors:
//   - Returns the service-level sentinel errors (ErrInvalidFeedback,
//     ErrMessageNotFound, ErrForbiddenFeedback, ErrDuplicateFeedback) for the
//     validation cases above.
//   - Returns the underlying DB error for unexpected failures.
func (s *FeedbackService) Leave(ctx context.Context, userID, messageID string, value int) error {
	if value != -1 && value != 1 {
		return ErrInvalidFeedback
	}

	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1) Load message and verify it exists.
		msg, err := repo.GetMessage(tx, messageID)
		if err != nil {
			// repo.GetMessage returns gorm.ErrRecordNotFound if missing.
			if errors.Is(err, gorm.ErrRecordNotFound) || isNotFound(err) {
				return ErrMessageNotFound
			}
			return err
		}

		// 2) Ensure the message's chat belongs to this user.
		if _, err := repo.GetChat(ctx, tx, msg.ChatID, userID); err != nil {
			// either not found or not owned by this user
			return ErrForbiddenFeedback
		}

		// 3) Only allow feedback on assistant messages.
		if msg.Role != "assistant" {
			return ErrForbiddenFeedback
		}

		// 4) Insert feedback with (message_id, user_id) uniqueness semantics.
		fb := &domain.Feedback{
			ID:        uuid.NewString(),
			MessageID: messageID,
			UserID:    userID,
			Value:     value,
			CreatedAt: time.Now().UTC(),
		}
		if err := tx.Create(fb).Error; err != nil {
			// Map duplicate key to a stable service error.
			if errors.Is(err, gorm.ErrDuplicatedKey) || isDuplicate(err) {
				return ErrDuplicateFeedback
			}
			return err
		}
		return nil
	})
}

// isNotFound treats repo-level not found sentinels as "not found" in a
// driver-agnostic way. It also checks gorm.ErrRecordNotFound for safety.
func isNotFound(err error) bool {
	// If your repo exposes ErrNotFound, detect it here:
	if errors.Is(err, repo.ErrNotFound) {
		return true
	}
	// Fallback to GORM's sentinel.
	return errors.Is(err, gorm.ErrRecordNotFound)
}

// isDuplicate attempts to detect unique-constraint violations across drivers
// that may not map to gorm.ErrDuplicatedKey.
func isDuplicate(err error) bool {
	// SQLite typically: "UNIQUE constraint failed"
	// Postgres typically: "duplicate key value violates unique constraint"
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "duplicate key")
}
