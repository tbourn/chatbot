package search

import (
	"bufio"
	"os"
	"strings"
)

// PrepareMarkdownInMemory reads the markdown at `path`, flattens any table rows
// into standalone facts, and returns the processed bytes. If no transform was
// needed, it returns the original file bytes.
//
// Notes:
//   - Avoids emitting a leading blank line.
//   - Normalizes the tail to end with exactly one newline.
func PrepareMarkdownInMemory(path string) ([]byte, error) {
	orig, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var b strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	wroteAny := false
	wroteBlank := true // start true to avoid a leading blank
	sawTable := false

	writeFact := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || strings.EqualFold(s, "text") {
			return
		}
		b.WriteString(s)
		b.WriteByte('\n')
		b.WriteByte('\n')
		wroteAny = true
		wroteBlank = true
	}

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			if !wroteBlank {
				b.WriteByte('\n')
				wroteBlank = true
			}
			continue
		}

		// table row: "| ... |"
		if strings.HasPrefix(line, "|") && strings.HasSuffix(line, "|") {
			sawTable = true
			raw := strings.Trim(line, "|")
			cols := strings.Split(raw, "|")

			allSep := true
			cleaned := make([]string, 0, len(cols))
			for _, c := range cols {
				cell := strings.TrimSpace(c)
				if cell != "" {
					cleaned = append(cleaned, cell)
				}
				tmp := strings.ReplaceAll(cell, ":", "")
				tmp = strings.ReplaceAll(tmp, "-", "")
				if strings.TrimSpace(tmp) != "" {
					allSep = false
				}
			}
			if allSep || len(cleaned) == 0 {
				continue
			}
			if len(cleaned) == 1 {
				writeFact(cleaned[0])
				continue
			}
			writeFact(strings.Join(cleaned, " "))
			continue
		}

		// non-table line → one fact per line
		wroteBlank = false
		writeFact(line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	// No transform → original bytes
	if !sawTable && !wroteAny {
		return orig, nil
	}

	out := b.String()
	if sawTable {
		// Table flows expect a single trailing newline
		out = strings.TrimRight(out, "\n") + "\n"
	}
	// For non-table flows, keep the natural "\n\n" tail
	return []byte(out), nil
}
