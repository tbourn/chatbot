// Package services – ChatService
//
// This file implements the ChatService, which manages the lifecycle of chats.
// It validates and normalizes titles, enforces ownership rules, and coordinates
// repository operations for creating, listing (with pagination), and updating
// chats. Title handling is intentionally minimal here because automatic title
// generation is performed in MessageService on the first user message.
//
// Service-level errors (e.g., ErrChatNotFound) are returned for predictable
// cases so handlers can map them to HTTP results consistently.
package services

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/tbourn/go-chat-backend/internal/domain"
	"golang.org/x/text/language"
)

// ChatRepo defines the repository contract required by ChatService.
// Implementations are responsible for persistence of chat aggregates.
type ChatRepo interface {
	// CreateChat inserts a new chat row for the given user.
	CreateChat(ctx context.Context, db *gorm.DB, userID, title string) (*domain.Chat, error)

	// ListChats returns all chats belonging to the user (non-paginated).
	ListChats(ctx context.Context, db *gorm.DB, userID string) ([]domain.Chat, error)

	// GetChat fetches a chat by ID ensuring it belongs to the user.
	GetChat(ctx context.Context, db *gorm.DB, id, userID string) (*domain.Chat, error)

	// UpdateChatTitle updates a chat’s title (only if it belongs to the user).
	UpdateChatTitle(ctx context.Context, db *gorm.DB, id, userID, title string) error

	// CountChats returns the total number of chats for pagination.
	CountChats(ctx context.Context, db *gorm.DB, userID string) (int64, error)

	// ListChatsPage returns a page of chats belonging to the user.
	ListChatsPage(ctx context.Context, db *gorm.DB, userID string, offset, limit int) ([]domain.Chat, error)
}

// ChatService provides chat-level operations such as creating,
// listing, and updating chat metadata. It enforces title rules
// and ensures ownership constraints.
type ChatService struct {
	// DB is the GORM handle used for persistence.
	DB *gorm.DB
	// Repo is the chat repository used by this service.
	Repo ChatRepo

	// TitleMaxLen caps stored titles by rune length.
	TitleMaxLen int
	// TitleLocale is retained for compatibility; auto-titling is handled in MessageService.
	TitleLocale language.Tag
}

// NewChatService constructs a ChatService with sane defaults for title handling.
func NewChatService(db *gorm.DB, r ChatRepo) *ChatService {
	return &ChatService{
		DB:          db,
		Repo:        r,
		TitleMaxLen: 60,
		TitleLocale: language.Und,
	}
}

// Create inserts a new chat owned by userID with the provided title.
// Titles are normalized, trimmed, clipped, and a default fallback is applied.
func (s *ChatService) Create(ctx context.Context, userID, title string) (*domain.Chat, error) {
	title = normalizeTitle(title)
	if title == "" {
		title = "New chat"
	}
	return s.Repo.CreateChat(ctx, s.DB, userID, s.clip(title))
}

// List returns all chats for a user (non-paginated).
// Prefer ListPage for scalability on large datasets.
func (s *ChatService) List(ctx context.Context, userID string) ([]domain.Chat, error) {
	return s.Repo.ListChats(ctx, s.DB, userID)
}

// ListPage returns a page of chats for a user (paginated).
// It applies defaults for invalid page/pageSize and returns total count.
func (s *ChatService) ListPage(ctx context.Context, userID string, page, pageSize int) ([]domain.Chat, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	total, err := s.Repo.CountChats(ctx, s.DB, userID)
	if err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return []domain.Chat{}, 0, nil
	}

	items, err := s.Repo.ListChatsPage(ctx, s.DB, userID, offset, pageSize)
	return items, total, err
}

// UpdateTitle updates a chat’s title, ensuring the chat exists and
// belongs to the given user. Falls back to "Untitled" if title is blank.
func (s *ChatService) UpdateTitle(ctx context.Context, userID, chatID, title string) error {
	title = normalizeTitle(title)
	if title == "" {
		title = "Untitled"
	}
	// Ensure the chat exists and belongs to the user.
	if _, err := s.Repo.GetChat(ctx, s.DB, chatID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrChatNotFound
		}
		return err
	}
	return s.Repo.UpdateChatTitle(ctx, s.DB, chatID, userID, s.clip(title))
}

// clip truncates a chat title to the configured maximum rune length.
func (s *ChatService) clip(title string) string {
	if s.TitleMaxLen > 0 && utf8.RuneCountInString(title) > s.TitleMaxLen {
		return string([]rune(title)[:s.TitleMaxLen])
	}
	return title
}

// normalizeTitle trims whitespace and collapses multiple spaces to one.
func normalizeTitle(s string) string {
	s = whitespaceRE.ReplaceAllString(strings.TrimSpace(s), " ")
	return s
}

// whitespaceRE collapses consecutive whitespace to a single space.
var whitespaceRE = regexp.MustCompile(`\s+`)
