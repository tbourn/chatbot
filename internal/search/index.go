// Package search provides a simple, deterministic, concurrency-safe in-memory
// search index built from Markdown paragraphs. It is intentionally small and
// dependency-free, but engineered with production-grade ergonomics:
//
//   - No logging in the library (callers decide how/what to log)
//   - Clear, documented types and functional options (Option pattern)
//   - Unicode-aware tokenization with optional stop-word removal
//   - Immutable, read-only index after construction (safe for concurrent use)
//   - Deterministic scoring and sorting (stable order for ties)
//   - Sensible defaults (paragraph filtering, result caps)
//   - Backward-compatible Index interface (TopK(query, k int) []Result)
//
// Scoring uses Jaccard similarity between the query token set and each
// paragraph’s token set: score = |Q ∩ P| / |Q ∪ P|.
package search

import (
	"bytes"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Result is a ranked snippet with its similarity score.
type Result struct {
	Snippet string
	Score   float64
}

// Index is the minimal interface implemented by all search indices.
type Index interface {
	TopK(query string, k int) []Result
}

// ----------------------------------------------------------------------------
// Options

type Option func(*config)

type config struct {
	minParagraphRunes int
	stopwords         map[string]struct{}
	maxDocs           int
}

func defaultConfig() config {
	return config{
		minParagraphRunes: 40,
		stopwords:         nil,
		maxDocs:           0,
	}
}

func WithMinParagraphRunes(n int) Option {
	return func(c *config) {
		if n >= 0 {
			c.minParagraphRunes = n
		}
	}
}

func WithStopwords(words []string) Option {
	return func(c *config) {
		m := make(map[string]struct{}, len(words))
		for _, w := range words {
			w = strings.ToLower(strings.TrimSpace(w))
			if w != "" {
				m[w] = struct{}{}
			}
		}
		if len(m) > 0 {
			c.stopwords = m
		}
	}
}

func WithMaxDocs(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.maxDocs = n
		}
	}
}

// ----------------------------------------------------------------------------
// Implementation

type doc struct {
	text   string
	tokens map[string]struct{}
	tLen   int
}

type index struct {
	cfg  config
	docs []doc
}

// NewIndexFromMarkdown builds an Index by reading the Markdown at path
// and delegating to NewIndexFromReader (in-memory).
func NewIndexFromMarkdown(path string, opts ...Option) (Index, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return &index{cfg: defaultConfig(), docs: nil}, err
	}
	return NewIndexFromReader(bytes.NewReader(b), opts...)
}

// NewIndexFromReader builds an Index from UTF-8 text provided by r.
// The reader is fully consumed; paragraphs are split on blank lines.
func NewIndexFromReader(r io.Reader, opts ...Option) (Index, error) {
	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}
	all, err := io.ReadAll(r)
	if err != nil {
		return &index{cfg: cfg, docs: nil}, err
	}
	paras := splitParasFromBytes(all)
	return buildIndex(paras, cfg), nil
}

// NewIndexFromStrings builds an Index directly from a slice of paragraphs.
func NewIndexFromStrings(paragraphs []string, opts ...Option) Index {
	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}
	return buildIndex(paragraphs, cfg)
}

func buildIndex(paragraphs []string, cfg config) *index {
	docs := make([]doc, 0, len(paragraphs))
	count := 0
	for _, raw := range paragraphs {
		t := strings.TrimSpace(normalizeWhitespace(raw))
		if t == "" {
			continue
		}
		if cfg.minParagraphRunes > 0 && utf8.RuneCountInString(t) < cfg.minParagraphRunes {
			continue
		}
		toks := tokenize(t, cfg.stopwords)
		if len(toks) == 0 {
			continue
		}
		docs = append(docs, doc{text: t, tokens: toks, tLen: len(toks)})
		count++
		if cfg.maxDocs > 0 && count >= cfg.maxDocs {
			break
		}
	}
	return &index{cfg: cfg, docs: docs}
}

// TopK returns up to k best-matching paragraphs by Jaccard similarity.
func (i *index) TopK(q string, k int) []Result {
	if len(i.docs) == 0 {
		return nil
	}
	if strings.TrimSpace(q) == "" {
		return nil
	}
	if k <= 0 {
		k = 3
	}
	qTokens := tokenize(q, i.cfg.stopwords)
	if len(qTokens) == 0 {
		return nil
	}
	qLen := len(qTokens)

	type scored struct {
		snippet  string
		score    float64
		lenRunes int
	}

	buf := make([]scored, 0, min(k*4, len(i.docs)))
	for _, d := range i.docs {
		over := overlap(qTokens, d.tokens)
		if over == 0 {
			continue
		}
		union := float64(qLen + d.tLen - over)
		if union <= 0 {
			continue
		}
		score := float64(over) / union
		if score <= 0 {
			continue
		}
		buf = append(buf, scored{
			snippet:  d.text,
			score:    score,
			lenRunes: utf8.RuneCountInString(d.text),
		})
	}
	if len(buf) == 0 {
		return nil
	}

	sort.SliceStable(buf, func(a, b int) bool {
		if buf[a].score != buf[b].score {
			return buf[a].score > buf[b].score
		}
		if buf[a].lenRunes != buf[b].lenRunes {
			return buf[a].lenRunes < buf[b].lenRunes
		}
		return buf[a].snippet < buf[b].snippet
	})

	if k > len(buf) {
		k = len(buf)
	}
	out := make([]Result, k)
	for i := 0; i < k; i++ {
		out[i] = Result{Snippet: buf[i].snippet, Score: buf[i].score}
	}
	return out
}

// ----------------------------------------------------------------------------
// Helpers

var wordRE = regexp.MustCompile(`\p{L}+\p{N}*`)

func tokenize(s string, stop map[string]struct{}) map[string]struct{} {
	s = strings.ToLower(s)
	words := wordRE.FindAllString(s, -1)
	if len(words) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(words))
	for _, w := range words {
		if w == "" {
			continue
		}
		if stop != nil {
			if _, skip := stop[w]; skip {
				continue
			}
		}
		out[w] = struct{}{}
	}
	return out
}

func overlap(a, b map[string]struct{}) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	n := 0
	if len(a) > len(b) {
		a, b = b, a
	}
	for k := range a {
		if _, ok := b[k]; ok {
			n++
		}
	}
	return n
}

func normalizeWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\r' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return b.String()
}

var paraSplitRE = regexp.MustCompile(`\n\s*\n`)

func splitParasFromBytes(all []byte) []string {
	raw := string(all)
	chunks := paraSplitRE.Split(raw, -1)
	out := make([]string, 0, len(chunks))
	for _, c := range chunks {
		if t := strings.TrimSpace(c); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
