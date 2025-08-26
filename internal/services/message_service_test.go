package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	sqlite "github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"golang.org/x/text/language"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/tbourn/go-chat-backend/internal/domain"
	"github.com/tbourn/go-chat-backend/internal/search"
)

// ---------- test helpers ----------

func newMsgDB(t *testing.T, migrate ...any) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:msgsvc_%s?mode=memory&cache=shared", uuid.NewString())

	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.Exec("PRAGMA foreign_keys=ON;")
	if len(migrate) > 0 {
		if err := db.AutoMigrate(migrate...); err != nil {
			t.Fatalf("automigrate: %v", err)
		}
	}
	return db
}

type fakeIndex struct {
	byQuery map[string][]search.Result
}

func (f *fakeIndex) TopK(q string, k int) []search.Result {
	rs := f.byQuery[q]
	if len(rs) > k {
		rs = rs[:k]
	}
	// copy for safety
	out := make([]search.Result, len(rs))
	copy(out, rs)
	return out
}

func mkIdx(entries map[string][]search.Result) search.Index {
	return &fakeIndex{byQuery: entries}
}

// ---------- Answer() ----------

func TestMessageService_Answer_EmptyPrompt(t *testing.T) {
	db := newMsgDB(t, &domain.Chat{}, &domain.Message{})
	s := &MessageService{DB: db}
	_, err := s.Answer(context.Background(), "u1", "c1", "   ")
	if err == nil || err != ErrEmptyPrompt {
		t.Fatalf("expected ErrEmptyPrompt, got %v", err)
	}
}

func TestMessageService_Answer_TooLong(t *testing.T) {
	db := newMsgDB(t, &domain.Chat{}, &domain.Message{})
	s := &MessageService{DB: db, MaxPromptRunes: 3}
	_, err := s.Answer(context.Background(), "u1", "c1", "abcd")
	if err == nil || err != ErrTooLong {
		t.Fatalf("expected ErrTooLong, got %v", err)
	}
}

func TestMessageService_Answer_ChatNotFound(t *testing.T) {
	// Migrate tables but do NOT insert the chat
	db := newMsgDB(t, &domain.Chat{}, &domain.Message{})
	s := &MessageService{DB: db}
	_, err := s.Answer(context.Background(), "uX", "c-missing", "hello")
	if err == nil || err != ErrChatNotFound {
		t.Fatalf("expected ErrChatNotFound, got %v", err)
	}
}

func TestMessageService_Answer_Success_AutoTitle_And_ClipReply(t *testing.T) {
	db := newMsgDB(t, &domain.Chat{}, &domain.Message{})
	// Chat owned by u1 with placeholder title → triggers auto-title
	chat := &domain.Chat{ID: "c1", UserID: "u1", Title: "New chat"}
	if err := db.Create(chat).Error; err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	// Retrieval returns two strong candidates (same strong entities),
	// so Answer() will concatenate them; then MaxReplyRunes will clip.
	prompt := `Gen Z in Nashville spend on streaming platforms`
	idx := mkIdx(map[string][]search.Result{
		prompt: {
			{Snippet: "In Nashville, Gen Z spend more on streaming platforms.", Score: 0.8},
			{Snippet: "Gen Z in Nashville show strong adoption of streaming platforms.", Score: 0.79},
		},
	})

	s := &MessageService{
		DB:             db,
		Index:          idx,
		Threshold:      0.05, // lenient
		MaxReplyRunes:  20,   // force clipping of concatenated reply
		TitleMaxLen:    12,   // clip generated title for assertion
		TitleLocale:    language.Und,
		MaxPromptRunes: 0,
	}

	got, err := s.Answer(context.Background(), "u1", "c1", prompt)
	if err != nil {
		t.Fatalf("Answer error: %v", err)
	}
	if got == nil || got.Role != roleAssistant {
		t.Fatalf("expected assistant message, got %#v", got)
	}
	if utf8.RuneCountInString(got.Content) != 20 {
		t.Fatalf("expected clipped reply length 20, got %d (%q)", utf8.RuneCountInString(got.Content), got.Content)
	}

	// Title should be auto-generated & clipped
	var updated domain.Chat
	if err := db.First(&updated, "id = ?", "c1").Error; err != nil {
		t.Fatalf("load updated chat: %v", err)
	}
	if updated.Title == "" || updated.Title == "New chat" {
		t.Fatalf("expected auto-generated title, got %q", updated.Title)
	}
	if utf8.RuneCountInString(updated.Title) > 12 {
		t.Fatalf("expected clipped title <=12 runes, got %q", updated.Title)
	}
}

// ---------- ListPage() ----------

func TestMessageService_ListPage_DBErrorOnChatCount(t *testing.T) {
	// DB without Chat table -> first Count() errors
	db := newMsgDB(t /* no migrate */)
	s := &MessageService{DB: db}
	_, _, err := s.ListPage(context.Background(), "c1", 1, 10)
	if err == nil {
		t.Fatalf("expected error due to missing chats table")
	}
}

func TestMessageService_ListPage_CountMessagesError(t *testing.T) {
	// Migrate Chat only -> CountMessages (on messages) errors
	db := newMsgDB(t, &domain.Chat{})
	if err := db.Create(&domain.Chat{ID: "c1", UserID: "u1", Title: "t"}).Error; err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	s := &MessageService{DB: db}
	_, _, err := s.ListPage(context.Background(), "c1", 1, 10)
	if err == nil {
		t.Fatalf("expected error due to missing messages table")
	}
}

func TestMessageService_ListPage_TotalZero_And_Success(t *testing.T) {
	db := newMsgDB(t, &domain.Chat{}, &domain.Message{})
	if err := db.Create(&domain.Chat{ID: "c2", UserID: "u1", Title: "t"}).Error; err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	s := &MessageService{DB: db}

	// total==0 branch
	items, total, err := s.ListPage(context.Background(), "c2", 0, 0) // defaults page=1,size=20
	if err != nil {
		t.Fatalf("ListPage error: %v", err)
	}
	if total != 0 || len(items) != 0 {
		t.Fatalf("expected empty, got total=%d len=%d", total, len(items))
	}

	// Add 3 messages and test success + pagination
	now := time.Now().UTC()
	msgs := []domain.Message{
		{ID: "m1", ChatID: "c2", Role: roleUser, Content: "hi", CreatedAt: now},
		{ID: "m2", ChatID: "c2", Role: roleAssistant, Content: "hey", CreatedAt: now.Add(time.Second)},
		{ID: "m3", ChatID: "c2", Role: roleUser, Content: "ok", CreatedAt: now.Add(2 * time.Second)},
	}
	for _, m := range msgs {
		if err := db.Create(&m).Error; err != nil {
			t.Fatalf("seed msg: %v", err)
		}
	}

	pageItems, total2, err := s.ListPage(context.Background(), "c2", -5, -7) // defaults to 1/20
	if err != nil {
		t.Fatalf("ListPage success error: %v", err)
	}
	if total2 != 3 || len(pageItems) == 0 {
		t.Fatalf("expected total=3 and non-empty page, got total=%d len=%d", total2, len(pageItems))
	}
}

func TestMessageService_ListPage_ChatNotFound(t *testing.T) {
	db := newMsgDB(t, &domain.Chat{}, &domain.Message{})
	s := &MessageService{DB: db}
	_, _, err := s.ListPage(context.Background(), "nope", 1, 10)
	if err == nil || err != ErrChatNotFound {
		t.Fatalf("expected ErrChatNotFound, got %v", err)
	}
}

// ---------- retrieve() branches ----------

func TestRetrieve_IndexNil_And_NoCandidatesAfterFallback(t *testing.T) {
	s := &MessageService{Index: nil}
	r, sc := s.retrieve(context.Background(), "anything")
	if sc != nil || r == "" || !strings.Contains(r, "can’t answer") {
		t.Fatalf("nil index should decline, got %q score=%v", r, sc)
	}

	// Index present but returns no results even after simplification
	idx := mkIdx(map[string][]search.Result{
		"gen z nashville": {}, // simplified returns empty too
	})
	s2 := &MessageService{Index: idx}
	r2, sc2 := s2.retrieve(context.Background(), `What do Gen Z in Nashville do?`)
	if sc2 != nil || !strings.Contains(r2, "can’t answer") {
		t.Fatalf("empty results should decline, got %q score=%v", r2, sc2)
	}
}

func TestRetrieve_ThresholdFail_And_TwoSnippetMerge(t *testing.T) {
	// First: threshold fail
	idx1 := mkIdx(map[string][]search.Result{
		"Gen Z Nashville streaming": {
			{Snippet: "Nashville Gen Z streaming", Score: 0.01}, // will form low combined
		},
	})
	s1 := &MessageService{Index: idx1, Threshold: 0.9}
	r1, sc1 := s1.retrieve(context.Background(), "Gen Z Nashville streaming")
	if sc1 != nil || !strings.Contains(r1, "can’t answer") {
		t.Fatalf("below threshold should decline, got %q score=%v", r1, sc1)
	}

	// Second: two close candidates merge with same strong-entity coverage
	prompt := "Gen Z in Nashville streaming platforms"
	idx2 := mkIdx(map[string][]search.Result{
		prompt: {
			{Snippet: "Gen Z in Nashville streaming platforms adoption.", Score: 0.7},
			{Snippet: "Nashville Gen Z streaming platforms show growth.", Score: 0.69}, // within 90%
		},
	})
	s2 := &MessageService{Index: idx2, Threshold: 0.1}
	out, score := s2.retrieve(context.Background(), prompt)
	if score == nil || !strings.Contains(out, "\n") {
		t.Fatalf("expected merged two-line output with score set, got %q score=%v", out, score)
	}
	if !strings.Contains(out, "Nashville") || !strings.Contains(out, "Gen Z") {
		t.Fatalf("expected strong entities in output, got %q", out)
	}
}

func TestRetrieve_ContentOrEntityGateRemovesAllCandidates(t *testing.T) {
	// Prompt contains content term "platforms" (>=5), but snippet omits it → gate removes.
	prompt := "Gen Z Nashville platforms"
	idx := mkIdx(map[string][]search.Result{
		prompt: {
			{Snippet: "Gen Z and Nashville facts only.", Score: 0.9},
		},
	})
	s := &MessageService{Index: idx}
	out, sc := s.retrieve(context.Background(), prompt)
	if sc != nil || !strings.Contains(out, "can’t answer") {
		t.Fatalf("expected decline due to content-term gate, got %q score=%v", out, sc)
	}
}

// ---------- title helpers ----------

func TestTitleHelpers(t *testing.T) {
	s := &MessageService{}

	// shouldAutoTitle
	if !s.shouldAutoTitle("") || !s.shouldAutoTitle("  new chat  ") || !s.shouldAutoTitle("Untitled") {
		t.Fatalf("shouldAutoTitle failed for placeholders")
	}
	if s.shouldAutoTitle("My Chat") {
		t.Fatalf("shouldAutoTitle true for custom title")
	}

	// generateTitleFromPrompt
	title := s.generateTitleFromPrompt("the state of ai in nashville 2025 and beyond")
	if title == "" || strings.Contains(strings.ToLower(title), "the") {
		t.Fatalf("generateTitleFromPrompt should drop stop words, got %q", title)
	}

	// clipTitle with runes
	s.TitleMaxLen = 5
	if got := s.clipTitle("☃☃☃☃☃☃"); utf8.RuneCountInString(got) != 5 {
		t.Fatalf("clipTitle expected 5 runes, got %d (%q)", utf8.RuneCountInString(got), got)
	}
	s.TitleMaxLen = 0
	if got := s.clipTitle("short"); got != "short" {
		t.Fatalf("clipTitle passthrough failed")
	}

	// locale
	if s.TitleLocaleOrDefault() != language.English {
		t.Fatalf("default locale should be English")
	}
	s.TitleLocale = language.Greek
	if s.TitleLocaleOrDefault() != language.Greek {
		t.Fatalf("custom locale not respected")
	}
}

// ---------- query simplification + markdown utils + precision helpers ----------

func TestSimplifyQuery(t *testing.T) {
	// keep some tokens
	if got := simplifyQuery("How much do Gen Z in Nashville spend on streaming?"); !strings.Contains(got, "nashville") {
		t.Fatalf("simplifyQuery should keep key tokens, got %q", got)
	}
	// all stop-words → fall back to raw tokens
	if got := simplifyQuery("the and or in of"); got != "the and or in of" {
		t.Fatalf("simplifyQuery fallback failed, got %q", got)
	}
}

func TestStripMarkdownTablesAndCollapseWhitespace(t *testing.T) {
	md := `
| text | value |
| --- | --- |
| Gen Z | Nashville |
Some line

| a | b |
|---|---|
| row | 2 |
`
	clean := stripMarkdownTablesToLines(md)
	// header & separators removed, rows collapsed to single lines
	if strings.Contains(strings.ToLower(clean), "text") || strings.Contains(clean, "---") {
		t.Fatalf("headers/separators not removed: %q", clean)
	}
	if !strings.Contains(clean, "Gen Z Nashville") || !strings.Contains(clean, "row 2") {
		t.Fatalf("table rows not joined: %q", clean)
	}

	ws := " a \r\n \n b \n\n c "
	if got := collapseWhitespaceLines(ws); got != "a\nb\nc" {
		t.Fatalf("collapseWhitespaceLines failed: %q", got)
	}
}

func TestExtractQueryTerms_NumberCapsLong_and_OverlapRelevance(t *testing.T) {
	p := `Gen Z in "music streaming" 2025 Nashville growth`
	q := extractQueryTerms(p)
	// tokens shouldn’t include stop-words
	if _, ok := q.allTokens["in"]; ok {
		t.Fatalf("stop-word leaked into tokens")
	}
	// entities include quoted phrase, number, cap, and long token
	expect := []string{"music streaming", "2025", "nashville", "growth"}
	for _, e := range expect {
		found := false
		for k := range q.entities {
			if k == e {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing entity %q in %#v", e, q.entities)
		}
	}

	// overlapRelevance > 0 with boosts, capped at <=1
	snippet := "Nashville sees growth in music streaming among Gen Z by 2025."
	score := overlapRelevance(snippet, q)
	if score <= 0 || score > 1.0 {
		t.Fatalf("overlapRelevance out of bounds: %v", score)
	}
}

func TestIsNumber_IsCapitalized(t *testing.T) {
	if !isNumber("12.5%") || !isNumber("2,000") || !isNumber("A123") {
		t.Fatalf("isNumber should accept digits with letters/punct")
	}
	if isNumber("abc") || isNumber("12!a") {
		t.Fatalf("isNumber false positives")
	}
	if !isCapitalized("Gen") || isCapitalized("gen") || isCapitalized("") {
		t.Fatalf("isCapitalized incorrect")
	}
}

// ---------- Answer(): title update failure branch ----------

func TestMessageService_Answer_AutoTitle_UpdateFails_NoPanic(t *testing.T) {
	db := newMsgDB(t, &domain.Chat{}, &domain.Message{})

	// Seed chat with placeholder title (auto-title should attempt an Update).
	chat := &domain.Chat{ID: "cUpd", UserID: "u1", Title: "New chat"}
	if err := db.Create(chat).Error; err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	// Force ONLY the chat UPDATE to fail (transaction should still succeed).
	if err := db.Callback().Update().Before("gorm:update").Register("force_update_error_chats", func(tx *gorm.DB) {
		if tx.Statement != nil && strings.Contains(tx.Statement.Table, "chats") {
			tx.AddError(errors.New("forced-update-error"))
		}
	}); err != nil {
		t.Fatalf("register update callback: %v", err)
	}

	// Retrieval returns a valid snippet so Answer proceeds to the update step.
	prompt := `Gen Z in Nashville spend on streaming platforms`
	idx := mkIdx(map[string][]search.Result{
		prompt: {
			{Snippet: "In Nashville, Gen Z spend more on streaming platforms.", Score: 0.8},
		},
	})

	s := &MessageService{
		DB:            db,
		Index:         idx,
		Threshold:     0.05,
		MaxReplyRunes: 0,
		TitleMaxLen:   20,
	}

	got, err := s.Answer(context.Background(), "u1", "cUpd", prompt)
	if err != nil {
		t.Fatalf("Answer returned error despite update failure: %v", err)
	}
	if got == nil || got.Role != roleAssistant {
		t.Fatalf("expected assistant message, got %#v", got)
	}

	// Title should remain the original placeholder since update errored.
	var after domain.Chat
	if err := db.First(&after, "id = ?", "cUpd").Error; err != nil {
		t.Fatalf("load chat: %v", err)
	}
	if after.Title != "New chat" {
		t.Fatalf("expected title unchanged due to update error, got %q", after.Title)
	}
}

// ---------- retrieve(): requiredHits==1 strict-fallback path ----------

func TestRetrieve_StrongEntityOne_FallbackByOverlap(t *testing.T) {
	// Prompt with exactly ONE strong entity (capitalized >=4) and no long tokens >=6.
	// "Nashville apps" -> strongEntities={"nashville"}; content terms empty (cap removed).
	prompt := "Nashville apps"
	idx := mkIdx(map[string][]search.Result{
		prompt: {
			// Does NOT contain "Nashville" (so hitCount=0), but overlap on "apps" lifts ov >= 0.20.
			{Snippet: "popular apps trend", Score: 0.30},
		},
	})
	s := &MessageService{Index: idx} // default Threshold=0.20 applies to raw score (0.30 >= 0.20)

	out, sc := s.retrieve(context.Background(), prompt)
	if sc == nil || !strings.Contains(strings.ToLower(out), "apps") {
		t.Fatalf("expected fallback accept via overlap, got out=%q score=%v", out, sc)
	}
}

// ---------- retrieve(): second candidate NOT merged (entity mismatch) ----------

func TestRetrieve_SecondCandidate_NotMerged_When_StrongEntitiesMismatch(t *testing.T) {
	prompt := "Gen Z in Nashville"
	idx := mkIdx(map[string][]search.Result{
		prompt: {
			{Snippet: "Gen Z in Nashville adopt streaming quickly.", Score: 0.70}, // top: hits {"gen z","nashville"}
			{Snippet: "Gen Z trends differ across cities.", Score: 0.69},          // close enough but misses "Nashville"
		},
	})
	s := &MessageService{Index: idx, Threshold: 0.10}
	out, _ := s.retrieve(context.Background(), prompt)
	if strings.Contains(out, "\n") {
		t.Fatalf("second candidate should NOT merge due to missing strong entities; got %q", out)
	}
}

// ---------- retrieve(): no-strong-entities low-ov short snippet rejected ----------

func TestRetrieve_NoStrongEntities_LowOverlap_ShortSnippet_Rejected(t *testing.T) {
	// No strong entities (all tokens <6 and no caps); content terms empty; snippet short + ov<0.10
	prompt := "apps art"
	idx := mkIdx(map[string][]search.Result{
		prompt: {
			{Snippet: "hey", Score: 0.9},
		},
	})
	s := &MessageService{Index: idx}
	out, sc := s.retrieve(context.Background(), prompt)
	if sc != nil || !strings.Contains(out, "can’t answer") {
		t.Fatalf("expected decline for short+low-overlap with no strong entities, got %q score=%v", out, sc)
	}
}

// ---------- generateTitleFromPrompt(): all stopwords -> empty ----------

func TestGenerateTitleFromPrompt_AllStopwords_Empty(t *testing.T) {
	s := &MessageService{}
	if got := s.generateTitleFromPrompt("the and of to in"); got != "" {
		t.Fatalf("expected empty title when all words are stopwords, got %q", got)
	}
}

// ---------- collapseWhitespaceLines(): empty fast-path ----------

func TestCollapseWhitespaceLines_EmptyInput(t *testing.T) {
	if got := collapseWhitespaceLines(""); got != "" {
		t.Fatalf("expected empty output for empty input, got %q", got)
	}
}

// ---------- overlapRelevance(): no tokens -> 0 ----------

func TestOverlapRelevance_NoTokens_ReturnsZero(t *testing.T) {
	var q queryTerms // allTokens nil/empty → function returns 0
	if score := overlapRelevance("anything here", q); score != 0 {
		t.Fatalf("expected 0 when query has no tokens, got %v", score)
	}
}

// ---------- overlapRelevance(): boost cap at 0.24 ----------

func TestOverlapRelevance_BoostCap(t *testing.T) {
	q := queryTerms{
		allTokens:   map[string]struct{}{"x": {}}, // ensure early-exit doesn't trigger
		entities:    map[string]struct{}{},
		entitySlice: []string{"alpha", "beta", "gamma", "delta", "epsilon"}, // 5 * 0.06 = 0.30 → capped
	}
	snippet := "alpha beta gamma delta epsilon"
	score := overlapRelevance(snippet, q)
	if !(score > 0.23 && score <= 0.24+1e-9) {
		t.Fatalf("expected boost capped near 0.24, got %v", score)
	}
}

// ---------- Answer(): no auto-title path (title already custom) ----------

func TestMessageService_Answer_NoAutoTitle_CustomTitle(t *testing.T) {
	db := newMsgDB(t, &domain.Chat{}, &domain.Message{})

	chat := &domain.Chat{ID: "cNoAuto", UserID: "u1", Title: "Already Good"}
	if err := db.Create(chat).Error; err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	prompt := "Gen Z Nashville streaming"
	idx := mkIdx(map[string][]search.Result{
		prompt: {
			{Snippet: "Gen Z in Nashville stream a lot.", Score: 0.8},
		},
	})

	s := &MessageService{DB: db, Index: idx}
	msg, err := s.Answer(context.Background(), "u1", "cNoAuto", prompt)
	if err != nil {
		t.Fatalf("Answer error: %v", err)
	}
	if msg == nil || msg.Role != roleAssistant {
		t.Fatalf("expected assistant message, got %#v", msg)
	}
	var after domain.Chat
	if err := db.First(&after, "id = ?", "cNoAuto").Error; err != nil {
		t.Fatalf("load chat: %v", err)
	}
	if after.Title != "Already Good" {
		t.Fatalf("title should remain unchanged; got %q", after.Title)
	}
}

// ---------- retrieve(): requiredHits==1 AND low overlap -> reject ----------

func TestRetrieve_StrongEntityOne_RejectedWhenOverlapLow(t *testing.T) {
	// One strong entity: "Nashville" (capitalized >=4). No content terms (no len>=5 non-caps).
	prompt := "Nashville apps"
	idx := mkIdx(map[string][]search.Result{
		prompt: {
			// No "Nashville", no "apps" → hitCount=0, ov≈0 → must be rejected by strict floor.
			{Snippet: "top picks today", Score: 0.7},
		},
	})
	s := &MessageService{Index: idx, Threshold: 0.1}
	out, sc := s.retrieve(context.Background(), prompt)
	if sc != nil || !strings.Contains(out, "can’t answer") {
		t.Fatalf("expected rejection (requiredHits==1 & low ov), got %q score=%v", out, sc)
	}
}

// ---------- retrieve(): requiredHits>=2 but snippet matches only one -> reject ----------

func TestRetrieve_TwoStrongEntities_RejectWhenOneMissing(t *testing.T) {
	// Two strong entities: "Gen Z" (compound caps) and "Nashville".
	prompt := "Gen Z Nashville"
	idx := mkIdx(map[string][]search.Result{
		prompt: {
			// Contains only "Gen Z" → hitCount=1 < 2 → reject.
			{Snippet: "Gen Z trends continue nationwide.", Score: 0.9},
		},
	})
	s := &MessageService{Index: idx}
	out, sc := s.retrieve(context.Background(), prompt)
	if sc != nil || !strings.Contains(out, "can’t answer") {
		t.Fatalf("expected rejection due to missing second strong entity, got %q score=%v", out, sc)
	}
}

// ---------- generateTitleFromPrompt(): empty & no-token inputs ----------

func TestGenerateTitleFromPrompt_EmptyAndNoTokens(t *testing.T) {
	s := &MessageService{}

	if got := s.generateTitleFromPrompt("   "); got != "" {
		t.Fatalf("expected empty title for whitespace prompt, got %q", got)
	}
	if got := s.generateTitleFromPrompt("!!! --- ###"); got != "" {
		t.Fatalf("expected empty title for no-token prompt, got %q", got)
	}
}

// ---------- simplifyQuery(): empty input ----------

func TestSimplifyQuery_EmptyInput(t *testing.T) {
	if got := simplifyQuery(""); got != "" {
		t.Fatalf("expected empty simplifyQuery for empty input, got %q", got)
	}
}

// ---------- stripMarkdownTablesToLines(): empty input ----------

func TestStripMarkdownTablesToLines_Empty(t *testing.T) {
	if got := stripMarkdownTablesToLines(""); got != "" {
		t.Fatalf("expected empty for empty input, got %q", got)
	}
}

// ---------- overlapRelevance(): clamp to 1.0 when jaccard 1 and boost > 0 ----------

func TestOverlapRelevance_ClampToOne(t *testing.T) {
	q := queryTerms{
		allTokens:   map[string]struct{}{"nashville": {}, "growth": {}},
		entities:    map[string]struct{}{},
		entitySlice: []string{"nashville"}, // ensure some boost too
	}
	snippet := "Nashville growth" // exact token match → Jaccard = 1.0
	score := overlapRelevance(snippet, q)
	if score != 1.0 {
		t.Fatalf("expected score clamped to 1.0, got %v", score)
	}
}
