package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// ---- ls ----

type lsTool struct{}

func (lsTool) Name() string        { return "ls" }
func (lsTool) Description() string { return "List a directory. Directories end with /." }
func (lsTool) Mutating() bool      { return false }
func (lsTool) Schema() string {
	return `{"type":"object","properties":{"path":{"type":"string"}}}`
}

func (lsTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("ls: bad arguments: %w", err)
	}
	path, err := resolve(root, args.Path)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return clip(ctx, strings.Join(names, "\n")), nil
}

// ---- grep ----

type grepTool struct{}

func (grepTool) Name() string        { return "grep" }
func (grepTool) Description() string { return "Search file contents with a regular expression." }
func (grepTool) Mutating() bool      { return false }
func (grepTool) Schema() string {
	return `{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"},"before":{"type":"integer"},"after":{"type":"integer"},"context":{"type":"integer"},"max_columns":{"type":"integer"}},"required":["pattern"]}`
}

type grepToolArgs struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Before     int    `json:"before"`
	After      int    `json:"after"`
	Context    int    `json:"context"`
	MaxColumns int    `json:"max_columns"`
}

func (grepTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args grepToolArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("grep: bad arguments: %w", err)
	}
	base, err := resolve(root, args.Path)
	if err != nil {
		return "", err
	}

	matches, _, outcome, err := GrepExWithOutcome(GrepExOptions{
		Pattern:       args.Pattern,
		Path:          base,
		RelRoot:       root, // 与原 grep 一致：路径相对工作区根
		Before:        args.Before,
		After:         args.After,
		Context:       args.Context,
		CaseSensitive: true, // 原 grep 大小写敏感
		HeadLimit:     maxMatches,
		MaxPerFile:    grepPerFileLimit(),
		Ctx:           ctx,
		TrimText:      true, // 原 grep 输出 strings.TrimSpace
		MaxColumns:    args.MaxColumns,
	})
	if err != nil {
		return "", err
	}

	if len(matches) == 0 {
		return "(no matches)", nil
	}
	out := formatGrepExOutput(matches, nil, grepExModeContent)
	out = appendSearchNote(out, outcome, ctx)
	return clip(ctx, out), nil
}

// Meta 把空的 grep 结果标记为无用。
func (grepTool) Meta(_ json.RawMessage, output string, err error) ToolMeta {
	if err != nil {
		return ToolMeta{}
	}
	return ToolMeta{Useless: strings.TrimSpace(output) == "(no matches)"}
}

// ---- find ----

type findTool struct{}

func (findTool) Name() string        { return "find" }
func (findTool) Description() string { return "Find files whose name matches a glob pattern." }
func (findTool) Mutating() bool      { return false }
func (findTool) Schema() string {
	return `{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"}},"required":["pattern"]}`
}

func (findTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("find: bad arguments: %w", err)
	}
	base, err := resolve(root, args.Path)
	if err != nil {
		return "", err
	}
	probe := probeLimit(maxMatches)
	var mu sync.Mutex
	var stopped atomic.Bool
	var found []string

	walkFiles(ctx, base, func(path string) bool {
		if stopped.Load() {
			return false
		}
		matched, matchErr := filepath.Match(args.Pattern, filepath.Base(path))
		if matchErr == nil && matched {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			mu.Lock()
			if !stopped.Load() {
				found = append(found, filepath.ToSlash(rel))
				if len(found) >= probe {
					stopped.Store(true)
				}
			}
			mu.Unlock()
		}
		return !stopped.Load()
	})
	if len(found) == 0 {
		return "(no matches)", nil
	}
	outcome := searchOutcome{Limit: maxMatches}.finalize(len(found), maxMatches)
	if ctx.Err() != nil {
		outcome.TimedOut = true
	}
	if len(found) > maxMatches {
		found = found[:maxMatches]
	}
	sort.Strings(found)
	out := appendSearchNote(strings.Join(found, "\n"), outcome, ctx)
	return clip(ctx, out), nil
}

// Meta 把空的 find 结果标记为无用。
func (findTool) Meta(_ json.RawMessage, output string, err error) ToolMeta {
	if err != nil {
		return ToolMeta{}
	}
	return ToolMeta{Useless: strings.TrimSpace(output) == "(no matches)"}
}

// Meta 把空的 glob 结果标记为无用。globTool 位于 glob.go 中，但该
// 方法与其他 meta 生产者声明在一起：同一个包、同样的
// 语义：提示来自该调用的实际输出。
func (globTool) Meta(_ json.RawMessage, output string, err error) ToolMeta {
	if err != nil {
		return ToolMeta{}
	}
	return ToolMeta{Useless: strings.HasPrefix(strings.TrimSpace(output), "no files match ")}
}
