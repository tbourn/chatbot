// Package domain defines the core persistence models for the application.
// These types are used by GORM for database schema mapping and are shared
// across the repository and service layers.
package domain

import "time"

// Idempotency represents a recorded result of a previously processed request,
// keyed by (user_id, chat_id, key). It enables safe retries for POST/PUT
// operations by returning the originally produced response without re-executing
// side effects.
type Idempotency struct {
	ID        string    `gorm:"type:TEXT NOT NULL;primaryKey"`
	UserID    string    `gorm:"type:TEXT NOT NULL;uniqueIndex:ux_user_chat_key,priority:1"`
	ChatID    string    `gorm:"type:TEXT NOT NULL;uniqueIndex:ux_user_chat_key,priority:2"`
	Key       string    `gorm:"type:TEXT NOT NULL;uniqueIndex:ux_user_chat_key,priority:3"`
	MessageID string    `gorm:"type:TEXT NOT NULL"`
	Status    int       `gorm:"type:INTEGER NOT NULL"`
	CreatedAt time.Time `gorm:"type:DATETIME NOT NULL;autoCreateTime"`
	ExpiresAt time.Time `gorm:"type:DATETIME NOT NULL;index"`
}

// TableName implements the GORM tabler interface.
func (Idempotency) TableName() string { return "idempotency" }
