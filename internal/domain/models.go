// Package domain defines the persistence models for chats, messages, and
// feedback. These types are mapped with GORM and form the core data layer
// of the chatbot application.
package domain

import (
	"time"

	"gorm.io/gorm"
)

// Chat represents a conversation owned by a user. Each chat has a generated
// title and contains one or more messages exchanged between the user and
// the assistant.
//
// Fields:
//   - ID: stable UUID primary key (char(36)).
//   - UserID: identifier of the chat owner; indexed for efficient retrieval.
//   - Title: human-readable chat title (auto-generated if not provided).
//   - CreatedAt / UpdatedAt: timestamps managed by GORM.
//   - DeletedAt: soft deletion marker (retains row for audit/history).
type Chat struct {
	ID        string         `json:"id"        gorm:"type:char(36);primaryKey"`
	UserID    string         `json:"user_id"   gorm:"type:varchar(64);not null;index:idx_user_chats"`
	Title     string         `json:"title"     gorm:"type:varchar(255);not null;default:'New chat'"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `json:"-"         gorm:"index"`
}

// TableName returns the database table name for Chat.
func (Chat) TableName() string { return "chats" }

// Message represents a single utterance within a chat. Messages are linked
// to a chat, and can be authored either by the "user" or the "assistant".
// Assistant messages may include a confidence score.
//
// Fields:
//   - ID: UUID primary key (char(36)).
//   - ChatID: foreign key to the owning chat (indexed).
//   - Role: "user" or "assistant" (enforced by DB constraint).
//   - Content: full text content of the message.
//   - Score: optional numeric score (only present for assistant messages).
//   - CreatedAt / UpdatedAt: timestamps managed by GORM.
//   - DeletedAt: soft deletion marker.
//   - Chat: FK association, ensures cascade delete/update.
type Message struct {
	ID        string         `json:"id"        gorm:"type:char(36);primaryKey"`
	ChatID    string         `json:"chat_id"   gorm:"type:char(36);not null;index:idx_chat_msgs,priority:1"`
	Role      string         `json:"role"      gorm:"type:varchar(16);not null;check:role IN ('user','assistant')"`
	Content   string         `json:"content"   gorm:"type:text;not null"`
	Score     *float64       `json:"score,omitempty"` // only for assistant messages
	CreatedAt time.Time      `json:"created_at" gorm:"index:idx_chat_msgs,priority:2"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `json:"-"         gorm:"index"`

	// Chat is the parent conversation. Messages are cascade-deleted
	// if their chat is removed.
	Chat Chat `json:"-" gorm:"foreignKey:ChatID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
}

// TableName returns the database table name for Message.
func (Message) TableName() string { return "messages" }

// Feedback represents a user-provided rating on a specific assistant message.
// A user can only leave one feedback entry per message (enforced by unique index).
//
// Fields:
//   - ID: UUID primary key (char(36)).
//   - MessageID: foreign key to the rated message (unique per user).
//   - UserID: identifier of the feedback author (unique per message).
//   - Value: +1 (positive) or -1 (negative).
//   - CreatedAt / UpdatedAt: timestamps managed by GORM.
//   - DeletedAt: soft deletion marker.
//   - Message: FK association, ensures cascade delete/update.
type Feedback struct {
	ID        string         `json:"id"         gorm:"type:char(36);primaryKey"`
	MessageID string         `json:"message_id" gorm:"type:char(36);not null;index;uniqueIndex:ux_feedback_message_user"`
	UserID    string         `json:"user_id"    gorm:"type:varchar(64);not null;index;uniqueIndex:ux_feedback_message_user"`
	Value     int            `json:"value"      gorm:"not null;check:value IN (-1,1)"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `json:"-"          gorm:"index"`

	// Message is the rated assistant message. Feedback is cascade-deleted
	// if the underlying message is removed.
	Message Message `json:"-" gorm:"foreignKey:MessageID;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
}

// TableName returns the database table name for Feedback.
func (Feedback) TableName() string { return "feedback" }
