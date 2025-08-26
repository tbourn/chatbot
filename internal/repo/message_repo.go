// Package repo implements the data persistence layer for domain entities,
// backed by GORM. This file provides repository functions for the Message model.
package repo

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/tbourn/go-chat-backend/internal/domain"
)

// CreateMessage inserts a new message row.
func CreateMessage(db *gorm.DB, chatID, role, content string, score *float64) (*domain.Message, error) {
	m := &domain.Message{
		ID:        uuid.NewString(),
		ChatID:    chatID,
		Role:      role,
		Content:   content,
		Score:     score,
		CreatedAt: time.Now().UTC(),
	}
	return m, db.Create(m).Error
}

// ListMessages returns messages ordered deterministically (CreatedAt ASC, ID ASC).
func ListMessages(db *gorm.DB, chatID string, limit int) ([]domain.Message, error) {
	var out []domain.Message
	q := db.Where("chat_id = ?", chatID).Order("created_at ASC, id ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Find(&out).Error
	return out, err
}

// CountMessages uses a raw COUNT so a missing table surfaces as an error (as tests expect).
func CountMessages(db *gorm.DB, chatID string) (int64, error) {
	var total int64
	err := db.Raw("SELECT COUNT(*) FROM messages WHERE chat_id = ?", chatID).Scan(&total).Error
	return total, err
}

// ListMessagesPage returns a paginated slice ordered (CreatedAt ASC, ID ASC).
func ListMessagesPage(db *gorm.DB, chatID string, offset, limit int) ([]domain.Message, error) {
	var out []domain.Message
	err := db.
		Where("chat_id = ?", chatID).
		Order("created_at ASC, id ASC").
		Offset(offset).
		Limit(limit).
		Find(&out).Error
	return out, err
}

// LeaveFeedback creates a feedback row for a message.
func LeaveFeedback(db *gorm.DB, messageID string, value int) error {
	fb := &domain.Feedback{
		ID:        uuid.NewString(),
		MessageID: messageID,
		Value:     value,
		CreatedAt: time.Now().UTC(),
	}
	return db.Create(fb).Error
}

// GetMessage fetches a message by ID.
func GetMessage(db *gorm.DB, id string) (*domain.Message, error) {
	var m domain.Message
	if err := db.Where("id = ?", id).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}
