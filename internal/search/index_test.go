package search

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// ---------- tiny io.Reader that always errors ----------
type boomReader struct{}

func (boomReader) Read(_ []byte) (int, error) { return 0, errors.New("boom") }

// ---------- helpers ----------
func writeIndexTemp(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	return p
}

// ---------- Options + defaultConfig ----------
func TestOptionsAndDefaults(t *testing.T) {
	def := defaultConfig()
	if def.minParagraphRunes != 40 || def.stopwords != nil || def.maxDocs != 0 {
		t.Fatalf("defaultConfig unexpected: %#v", def)
	}

	// Apply options (including no-ops)
	cfg := def
	WithMinParagraphRunes(10)(&cfg)
	if cfg.minParagraphRunes != 10 {
		t.Fatalf("WithMinParagraphRunes failed: %d", cfg.minParagraphRunes)
	}
	WithMinParagraphRunes(-5)(&cfg) // no-op
	if cfg.minParagraphRunes != 10 {
		t.Fatalf("negative minParagraphRunes should be ignored")
	}

	WithStopwords([]string{"  The ", "", "An"})(&cfg)

	if _, ok := cfg.stopwords["the"]; !ok {
		t.Fatalf("WithStopwords failed (missing 'the'): %#v", cfg.stopwords)
	}
	if _, ok := cfg.stopwords["an"]; !ok {
		t.Fatalf("WithStopwords failed (missing 'an'): %#v", cfg.stopwords)
	}

	cfg2 := def
	WithStopwords(nil)(&cfg2) // remains nil (no change because m len==0)
	if cfg2.stopwords != nil {
		t.Fatalf("empty stopwords should remain nil")
	}

	WithMaxDocs(2)(&cfg)
	if cfg.maxDocs != 2 {
		t.Fatalf("WithMaxDocs failed: %d", cfg.maxDocs)
	}
	WithMaxDocs(0)(&cfg) // no-op
	if cfg.maxDocs != 2 {
		t.Fatalf("non-positive maxDocs should be ignored")
	}
}

// ---------- NewIndexFromMarkdown ----------
func TestNewIndexFromMarkdown_SuccessAndError(t *testing.T) {
	dir := t.TempDir()
	md := "Alpha beta gamma.\n\nDelta epsilon zeta."
	p := writeIndexTemp(t, dir, "doc.md", md)

	idx, err := NewIndexFromMarkdown(p, WithMinParagraphRunes(0))
	if err != nil {
		t.Fatalf("NewIndexFromMarkdown error: %v", err)
	}
	res := idx.TopK("alpha zeta", 5)
	if len(res) == 0 {
		t.Fatalf("expected some results")
	}

	// error path: missing file -> returns index with default cfg and err != nil
	_, err2 := NewIndexFromMarkdown(filepath.Join(dir, "missing.md"))
	if err2 == nil {
		t.Fatalf("expected error for missing file")
	}
}

// ---------- NewIndexFromReader ----------
func TestNewIndexFromReader_ErrorAndSuccess(t *testing.T) {
	// error from io.ReadAll
	_, err := NewIndexFromReader(boomReader{})
	if err == nil {
		t.Fatalf("expected read error")
	}
	// success
	r := bytes.NewBufferString("Para one.\n\nPara two two.")
	idx, err := NewIndexFromReader(r, WithMinParagraphRunes(0))
	if err != nil {
		t.Fatalf("NewIndexFromReader success err: %v", err)
	}
	out := idx.TopK("two", 3)
	if len(out) == 0 {
		t.Fatalf("expected results from reader-built index")
	}
}

// ---------- NewIndexFromStrings + buildIndex filters ----------
func TestBuildIndex_FiltersAndMaxDocs(t *testing.T) {
	paras := []string{
		"",                    // skipped
		" \t \r  ",            // skipped
		"short",               // filtered by minParagraphRunes when >0
		"The and a",           // all stopwords -> tokens empty -> skipped
		"Keep This Paragraph", // valid
		"Another paragraph here with words",
	}
	// minParagraphRunes=0 so "short" allowed; but we'll set a positive first
	idx1 := NewIndexFromStrings(paras, WithMinParagraphRunes(6), WithStopwords([]string{"the", "and", "a"}))
	// Only "Keep This Paragraph" and "Another ..." pass (short=5 runes -> filtered)
	if ii, ok := idx1.(*index); ok {
		if len(ii.docs) != 2 {
			t.Fatalf("expected 2 docs, got %d", len(ii.docs))
		}
	}

	// maxDocs cap
	idx2 := NewIndexFromStrings(paras, WithMinParagraphRunes(0), WithMaxDocs(1))
	if ii, ok := idx2.(*index); ok {
		if len(ii.docs) != 1 {
			t.Fatalf("maxDocs cap failed, got %d", len(ii.docs))
		}
	}
}

// ---------- TopK branches & tie-breakers ----------
func TestTopK_BranchesAndSorting(t *testing.T) {
	// empty docs
	empty := &index{cfg: defaultConfig(), docs: nil}
	if res := empty.TopK("x", 3); res != nil {
		t.Fatalf("empty index should return nil")
	}
	// blank query
	idx := NewIndexFromStrings([]string{"alpha beta", "alpha beta gamma"}, WithMinParagraphRunes(0))
	if out := idx.TopK("   ", 2); out != nil {
		t.Fatalf("blank query should return nil")
	}
	// qTokens empty (all stopwords)
	idxStop := NewIndexFromStrings([]string{"alpha beta"}, WithStopwords([]string{"alpha", "beta"}), WithMinParagraphRunes(0))
	if out := idxStop.TopK("alpha beta", 2); out != nil {
		t.Fatalf("query becoming empty should yield nil")
	}

	// Build index to test scoring + tie-breakers:
	// d1 tokens == query -> score 1.0
	// d2 has extra token -> lower score
	// d3 tokens == query but same rune length as d1 -> tie on score & len, alphabetic tie-break on snippet
	idx2 := NewIndexFromStrings([]string{
		"alpha beta",       // d1 (score 1)
		"alpha beta gamma", // d2 (score < 1)
		"beta alpha",       // d3 (score 1; same length as d1)
		"delta epsilon",    // d4 (zero overlap -> skipped)
	}, WithMinParagraphRunes(0))

	// k<=0 defaults to 3, but we expect top 3 candidates anyway (d4 skipped)
	got := idx2.TopK("alpha beta", 0)
	if len(got) != 3 {
		t.Fatalf("expected 3 results (k default), got %d", len(got))
	}
	// order: d1 (alpha beta) score 1, then d3 (beta alpha) score 1 but alphabetically after,
	// then d2 (alpha beta gamma)
	if got[0].Snippet != "alpha beta" || got[1].Snippet != "beta alpha" || got[2].Snippet != "alpha beta gamma" {
		t.Fatalf("unexpected order: %#v", got)
	}
	// Ensure zero-overlap doc excluded
	for _, r := range got {
		if r.Snippet == "delta epsilon" {
			t.Fatalf("zero-overlap document should be excluded")
		}
	}
}

// ---------- Helpers: tokenize / overlap / whitespace / split / min ----------
func TestHelpers_TokenizeOverlapWhitespaceSplitMin(t *testing.T) {
	// tokenize
	toks := tokenize("Hello HELLO 123 world", nil)

	if _, ok := toks["hello"]; !ok {
		t.Fatalf("tokenize(lower) missing 'hello': %#v", toks)
	}
	if _, ok := toks["world"]; !ok {
		t.Fatalf("tokenize(lower) missing 'world': %#v", toks)
	}

	stop := map[string]struct{}{"hello": {}}
	toks2 := tokenize("Hello world", stop)

	if _, ok := toks2["hello"]; ok {
		t.Fatalf("tokenize(stopwords) should have removed 'hello': %#v", toks2)
	}
	if _, ok := toks2["world"]; !ok {
		t.Fatalf("tokenize(stopwords) missing 'world': %#v", toks2)
	}

	if toks3 := tokenize("$$$ !!!", nil); toks3 != nil {
		t.Fatalf("tokenize should return nil when no words")
	}

	// overlap
	if overlap(nil, toks) != 0 || overlap(toks, nil) != 0 {
		t.Fatalf("overlap with nil should be 0")
	}
	if overlap(map[string]struct{}{"a": {}}, map[string]struct{}{"b": {}}) != 0 {
		t.Fatalf("overlap disjoint should be 0")
	}
	if overlap(map[string]struct{}{"a": {}, "b": {}}, map[string]struct{}{"b": {}, "c": {}}) != 1 {
		t.Fatalf("overlap count wrong")
	}

	// normalizeWhitespace
	ws := "alpha\t beta\r  gamma"
	if got := normalizeWhitespace(ws); got != "alpha beta gamma" {
		t.Fatalf("normalizeWhitespace failed: %q", got)
	}

	// splitParasFromBytes
	raw := []byte("p1\n\n\n  \n p2 \n\np3")
	ps := splitParasFromBytes(raw)
	if len(ps) != 3 || ps[0] != "p1" || ps[1] != "p2" || ps[2] != "p3" {
		t.Fatalf("splitParasFromBytes failed: %#v", ps)
	}

	// min
	if min(2, 5) != 2 || min(5, 2) != 2 {
		t.Fatalf("min failed")
	}
}

func TestTopK_KGreaterThanLen_And_LenRunesTieBreak(t *testing.T) {
	// Two docs with EXACTLY the same token set as the query ("alpha", "beta"),
	// but different snippet lengths → same score, tie broken by shorter lenRunes.
	idx := NewIndexFromStrings([]string{
		"alpha beta",   // shorter snippet
		"alpha beta!!", // longer snippet (punctuation kept in snippet length)
	}, WithMinParagraphRunes(0))

	out := idx.TopK("alpha beta", 10) // k > len(buf) to hit the cap branch
	if len(out) != 2 {
		t.Fatalf("expected 2 results, got %d", len(out))
	}
	if out[0].Snippet != "alpha beta" || out[1].Snippet != "alpha beta!!" {
		t.Fatalf("lenRunes tie-break failed: %#v", out)
	}
	// both should have perfect score (same token set)
	if out[0].Score != 1.0 || out[1].Score != 1.0 {
		t.Fatalf("expected scores 1.0, got %+v", out)
	}
}

func TestTopK_NoOverlap_ReturnsNil(t *testing.T) {
	// Query has tokens, but no documents overlap → len(buf)==0 → nil
	idx := NewIndexFromStrings([]string{
		"delta epsilon",
		"zeta eta theta",
	}, WithMinParagraphRunes(0))

	out := idx.TopK("alpha", 5)
	if out != nil {
		t.Fatalf("expected nil for no-overlap case, got %+v", out)
	}
}

func TestHelpers_OverlapSwap_And_TokenizeAlphaNum(t *testing.T) {
	// overlap swap branch: len(a) > len(b) triggers a,b swap
	a := map[string]struct{}{"a": {}, "b": {}, "c": {}}
	b := map[string]struct{}{"a": {}}
	if got := overlap(a, b); got != 1 {
		t.Fatalf("expected overlap 1 with swap branch, got %d", got)
	}

	// tokenize alphanumeric: \p{L}+\p{N}* should keep trailing digits
	toks := tokenize("foo bar abc123", nil)
	if _, ok := toks["abc123"]; !ok {
		t.Fatalf("expected alphanumeric token 'abc123' to be present: %#v", toks)
	}
}

func TestTopK_UnionNonPositive_ForcesContinue(t *testing.T) {
	// Build a normal index first.
	idx := NewIndexFromStrings([]string{"alpha"}, WithMinParagraphRunes(0))
	ii, ok := idx.(*index)
	if !ok || len(ii.docs) != 1 {
		t.Fatalf("setup failed: %#v", idx)
	}
	// Sanity: the doc should contain the token "alpha" so overlap == 1.
	if _, ok := ii.docs[0].tokens["alpha"]; !ok {
		t.Fatalf("expected token 'alpha' in doc tokens")
	}
	// Force union = qLen + tLen - over == 1 + 0 - 1 == 0 → triggers `union <= 0` continue.
	ii.docs[0].tLen = 0

	out := ii.TopK("alpha", 5)
	if out != nil {
		t.Fatalf("expected nil results due to union<=0 path, got %+v", out)
	}
}

func TestTokenize_WithEmptyNonNilStopmap(t *testing.T) {
	// stop != nil branch with no entries (behaves like nil)
	emptyStop := map[string]struct{}{}
	toks := tokenize("alpha", emptyStop)
	if _, ok := toks["alpha"]; !ok {
		t.Fatalf("expected 'alpha' token with empty stop map: %#v", toks)
	}
}
