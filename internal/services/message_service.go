// Package services – MessageService
//
// This file implements MessageService, the application-level component that
// owns the lifecycle of chat messages and assistant replies. It validates
// inputs, checks chat ownership, performs retrieval over the configured
// search.Index, and persists the user/assistant message pair atomically.
//
// Optional enhancement: it also auto-generates a chat title from the first
// user prompt when the chat still has a default/empty title.
//
// Observability: all public methods are OpenTelemetry-instrumented; spans
// include chat/user identifiers and pagination parameters where applicable.

package services

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/tbourn/go-chat-backend/internal/domain"
	"github.com/tbourn/go-chat-backend/internal/repo"
	"github.com/tbourn/go-chat-backend/internal/search"

	// OpenTelemetry
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

const (
	roleUser      = "user"
	roleAssistant = "assistant"

	// default titles we consider “placeholder” and eligible for auto-generation
	defaultTitleNew      = "New chat"
	defaultTitleUntitled = "Untitled"
)

// MessageService coordinates message persistence and retrieval-based answers.
type MessageService struct {
	DB        *gorm.DB
	Index     search.Index
	Threshold float64

	// Optional guards
	MaxPromptRunes int
	MaxReplyRunes  int

	// Title generation config
	TitleLocale language.Tag
	TitleMaxLen int
}

// Answer validates prompt, verifies chat, retrieves a reply, and persists both
// user and assistant messages atomically. It may auto-generate a chat title.
func (s *MessageService) Answer(ctx context.Context, userID, chatID, prompt string) (*domain.Message, error) {
	tr := otel.Tracer("services/MessageService")
	ctx, span := tr.Start(ctx, "Answer",
		trace.WithAttributes(
			attribute.String("chat.id", chatID),
			attribute.String("user.id", userID),
		),
	)
	defer span.End()

	// Normalize & validate prompt
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, ErrEmptyPrompt
	}
	if s.MaxPromptRunes > 0 && utf8.RuneCountInString(prompt) > s.MaxPromptRunes {
		return nil, ErrTooLong
	}

	// Ensure the chat exists and belongs to the user
	chat, err := repo.GetChat(ctx, s.DB, chatID, userID)
	if err != nil {
		return nil, ErrChatNotFound
	}

	// Build reply from retrieval
	reply, score := s.retrieve(ctx, prompt)

	// Persist user + assistant (and maybe update title) in one transaction
	var assistantMsg *domain.Message
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := repo.CreateMessage(tx, chatID, roleUser, prompt, nil); err != nil {
			return err
		}
		m, err := repo.CreateMessage(tx, chatID, roleAssistant, reply, score)
		if err != nil {
			return err
		}
		assistantMsg = m

		// Auto-title if placeholder
		if s.shouldAutoTitle(chat.Title) {
			gen := s.generateTitleFromPrompt(prompt)
			if gen != "" {
				gen = s.clipTitle(gen)
				if uerr := tx.Model(&domain.Chat{}).Where("id = ?", chatID).Update("title", gen).Error; uerr == nil {
					chat.Title = gen
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Clip reply length if configured
	if s.MaxReplyRunes > 0 && utf8.RuneCountInString(assistantMsg.Content) > s.MaxReplyRunes {
		runes := []rune(assistantMsg.Content)
		assistantMsg.Content = string(runes[:s.MaxReplyRunes])
	}

	return assistantMsg, nil
}

// ListPage returns paginated messages for a chat.
func (s *MessageService) ListPage(ctx context.Context, chatID string, page, pageSize int) ([]domain.Message, int64, error) {
	tr := otel.Tracer("services/MessageService")
	ctx, span := tr.Start(ctx, "ListPage",
		trace.WithAttributes(
			attribute.String("chat.id", chatID),
			attribute.Int("page", page),
			attribute.Int("page_size", pageSize),
		),
	)
	defer span.End()

	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	// Ensure chat exists
	var chatCount int64
	if err := s.DB.WithContext(ctx).Model(&domain.Chat{}).Where("id = ?", chatID).Count(&chatCount).Error; err != nil {
		return nil, 0, err
	}
	if chatCount == 0 {
		return nil, 0, ErrChatNotFound
	}

	total, err := repo.CountMessages(s.DB.WithContext(ctx), chatID)
	if err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return []domain.Message{}, 0, nil
	}

	items, err := repo.ListMessagesPage(s.DB.WithContext(ctx), chatID, offset, pageSize)
	return items, total, err
}

// --- Retrieval with precision filtering and re-ranking ---
//
// Strategy:
//  1. Pull TopK=10 candidates.
//  2. Extract query entities/keywords from prompt.
//  3. Build generic "content terms" (non-cap, len>=5, + long quoted phrases), minus generic words.
//  4. Build STRONG entities = long/number entities + compound caps ("Gen Z", "United States")
//     + single proper nouns (capitalized len>=4, e.g., "Nashville").
//  5. Compute overlap (Jaccard + small phrase boosts) and blend with normalized index score.
//  6. Gates: require a content-term hit; enforce strict strong-entity coverage (when query is specific).
//  7. Return 1–2 snippets; only add the second if it matches the same strong entities as top.
func (s *MessageService) retrieve(ctx context.Context, prompt string) (reply string, score *float64) {
	tr := otel.Tracer("services/MessageService")
	_, span := tr.Start(ctx, "retrieve",
		trace.WithAttributes(attribute.String("query", prompt)),
	)
	defer span.End()

	if s.Index == nil {
		return "I can’t answer that from the provided data.", nil
	}

	// Pull more candidates than we will answer with
	const K = 10
	results := s.Index.TopK(prompt, K)
	if len(results) == 0 {
		if simplified := simplifyQuery(prompt); simplified != "" && simplified != prompt {
			results = s.Index.TopK(simplified, K)
		}
	}
	if len(results) == 0 {
		return "I can’t answer that from the provided data.", nil
	}

	// Extract query terms/entities
	q := extractQueryTerms(prompt)

	// ---------- Build generic "content terms" from the prompt ----------
	lowerPrompt := strings.ToLower(prompt)
	contentSet := make(map[string]struct{})

	// Very generic words to drop from content terms (keeps focus on nouns like "investments", "affluent")
	genericContentDrop := map[string]struct{}{
		"interested": {}, "interest": {}, "interests": {},
		"percentage": {}, "percent": {}, "share": {},
		"likely": {}, "likelihood": {}, "compared": {}, "comparison": {}, "average": {}, "overall": {},
		"people": {}, "person": {},
		"new": {}, "brands": {}, "products": {}, "find": {}, "out": {}, "about": {},
	}

	// Base tokens from the prompt (non-stopword, len>=5)
	for _, tok := range qwordRE.FindAllString(lowerPrompt, -1) {
		if _, stop := qStop[tok]; stop {
			continue
		}
		if len(tok) >= 5 {
			if _, drop := genericContentDrop[tok]; drop {
				continue
			}
			contentSet[tok] = struct{}{}
		}
	}
	// Quoted phrases (>=5 chars when trimmed)
	for _, m := range quotedPhraseRE.FindAllStringSubmatch(prompt, -1) {
		for i := 1; i < len(m); i++ {
			if p := strings.ToLower(strings.TrimSpace(m[i])); len(p) >= 5 {
				if _, drop := genericContentDrop[p]; drop {
					continue
				}
				contentSet[p] = struct{}{}
			}
		}
	}
	// Strip capitalized words from content terms (treat them as qualifiers, not topics)
	for _, raw := range alnumRE.FindAllString(prompt, -1) {
		if isCapitalized(raw) {
			delete(contentSet, strings.ToLower(raw))
		}
	}

	contentTerms := make([]string, 0, len(contentSet))
	for t := range contentSet {
		contentTerms = append(contentTerms, t)
	}
	containsAny := func(sLower string, terms []string) bool {
		for _, t := range terms {
			if t != "" && strings.Contains(sLower, t) {
				return true
			}
		}
		return false
	}
	// -------------------------------------------------------------------

	// ---------- Strong entities from the query (+ compound caps) ----------
	strongEntities := make(map[string]struct{})

	// Long/number entities from q.entities
	for e := range q.entities {
		if isNumber(e) || len(e) >= 5 {
			strongEntities[e] = struct{}{}
		}
	}

	// Compound caps: bigrams/trigrams like "Gen Z", "United States", "New York"
	toks := alnumRE.FindAllString(prompt, -1)
	addPhrase := func(parts ...string) {
		ph := strings.ToLower(strings.Join(parts, " "))
		if strings.TrimSpace(ph) != "" {
			strongEntities[ph] = struct{}{}
		}
	}
	for i := 0; i+1 < len(toks); i++ {
		a, b := toks[i], toks[i+1]
		// "Gen" + single capital letter (X, Z, etc.)
		if strings.EqualFold(a, "Gen") && len(b) == 1 && isCapitalized(b) {
			addPhrase(a, b) // → "gen z"
		}
		// consecutive capitalized words → bigram (and maybe trigram)
		if isCapitalized(a) && isCapitalized(b) {
			addPhrase(a, b)
			if i+2 < len(toks) {
				c := toks[i+2]
				if isCapitalized(c) {
					addPhrase(a, b, c)
				}
			}
		}
	}

	// Single proper nouns (capitalized len>=4), e.g., "Nashville"
	for _, w := range toks {
		if isCapitalized(w) && utf8.RuneCountInString(w) >= 4 {
			strongEntities[strings.ToLower(w)] = struct{}{}
		}
	}

	// Count hits of strong entities in a snippet
	countStrongHits := func(snippet string) (int, map[string]struct{}) {
		hit := make(map[string]struct{}, len(strongEntities))
		if len(strongEntities) == 0 {
			return 0, hit
		}
		sn := strings.ToLower(snippet)
		for e := range strongEntities {
			if e != "" && strings.Contains(sn, e) {
				hit[e] = struct{}{}
			}
		}
		return len(hit), hit
	}

	// Required hits based on strong entities
	requiredHits := 0
	switch n := len(strongEntities); {
	case n >= 2:
		requiredHits = 2
	case n == 1:
		requiredHits = 1
	default:
		requiredHits = 0
	}
	// -------------------------------------------------------------------

	// Normalize index scores to [0,1]
	maxScore := 0.0
	for _, r := range results {
		if r.Score > maxScore {
			maxScore = r.Score
		}
	}
	if maxScore == 0 {
		maxScore = 1
	}

	type cand struct {
		text         string
		indexScore   float64
		overlapRel   float64
		combined     float64
		strongEntHit map[string]struct{} // which strong query entities this snippet contains
	}

	// Floors
	const strictFloor = 0.20  // used only when query has 0–1 strong entities
	const lenientFloor = 0.10 // when strong entities satisfied (or none)

	cands := make([]cand, 0, len(results))
	for _, r := range results {
		clean := stripMarkdownTablesToLines(strings.TrimSpace(r.Snippet))
		if clean == "" {
			continue
		}
		sLower := strings.ToLower(clean)

		ov := overlapRelevance(clean, q) // [0,1]
		ns := r.Score / maxScore         // [0,1]
		combined := 0.5*ns + 0.5*ov

		// 1) Content-term gate: if query has content terms, require at least one in snippet
		if len(contentTerms) > 0 && !containsAny(sLower, contentTerms) {
			continue
		}

		// 2) Strong-entity gate
		hitCount, hitSet := countStrongHits(clean)

		if requiredHits >= 2 {
			// Query is specific → REQUIRE at least 2 strong-entity hits (no overlap escape)
			if hitCount < 2 {
				continue
			}
		} else if requiredHits == 1 {
			// Query has one strong entity → require it, or strong overlap as rare fallback
			if hitCount < 1 && ov < strictFloor {
				continue
			}
		} else {
			// No strong entities in query → still avoid trivial snippets
			if ov < lenientFloor && utf8.RuneCountInString(clean) < 12 {
				continue
			}
		}

		// Small tie-break boost for better strong-entity coverage
		if hitCount > requiredHits {
			combined += 0.03
		}

		cands = append(cands, cand{
			text:         clean,
			indexScore:   r.Score,
			overlapRel:   ov,
			combined:     combined,
			strongEntHit: hitSet,
		})
	}

	// NEW: decline if nothing passes the precision gates
	if len(cands) == 0 {
		return "I can’t answer that from the provided data.", nil
	}

	// Sort by combined descending
	sort.Slice(cands, func(i, j int) bool { return cands[i].combined > cands[j].combined })

	top := cands[0]

	// Threshold on blended score
	thr := s.Threshold
	if thr <= 0 {
		thr = 0.20
	}
	if top.indexScore < thr {
		return "I can’t answer that from the provided data.", nil
	}

	// Only add a second if it's close AND covers at least the same strong entities as top.
	out := top.text
	if len(cands) > 1 && cands[1].combined >= top.combined*0.9 {
		ok := true
		for e := range top.strongEntHit {
			if _, hit := cands[1].strongEntHit[e]; !hit {
				ok = false
				break
			}
		}
		if ok {
			out = out + "\n" + cands[1].text
		}
	}

	v := top.indexScore
	return collapseWhitespaceLines(out), &v
}

// shouldAutoTitle reports whether the current title is a placeholder.
func (s *MessageService) shouldAutoTitle(current string) bool {
	t := strings.TrimSpace(strings.ToLower(current))
	return t == "" || t == strings.ToLower(defaultTitleNew) || t == strings.ToLower(defaultTitleUntitled)
}

// generateTitleFromPrompt derives a concise title from the prompt.
func (s *MessageService) generateTitleFromPrompt(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ""
	}
	toks := titleWordRE.FindAllString(strings.ToLower(prompt), -1)
	if len(toks) == 0 {
		return ""
	}

	titleCaser := cases.Title(s.TitleLocaleOrDefault())
	out := make([]string, 0, 8)

	for _, w := range toks {
		if _, skip := titleStopWords[w]; skip {
			continue
		}
		out = append(out, titleCaser.String(w))
		if len(out) >= 8 {
			break
		}
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, " ")
}

// clipTitle truncates a generated title to the configured maximum rune length.
func (s *MessageService) clipTitle(title string) string {
	max := s.TitleMaxLen
	if max <= 0 {
		max = 60
	}
	if utf8.RuneCountInString(title) > max {
		return string([]rune(title)[:max])
	}
	return title
}

// TitleLocaleOrDefault returns the configured locale for casing or English if unset.
func (s *MessageService) TitleLocaleOrDefault() language.Tag {
	if s.TitleLocale == language.Und {
		return language.English
	}
	return s.TitleLocale
}

// --- Title generation helpers ---

// Extract Unicode letters with optional trailing numbers (e.g., "gwi2025").
var titleWordRE = regexp.MustCompile(`[\p{L}]+[\p{N}]*`)

// Minimal English stop-words set for compact titles.
var titleStopWords = map[string]struct{}{
	"the": {}, "a": {}, "an": {}, "and": {}, "or": {}, "of": {}, "to": {}, "in": {},
	"is": {}, "are": {}, "for": {}, "on": {}, "with": {}, "by": {}, "from": {},
	"at": {}, "as": {}, "that": {}, "this": {}, "it": {}, "be": {}, "was": {}, "were": {},
}

// --- Query simplification for retrieval fallback ---

// qwordRE: words (letters/digits). We build a keyword query from these.
var qwordRE = regexp.MustCompile(`[\p{L}\p{N}]+`)

// qStop: words to drop when simplifying the question to keywords.
var qStop = map[string]struct{}{
	"the": {}, "a": {}, "an": {}, "and": {}, "or": {}, "of": {}, "to": {}, "in": {},
	"is": {}, "are": {}, "for": {}, "on": {}, "with": {}, "by": {}, "from": {},
	"at": {}, "as": {}, "that": {}, "this": {}, "it": {}, "be": {}, "was": {}, "were": {},
	"how": {}, "much": {}, "more": {}, "likely": {}, "do": {}, "does": {}, "what": {}, "which": {},
	"new": {}, "brands": {}, "products": {}, "find": {}, "out": {}, "about": {},
}

// simplifyQuery converts a long NL question into a compact keyword string.
func simplifyQuery(s string) string {
	toks := qwordRE.FindAllString(strings.ToLower(s), -1)
	if len(toks) == 0 {
		return ""
	}
	keep := make([]string, 0, len(toks))
	for _, t := range toks {
		if _, stop := qStop[t]; stop {
			continue
		}
		keep = append(keep, t)
	}
	if len(keep) == 0 {
		// nothing left after filtering; fall back to all tokens
		return strings.Join(toks, " ")
	}
	return strings.Join(keep, " ")
}

// --- Markdown table cleanup utilities ---

// Matches a table row and a separator row like: | --- | :---: | ---: |
var (
	mdTableRow = regexp.MustCompile(`^\s*\|.*\|\s*$`)
	mdSepRow   = regexp.MustCompile(`^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)+\|?\s*$`)
)

// stripMarkdownTablesToLines converts markdown table blocks into one-line facts,
// skipping header & '---' separator lines, and preserving non-table lines.
func stripMarkdownTablesToLines(s string) string {
	if s == "" {
		return ""
	}

	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))

	i := 0
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])

		// Detect a markdown table by header row + separator row
		if mdTableRow.MatchString(line) && i+1 < len(lines) && mdSepRow.MatchString(strings.TrimSpace(lines[i+1])) {
			// Skip header + separator
			i += 2

			// Consume body rows; keep cells but drop pipes
			for i < len(lines) && mdTableRow.MatchString(strings.TrimSpace(lines[i])) {
				row := strings.TrimSpace(lines[i])
				row = strings.TrimPrefix(row, "|")
				row = strings.TrimSuffix(row, "|")
				cells := strings.Split(row, "|")
				for j := range cells {
					cells[j] = strings.TrimSpace(cells[j])
				}
				cleaned := strings.Join(cells, " ")
				if cleaned != "" {
					out = append(out, cleaned)
				}
				i++
			}
			continue
		}

		// Non-table line: keep if non-empty
		if line != "" {
			out = append(out, line)
		}
		i++
	}

	return strings.Join(out, "\n")
}

// collapseWhitespaceLines trims each line, collapses internal whitespace to a single
// space, and drops empty lines entirely.
func collapseWhitespaceLines(s string) string {
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	raw := strings.Split(s, "\n")
	out := make([]string, 0, len(raw))
	for _, ln := range raw {
		parts := strings.Fields(ln)
		if len(parts) == 0 {
			continue
		}
		out = append(out, strings.Join(parts, " "))
	}
	return strings.Join(out, "\n")
}

// --- Precision helpers ---

var (
	quotedPhraseRE = regexp.MustCompile(`"([^"]+)"|‘([^’]+)’|“([^”]+)”|\'([^\']+)\'`)
	alnumRE        = regexp.MustCompile(`[\p{L}\p{N}]+`)
)

// queryTerms holds both tokens and strong "entities".
type queryTerms struct {
	allTokens   map[string]struct{}
	entities    map[string]struct{} // quoted phrases, numbers, Capitalized words, long tokens
	entitySlice []string            // for quick iteration/phrase checks
}

// extractQueryTerms pulls tokens and entities from the prompt.
func extractQueryTerms(prompt string) queryTerms {
	p := strings.TrimSpace(prompt)
	lower := strings.ToLower(p)

	tokens := make(map[string]struct{})
	for _, t := range alnumRE.FindAllString(lower, -1) {
		if _, stop := qStop[t]; stop {
			continue
		}
		tokens[t] = struct{}{}
	}

	entities := make(map[string]struct{})

	// quoted phrases
	for _, m := range quotedPhraseRE.FindAllStringSubmatch(p, -1) {
		for i := 1; i < len(m); i++ {
			if m[i] != "" {
				ph := strings.TrimSpace(m[i])
				if ph != "" {
					entities[strings.ToLower(ph)] = struct{}{}
				}
			}
		}
	}

	// numbers & capitalized words & long tokens
	for _, raw := range alnumRE.FindAllString(p, -1) {
		lc := strings.ToLower(raw)
		if _, stop := qStop[lc]; stop {
			continue
		}
		if isNumber(raw) || isCapitalized(raw) || len(lc) >= 6 {
			entities[lc] = struct{}{}
		}
	}

	es := make([]string, 0, len(entities))
	for k := range entities {
		es = append(es, k)
	}
	return queryTerms{
		allTokens:   tokens,
		entities:    entities,
		entitySlice: es,
	}
}

func isNumber(s string) bool {
	hasDigit := false
	for _, r := range s {
		if unicode.IsDigit(r) {
			hasDigit = true
		} else if !(unicode.IsLetter(r) || r == '.' || r == ',' || r == '%') {
			return false
		}
	}
	return hasDigit
}

func isCapitalized(s string) bool {
	rs := []rune(s)
	if len(rs) == 0 {
		return false
	}
	return unicode.IsUpper(rs[0])
}

// overlapRelevance computes a simple Jaccard overlap between query tokens and snippet tokens,
// with small boosts for exact entity phrase matches.
func overlapRelevance(snippet string, q queryTerms) float64 {
	if len(q.allTokens) == 0 {
		return 0
	}
	snippetLower := strings.ToLower(snippet)
	sTokens := make(map[string]struct{})
	for _, t := range alnumRE.FindAllString(snippetLower, -1) {
		sTokens[t] = struct{}{}
	}

	inter := 0
	for t := range q.allTokens {
		if _, ok := sTokens[t]; ok {
			inter++
		}
	}
	union := len(sTokens) + len(q.allTokens) - inter
	if union == 0 {
		return 0
	}
	j := float64(inter) / float64(union)

	// phrase/entity boost
	boost := 0.0
	for _, e := range q.entitySlice {
		if e == "" {
			continue
		}
		if strings.Contains(snippetLower, e) {
			boost += 0.06 // small additive boost per entity match
		}
	}
	if boost > 0.24 {
		boost = 0.24 // cap
	}
	score := j + boost
	if score > 1.0 {
		score = 1.0
	}
	return score
}
