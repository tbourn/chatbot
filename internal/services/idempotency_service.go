package services

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/tbourn/go-chat-backend/internal/domain"
	"github.com/tbourn/go-chat-backend/internal/repo"
)

// IdempotencyService persists and retrieves idempotent request results.
type IdempotencyService struct {
	DB  *gorm.DB
	TTL time.Duration
}

// NewIdempotencyService constructs an IdempotencyService with a fallback TTL.
func NewIdempotencyService(db *gorm.DB, ttl time.Duration) *IdempotencyService {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &IdempotencyService{DB: db, TTL: ttl}
}

// Exists reports whether a non-expired idempotency record is stored.
func (s *IdempotencyService) Exists(ctx context.Context, userID, chatID, key string, now time.Time) (bool, error) {
	if s == nil || s.DB == nil {
		return false, nil
	}
	rec, err := repo.GetIdempotency(ctx, s.DB, userID, chatID, key, now)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return rec != nil, nil
}

// Replay returns the previously stored message for (user, chat, key) when one exists.
func (s *IdempotencyService) Replay(ctx context.Context, userID, chatID, key string, now time.Time) (*domain.Message, bool, error) {
	if s == nil || s.DB == nil {
		return nil, false, nil
	}
	rec, err := repo.GetIdempotency(ctx, s.DB, userID, chatID, key, now)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	msg, err := repo.GetMessage(s.DB, rec.MessageID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return msg, true, nil
}

// Record stores the association between an idempotency key and a response message.
func (s *IdempotencyService) Record(ctx context.Context, userID, chatID, key string, messageID string, status int) error {
	if s == nil || s.DB == nil {
		return nil
	}
	_, err := repo.CreateIdempotency(ctx, s.DB, userID, chatID, key, messageID, status, s.TTL)
	if err != nil {
		if errors.Is(err, repo.ErrDuplicate) {
			return nil
		}
		return err
	}
	return nil
}
