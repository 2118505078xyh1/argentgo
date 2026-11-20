package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ---- read ----

// readWindowCap bounds each side of a line-centred window so a single call can
// never dump an unbounded amount of context.
const readWindowCap = 200

type readTool struct{}

func (readTool) Name() string { return "read" }
func (readTool) Description() string {
	return "Read a UTF-8 text file; prefer this over shell/cat for file contents. " +
		"Long files can be paged with offset (1-based first line) and limit; " +
		"a partial read reports the true total line count. " +
		"For focused context, set line (1-based) with before/after to read a window " +
		"around a target; before/after are capped at 200 each and line wins over offset/limit. " +
		"Set outline=true to read a Go file as a compact symbol list (kind, name, line) " +
		"instead of full source; re-call without outline (optionally with offset/limit) to expand a section."
}
func (readTool) Mutating() bool { return false }
func (readTool) Schema() string {
	return `{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer","description":"1-based first line"},"limit":{"type":"integer","description":"number of lines"},"line":{"type":"integer","description":"1-based target line; when > 0, wins over offset/limit"},"before":{"type":"integer","description":"lines above target (0-200, capped)"},"after":{"type":"integer","description":"lines below target (0-200, capped)"},"outline":{"type":"boolean","description":"for Go files, return a compact symbol list (kind, name, line) instead of full source; other files fall back to normal read"}},"required":["path"]}`
}

func (readTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Path    string `json:"path"`
		Offset  int    `json:"offset"`
		Limit   int    `json:"limit"`
		Line    int    `json:"line"`
		Before  int    `json:"before"`
		After   int    `json:"after"`
		Outline bool   `json:"outline"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("read: bad arguments: %w", err)
	}
	path, err := resolve(root, args.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("%s is not UTF-8 text", args.Path)
	}
	lines := strings.Split(string(data), "\n")

	// A trailing newline terminates the final line; strings.Split leaves an
	// empty sentinel after it that is not a line worth paging toward. Keep the
	// sentinel in the slice above so a full read stays byte-identical, but count
	// only real lines here.
	totalLines := len(lines)
	if len(data) > 0 && data[len(data)-1] == '\n' {
		totalLines--
	}

	if args.Outline {
		// Outline mode is a file-level "signature directory": for Go files we
		// reuse the repo-map extractor to list top-level declarations instead of
		// dumping the whole source. Line/offset selectors are intentionally
		// ignored here; callers expand a section with a normal (outline=false)
		// read. Non-Go files have no extractor yet, so fall back to a normal read
		// rather than returning an empty or misleading outline.
		if strings.HasSuffix(strings.ToLower(path), ".go") {
			return clip(ctx, outlineGoFile(args.Path, path)), nil
		}
	}

	start, end := 1, len(lines)
	if args.Line > 0 {
		// Gap-centred mode: line wins over offset/limit. before/after are
		// independent per-side budgets, clamped to [0, readWindowCap].
		before, after := args.Before, args.After
		if before < 0 {
			before = 0
		}
		if after < 0 {
			after = 0
		}
		if before > readWindowCap {
			before = readWindowCap
		}
		if after > readWindowCap {
			after = readWindowCap
		}
		start = args.Line - before
		if start < 1 {
			start = 1
		}
		end = args.Line + after
		if end > totalLines {
			end = totalLines
		}
		// A target past the last real line has no window to show; mirror the
		// offset path's out-of-range behaviour.
		if start > totalLines {
			return "", nil
		}
	} else {
		// Legacy offset/limit path: unchanged when line == 0.
		start = args.Offset
		if start < 1 {
			start = 1
		}
		if start > len(lines) {
			return "", nil
		}
		end = len(lines)
		if args.Limit > 0 && start-1+args.Limit < end {
			end = start - 1 + args.Limit
		}
	}
	out := clip(ctx, strings.Join(lines[start-1:end], "\n"))

	// end is the last displayed 1-based line; a smaller real total means the
	// caller has not reached the end of the file yet, so steer them to page.
	if end < totalLines {
		out += fmt.Sprintf("\n[showing lines %d–%d of %d; re-call with offset/limit to page]", start, end, totalLines)
	}
	return out, nil
}

// outlineGoFile renders a Go file as a compact "signature directory": one line
// per top-level declaration with its kind and 1-based line number. It reuses
// the repo-map symbol extractor so "what counts as a symbol" stays consistent
// between the read tool and the repo map. display is the caller-supplied path
// (used only in the header); disk is the resolved absolute path to parse.
func outlineGoFile(display, disk string) string {
	syms := extractRepoSymbols(filepath.Dir(disk), []string{filepath.Base(disk)})
	var b strings.Builder
	fmt.Fprintf(&b, "outline: %s (%d symbols)", display, len(syms))
	if len(syms) == 0 {
		b.WriteString("\n  (no top-level declarations)")
		return b.String()
	}
	for _, s := range syms {
		name := s.Name
		if s.Receiver != "" {
			name = s.Receiver + "." + name
		}
		fmt.Fprintf(&b, "\n  %-7s %s:%d", s.Kind, name, s.Line)
	}
	return b.String()
}

// Meta reports a supersede key derived from the file path alone. Line/offset
// selectors are dropped: a later read of any range of the same file makes an
// earlier read of that file stale for context purposes.
func (readTool) Meta(args json.RawMessage, _ string, err error) ToolMeta {
	if err != nil {
		return ToolMeta{}
	}
	var a struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(args, &a) != nil {
		return ToolMeta{}
	}
	p := normalizeReadPath(a.Path)
	if p == "" {
		return ToolMeta{}
	}
	return ToolMeta{SupersedeKey: "read|" + p}
}

// readSelector matches the common line-range suffixes appended to a path:
// ":10", ":10:20", "#L10", "#L10-L20" and their comma variants.
var readSelector = regexp.MustCompile(`(?::[0-9]+(?:[:,-][0-9]+)*|#[Ll]?[0-9]+(?:[-:][Ll]?[0-9]+)?)+$`)

// normalizeReadPath cleans a read path and removes line selectors so two reads
// of the same file share one supersede key regardless of their ranges.
func normalizeReadPath(raw string) string {
	p := strings.TrimSpace(raw)
	if p == "" {
		return ""
	}
	p = filepath.ToSlash(p)
	p = readSelector.ReplaceAllString(p, "")
	p = strings.TrimSpace(p)
	if p == "" || p == "." {
		return ""
	}
	cleaned := path.Clean(p)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return ""
	}
	return cleaned
}
