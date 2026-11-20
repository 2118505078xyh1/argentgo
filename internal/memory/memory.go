// Package memory loads project instruction files for the system prompt.
//
// It looks for AGENTS.md and CLAUDE.md from the working directory upwards,
// stopping at the user's home directory (or the filesystem root when the
// working directory is outside it). Files are ordered nearest first: a nested
// project file refines the outer one, and the renderer says so explicitly.
//
// Each file is capped, because a real CLAUDE.md can be thousands of lines that
// would otherwise be re-sent on every turn. Truncation is reported so the model
// knows it can read the file for the rest. Files are read incrementally (at
// most one byte past the cap) and the combined text is bounded by totalBudget,
// so a huge or deep tree cannot inflate the system prompt or memory use.
package memory

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// FileNames are the instruction files read in each directory, in order.
var FileNames = []string{"AGENTS.md", "CLAUDE.md"}

// DefaultLimit is the number of bytes kept from each file.
const DefaultLimit = 8000

// totalBudget caps the combined size of the text kept from all files, so a
// deep tree cannot accumulate dozens of capped copies in the system prompt.
const totalBudget = 32 * 1024

// maxDepth stops a walk that never reaches the home directory.
const maxDepth = 32

// TruncationMarker is appended to a shortened document.
const TruncationMarker = "\n… [truncated; read the file for the rest]"

// Doc is one instruction file found on the way up.
type Doc struct {
	// Path is the absolute path of the file.
	Path string
	// Text is the file content, possibly truncated.
	Text string
	// Truncated reports whether Text was cut at the size limit.
	Truncated bool
}

// Load walks up from root, collecting AGENTS.md and CLAUDE.md, and stops at the
// user's home directory. Each file is capped at DefaultLimit bytes.
func Load(root string) ([]Doc, error) {
	home, _ := os.UserHomeDir()
	return LoadIn(root, home, DefaultLimit)
}

// LoadLimit is Load with a custom per-file cap. A limit <= 0 means no cap: in
// that mode neither the per-file cap nor the global total budget is applied.
func LoadLimit(root string, limit int) ([]Doc, error) {
	home, _ := os.UserHomeDir()
	return LoadIn(root, home, limit)
}

// LoadIn is Load with an explicit home directory, which is also the walk
// boundary. Tests use it to keep the walk inside a scratch tree.
func LoadIn(root, home string, limit int) ([]Doc, error) {
	dir, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("memory: %s is not a directory", dir)
	}
	stop := ""
	if home != "" {
		if abs, err := filepath.Abs(home); err == nil {
			stop = filepath.Clean(abs)
		}
	}

	var docs []Doc
	used := 0 // combined bytes of the text kept so far
	current := filepath.Clean(dir)
	samePath := func(a, b string) bool {
		if b == "" {
			return false
		}
		if filepath.Separator == '\\' {
			return strings.EqualFold(a, b)
		}
		return a == b
	}

walk:
	for depth := 0; depth < maxDepth; depth++ {
		for _, name := range FileNames {
			path := filepath.Join(current, name)
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				continue
			}
			body, fileTruncated, err := readInstructionFile(path, limit)
			if err != nil {
				continue
			}
			if body == "" {
				continue
			}

			budgetTruncated := false
			if limit > 0 {
				// A per-file clip appends TruncationMarker below. Reserve those
				// bytes now: otherwise a capped body that just fits remaining
				// can push the combined text past totalBudget once the marker
				// is added. Re-clipping to remaining-marker keeps the doc and
				// makes the budget exact.
				markerCost := 0
				if fileTruncated {
					markerCost = len(TruncationMarker)
				}
				remaining := totalBudget - used
				if remaining <= markerCost {
					break walk
				}
				if len(body)+markerCost > remaining {
					room := remaining - len(TruncationMarker)
					if room <= 0 {
						break walk
					}
					body = clipRunes(body, room)
					budgetTruncated = true
				}
			}

			doc := Doc{Path: path, Text: body, Truncated: fileTruncated || budgetTruncated}
			if doc.Truncated {
				doc.Text = strings.TrimRight(doc.Text, " \t\r\n") + TruncationMarker
			}
			docs = append(docs, doc)
			used += len(doc.Text)

			if budgetTruncated || used >= totalBudget {
				break walk
			}
		}

		if samePath(current, stop) {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return docs, nil
}

// readInstructionFile reads path and returns its trimmed text. When limit > 0
// it reads at most limit+1 bytes: the extra byte only tells us whether the file
// is longer, so the whole file is never loaded. The returned text is already
// cut on a rune boundary. With limit <= 0 the entire file is read and truncated
// is always false (the "no cap" contract of LoadLimit).
func readInstructionFile(path string, limit int) (text string, truncated bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()

	var raw []byte
	if limit > 0 {
		raw, err = io.ReadAll(io.LimitReader(f, int64(limit)+1))
		if err != nil {
			return "", false, err
		}
		if len(raw) > limit {
			truncated = true
			raw = []byte(clipRunes(string(raw), limit))
		}
	} else {
		raw, err = io.ReadAll(f)
		if err != nil {
			return "", false, err
		}
	}
	return strings.TrimSpace(strings.TrimPrefix(string(raw), "\ufeff")), truncated, nil
}

// clipRunes shortens s to at most limit bytes without splitting a UTF-8 rune.
// The caller guarantees len(s) > limit, so s[limit] exists and is used to
// detect a rune that was cut in half; bytes are dropped back to its start.
func clipRunes(s string, limit int) string {
	k := limit
	for k > 0 && !utf8.RuneStart(s[k]) {
		k--
	}
	return s[:k]
}

// Block renders docs as the block injected into the system prompt.
//
// The header states the ordering rule: entries run from the most specific
// (nearest) file to the least specific, so a conflict is resolved in favour of
// the first one. Empty input renders nothing.
func Block(docs []Doc) string {
	if len(docs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<project-memory>\n")
	b.WriteString("Instruction files, most specific first: when two files disagree, the earlier (nearer) one wins.\n")
	for _, doc := range docs {
		fmt.Fprintf(&b, "<file path=%q>\n%s\n</file>\n", doc.Path, doc.Text)
	}
	b.WriteString("</project-memory>\n")
	return b.String()
}
