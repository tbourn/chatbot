package services

import (
	"context"
	"errors"
	"testing"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/tbourn/go-chat-backend/internal/domain"
	"golang.org/x/text/language"
)

// ----- Fake repo -----

type fakeChatRepo struct {
	// capture args
	createUserID string
	createTitle  string

	listUserID string

	getID     string
	getUserID string
	getChat   *domain.Chat
	getErr    error

	updateID     string
	updateUserID string
	updateTitle  string
	updateErr    error

	countUserID string
	countTotal  int64
	countErr    error

	pageUserID string
	pageOffset int
	pageLimit  int
	pageItems  []domain.Chat
	pageErr    error
}

func (r *fakeChatRepo) CreateChat(ctx context.Context, db *gorm.DB, userID, title string) (*domain.Chat, error) {
	r.createUserID = userID
	r.createTitle = title
	return &domain.Chat{ID: "c1", UserID: userID, Title: title}, nil
}

func (r *fakeChatRepo) ListChats(ctx context.Context, db *gorm.DB, userID string) ([]domain.Chat, error) {
	r.listUserID = userID
	return []domain.Chat{
		{ID: "c1", UserID: userID, Title: "t1"},
		{ID: "c2", UserID: userID, Title: "t2"},
	}, nil
}

func (r *fakeChatRepo) GetChat(ctx context.Context, db *gorm.DB, id, userID string) (*domain.Chat, error) {
	r.getID, r.getUserID = id, userID
	return r.getChat, r.getErr
}

func (r *fakeChatRepo) UpdateChatTitle(ctx context.Context, db *gorm.DB, id, userID, title string) error {
	r.updateID, r.updateUserID, r.updateTitle = id, userID, title
	return r.updateErr
}

func (r *fakeChatRepo) CountChats(ctx context.Context, db *gorm.DB, userID string) (int64, error) {
	r.countUserID = userID
	return r.countTotal, r.countErr
}

func (r *fakeChatRepo) ListChatsPage(ctx context.Context, db *gorm.DB, userID string, offset, limit int) ([]domain.Chat, error) {
	r.pageUserID, r.pageOffset, r.pageLimit = userID, offset, limit
	return r.pageItems, r.pageErr
}

// ----- Tests -----

func TestNewChatService_Defaults(t *testing.T) {
	r := &fakeChatRepo{}
	s := NewChatService(nil, r)

	if s.DB != nil { // DB can be nil in tests
		t.Fatalf("expected nil DB, got %v", s.DB)
	}
	if s.Repo != r {
		t.Fatalf("repo not set")
	}
	if s.TitleMaxLen != 60 {
		t.Fatalf("TitleMaxLen default = 60, got %d", s.TitleMaxLen)
	}
	if s.TitleLocale != language.Und {
		t.Fatalf("TitleLocale default = Und, got %v", s.TitleLocale)
	}
}

func TestNormalizeTitle(t *testing.T) {
	cases := map[string]string{
		"":                      "",
		"   leading   ":         "leading",
		"multi   spaces":        "multi spaces",
		"tabs\tand\nnewlines  ": "tabs and newlines",
		" already ok ":          "already ok",
		"\t  \n":                "",
		"  a   b   c  ":         "a b c",
	}
	for in, want := range cases {
		if got := normalizeTitle(in); got != want {
			t.Errorf("normalizeTitle(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestClip_UsesRunesNotBytes(t *testing.T) {
	r := &fakeChatRepo{}
	s := NewChatService(nil, r)
	s.TitleMaxLen = 5

	// Use a multi-byte rune (e.g., snowman) and plain letters
	long := "☃☃☃☃☃☃☃" // 7 runes, > 5
	got := s.clip(long)
	if utf8.RuneCountInString(got) != 5 {
		t.Fatalf("clip should keep 5 runes, got %d (%q)", utf8.RuneCountInString(got), got)
	}
	// Also ensure it returns input when under limit
	short := "hi"
	if s.clip(short) != short {
		t.Fatalf("expected passthrough for short input")
	}
}

func TestCreate_DefaultTitleWhenBlank_AndClipped(t *testing.T) {
	r := &fakeChatRepo{}
	s := NewChatService(nil, r)
	s.TitleMaxLen = 4

	// blank -> "New chat" -> clipped to "New "
	chat, err := s.Create(context.Background(), "u1", "    ")
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if chat.UserID != "u1" {
		t.Fatalf("chat.UserID = %q", chat.UserID)
	}
	if r.createTitle != "New " {
		t.Fatalf("repo got title %q; want %q", r.createTitle, "New ")
	}
}

func TestCreate_NormalizesAndClips(t *testing.T) {
	r := &fakeChatRepo{}
	s := NewChatService(nil, r)
	s.TitleMaxLen = 3

	_, err := s.Create(context.Background(), "user-x", "  A   B  ")
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	// "A B" clipped to "A B" (3 runes exactly)
	if r.createTitle != "A B" {
		t.Fatalf("expected normalized/clipped title %q, got %q", "A B", r.createTitle)
	}
}

func TestList_ForwardsToRepo(t *testing.T) {
	r := &fakeChatRepo{}
	s := NewChatService(nil, r)

	out, err := s.List(context.Background(), "u2")
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if r.listUserID != "u2" {
		t.Fatalf("repo got user %q; want u2", r.listUserID)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 items, got %d", len(out))
	}
}

func TestListPage_DefaultsAndTotalZero(t *testing.T) {
	r := &fakeChatRepo{countTotal: 0}
	s := NewChatService(nil, r)

	// page=0 -> default to 1, size=0 -> default to 20
	items, total, err := s.ListPage(context.Background(), "u3", 0, 0)
	if err != nil {
		t.Fatalf("ListPage error: %v", err)
	}
	if total != 0 || len(items) != 0 {
		t.Fatalf("expected empty results when total=0; got total=%d len=%d", total, len(items))
	}
	// verify defaults used by side effect: CountChats only called; offset/limit not used
	if r.countUserID != "u3" {
		t.Fatalf("CountChats called with user %q; want u3", r.countUserID)
	}
}

func TestListPage_CountError(t *testing.T) {
	sentinel := errors.New("boom")
	r := &fakeChatRepo{countErr: sentinel}
	s := NewChatService(nil, r)

	_, _, err := s.ListPage(context.Background(), "u4", 1, 10)
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected count error to propagate, got %v", err)
	}
}

func TestListPage_Success_OffsetLimitAndItemsError(t *testing.T) {
	// First: items error propagates
	sentinel := errors.New("items-fail")
	r := &fakeChatRepo{
		countTotal: 42,
		pageErr:    sentinel,
	}
	s := NewChatService(nil, r)

	_, total, err := s.ListPage(context.Background(), "u5", 3, 10)
	if total != 42 {
		t.Fatalf("total = %d; want 42", total)
	}
	if r.pageOffset != (3-1)*10 || r.pageLimit != 10 {
		t.Fatalf("offset/limit = %d/%d; want %d/%d", r.pageOffset, r.pageLimit, 20, 10)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected items error to propagate")
	}

	// Second: success path returns items
	r2 := &fakeChatRepo{
		countTotal: 42,
		pageItems:  []domain.Chat{{ID: "x1"}, {ID: "x2"}},
	}
	s2 := NewChatService(nil, r2)
	items, total2, err2 := s2.ListPage(context.Background(), "u6", -10, -5) // forces defaults: page=1, size=20
	if err2 != nil {
		t.Fatalf("ListPage success error: %v", err2)
	}
	if total2 != 42 || len(items) != 2 {
		t.Fatalf("expected 2 items and total 42; got %d/%d", len(items), total2)
	}
	if r2.pageOffset != 0 || r2.pageLimit != 20 {
		t.Fatalf("expected default offset/limit 0/20; got %d/%d", r2.pageOffset, r2.pageLimit)
	}
}

func TestUpdateTitle_NotFoundMapsToErrChatNotFound(t *testing.T) {
	r := &fakeChatRepo{getErr: gorm.ErrRecordNotFound}
	s := NewChatService(nil, r)

	err := s.UpdateTitle(context.Background(), "u1", "chat-1", "ignored")
	if !errors.Is(err, ErrChatNotFound) {
		t.Fatalf("expected ErrChatNotFound mapping, got %v", err)
	}
}

func TestUpdateTitle_RepoGetOtherError(t *testing.T) {
	sentinel := errors.New("db down")
	r := &fakeChatRepo{getErr: sentinel}
	s := NewChatService(nil, r)

	err := s.UpdateTitle(context.Background(), "u1", "chat-1", "ok")
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
}

func TestUpdateTitle_BlankBecomesUntitled_AndClippedAndNormalized(t *testing.T) {
	r := &fakeChatRepo{getChat: &domain.Chat{ID: "chat-1", UserID: "u1"}}
	s := NewChatService(nil, r)
	s.TitleMaxLen = 7

	// Blank -> "Untitled", clipped to 7 runes -> "Untitle"
	err := s.UpdateTitle(context.Background(), "u1", "chat-1", "   \t  ")
	if err != nil {
		t.Fatalf("UpdateTitle error: %v", err)
	}
	if r.updateTitle != "Untitle" {
		t.Fatalf("expected clipped Untitled -> Untitle, got %q", r.updateTitle)
	}

	// Normalization: multiple spaces collapse to one, then clipped if needed
	r2 := &fakeChatRepo{getChat: &domain.Chat{ID: "chat-2", UserID: "u2"}}
	s2 := NewChatService(nil, r2)
	s2.TitleMaxLen = 5
	err = s2.UpdateTitle(context.Background(), "u2", "chat-2", "  A   B   C  ")
	if err != nil {
		t.Fatalf("UpdateTitle error: %v", err)
	}
	// "A B C" (5 runes) fits exactly
	if r2.updateTitle != "A B C" {
		t.Fatalf("expected normalized title %q; got %q", "A B C", r2.updateTitle)
	}
}
