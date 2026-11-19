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

type readTool struct{}

func (readTool) Name() string        { return "read" }
func (readTool) Description() string { return "Read a UTF-8 text file, optionally a line range." }
func (readTool) Mutating() bool      { return false }
func (readTool) Schema() string {
	return `{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer","description":"1-based first line"},"limit":{"type":"integer","description":"number of lines"}},"required":["path"]}`
}

func (readTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
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
	start := args.Offset
	if start < 1 {
		start = 1
	}
	if start > len(lines) {
		return "", nil
	}
	end := len(lines)
	if args.Limit > 0 && start-1+args.Limit < end {
		end = start - 1 + args.Limit
	}
	return clip(ctx, strings.Join(lines[start-1:end], "\n")), nil
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
