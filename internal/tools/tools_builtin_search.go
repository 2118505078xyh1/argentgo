package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
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
	return `{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"}},"required":["pattern"]}`
}

func (grepTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("grep: bad arguments: %w", err)
	}
	re, err := regexp.Compile(args.Pattern)
	if err != nil {
		return "", fmt.Errorf("grep: bad pattern: %w", err)
	}
	base, err := resolve(root, args.Path)
	if err != nil {
		return "", err
	}
	var matches []string
	perFile := grepPerFileLimit()
	walkFiles(base, func(path string) bool {
		data, err := os.ReadFile(path)
		if err != nil || strings.ContainsRune(string(data), 0) {
			return true
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		fileCount := 0
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				matches = append(matches, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), i+1, strings.TrimSpace(line)))
				fileCount++
				if len(matches) >= maxMatches {
					return false
				}
				if perFile > 0 && fileCount >= perFile {
					break
				}
			}
		}
		return true
	})
	if len(matches) == 0 {
		return "(no matches)", nil
	}
	return clip(ctx, strings.Join(matches, "\n")), nil
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
	var found []string
	walkFiles(base, func(path string) bool {
		matched, matchErr := filepath.Match(args.Pattern, filepath.Base(path))
		if matchErr == nil && matched {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			found = append(found, filepath.ToSlash(rel))
			if len(found) >= maxMatches {
				return false
			}
		}
		return true
	})
	if len(found) == 0 {
		return "(no matches)", nil
	}
	sort.Strings(found)
	return clip(ctx, strings.Join(found, "\n")), nil
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

// walkFiles 遍历 base 下的普通文件，跳过嘈杂的目录。
// 回调返回 false 时停止遍历。
func walkFiles(base string, visit func(string) bool) {
	skip := map[string]bool{".git": true, "node_modules": true, "target": true, "dist": true}
	_ = filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if path != base && skip[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		if !visit(path) {
			return filepath.SkipAll
		}
		return nil
	})
}
