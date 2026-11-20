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
		"around a target; before/after are capped at 200 each and line wins over offset/limit."
}
func (readTool) Mutating() bool { return false }
func (readTool) Schema() string {
	return `{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer","description":"1-based first line"},"limit":{"type":"integer","description":"number of lines"},"line":{"type":"integer","description":"1-based target line; when > 0, wins over offset/limit"},"before":{"type":"integer","description":"lines above target (0-200, capped)"},"after":{"type":"integer","description":"lines below target (0-200, capped)"}},"required":["path"]}`
}

func (readTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
		Line   int    `json:"line"`
		Before int    `json:"before"`
		After  int    `json:"after"`
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
