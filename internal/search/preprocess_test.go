package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePreprocessTemp(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return p
}

func TestPrepareMarkdownInMemory_ReadFileError(t *testing.T) {
	// Non-existent path -> os.ReadFile error branch
	_, err := PrepareMarkdownInMemory(filepath.Join(t.TempDir(), "nope.md"))
	if err == nil {
		t.Fatalf("expected error for missing file")
	}
}

func TestPrepareMarkdownInMemory_NoTransformReturnsOriginal(t *testing.T) {
	// Only blanks and a single "text" line ⇒ wroteAny=false & sawTable=false
	// → returns original bytes (not the builder output).
	dir := t.TempDir()
	orig := "\n   \n  text  \n\n"
	p := writePreprocessTemp(t, dir, "a.md", orig)

	got, err := PrepareMarkdownInMemory(p)
	if err != nil {
		t.Fatalf("PrepareMarkdownInMemory error: %v", err)
	}
	if string(got) != orig {
		t.Fatalf("expected original bytes, got %q", string(got))
	}
}

func TestPrepareMarkdownInMemory_NonTableLinesFlattened(t *testing.T) {
	dir := t.TempDir()
	in := "  alpha  \n\n   beta   \n"
	// Non-table lines become one fact per line, each followed by a blank line.
	want := "alpha\n\nbeta\n\n"
	p := writePreprocessTemp(t, dir, "b.md", in)

	got, err := PrepareMarkdownInMemory(p)
	if err != nil {
		t.Fatalf("PrepareMarkdownInMemory error: %v", err)
	}
	if string(got) != want {
		t.Fatalf("flatten mismatch:\nwant:\n%q\ngot:\n%q", want, string(got))
	}
}

func TestPrepareMarkdownInMemory_TableProcessing(t *testing.T) {
	dir := t.TempDir()
	in := `
| text | value |
| --- | --- |
| Gen Z | Nashville |
| text |
| onecell |
| a |  | b |
not a table line
`
	// Expectations:
	// - header "text|value" kept as "text value"
	// - separator row skipped
	// - "Gen Z | Nashville" -> "Gen Z Nashville"
	// - single cell "text" row dropped (writeFact skips "text")
	// - single cell "onecell" kept
	// - "a |  | b" -> "a b"
	// - non-table line preserved
	want := strings.Join([]string{
		"text value",
		"",
		"Gen Z Nashville",
		"",
		"onecell",
		"",
		"a b",
		"",
		"not a table line",
		"",
	}, "\n")

	p := writePreprocessTemp(t, dir, "c.md", in)

	got, err := PrepareMarkdownInMemory(p)
	if err != nil {
		t.Fatalf("PrepareMarkdownInMemory error: %v", err)
	}
	if string(got) != want {
		t.Fatalf("table processing mismatch:\nwant:\n%q\ngot:\n%q", want, string(got))
	}
}

func TestPrepareMarkdownInMemory_ScannerErrTooLong(t *testing.T) {
	// Scanner max token size was set to 4 MiB. Create a single line > 4 MiB to force sc.Err()!=nil.
	dir := t.TempDir()
	huge := strings.Repeat("a", 4*1024*1024+10) // > 4MiB single line
	p := writePreprocessTemp(t, dir, "huge.md", huge)

	_, err := PrepareMarkdownInMemory(p)
	if err == nil {
		t.Fatalf("expected scanner error for overly long line")
	}
}
