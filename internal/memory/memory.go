// Package memory loads project instruction files for the system prompt.
//
// It looks for AGENTS.md and CLAUDE.md from the working directory upwards,
// stopping at the user's home directory (or the filesystem root when the
// working directory is outside it). Files are ordered nearest first: a nested
// project file refines the outer one, and the renderer says so explicitly.
//
// Each file is capped, because a real CLAUDE.md can be thousands of lines that
// would otherwise be re-sent on every turn. Truncation is reported so the model
// knows it can read the file for the rest.
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileNames are the instruction files read in each directory, in order.
var FileNames = []string{"AGENTS.md", "CLAUDE.md"}

// DefaultLimit is the number of bytes kept from each file.
const DefaultLimit = 8000

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

// LoadLimit is Load with a custom per-file cap. A limit <= 0 means no cap.
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

	for depth := 0; depth < maxDepth; depth++ {
		for _, name := range FileNames {
			path := filepath.Join(current, name)
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				continue
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			text := strings.TrimSpace(strings.TrimPrefix(string(raw), "\ufeff"))
			if text == "" {
				continue
			}
			doc := Doc{Path: path, Text: text}
			if limit > 0 && len(doc.Text) > limit {
				doc.Text = strings.TrimRight(doc.Text[:limit], " \t\r\n") + TruncationMarker
				doc.Truncated = true
			}
			docs = append(docs, doc)
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
