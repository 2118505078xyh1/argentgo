package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// ---- glob ----

type globTool struct{}

func (globTool) Name() string        { return "glob" }
func (globTool) Description() string { return "Find files by glob pattern, such as '**/*.go'." }
func (globTool) Mutating() bool      { return false }
func (globTool) Schema() string {
	return `{"type":"object","properties":{"pattern":{"type":"string","description":"glob pattern; ** crosses directories"},"path":{"type":"string","description":"directory to search; defaults to the workspace root"}},"required":["pattern"]}`
}

func (globTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("glob: bad arguments: %w", err)
	}
	pattern := strings.TrimSpace(args.Pattern)
	if pattern == "" {
		return "", fmt.Errorf("glob: pattern must not be empty")
	}
	base, err := resolve(root, args.Path)
	if err != nil {
		return "", err
	}
	var hits []string
	walkFiles(base, func(file string) bool {
		rel, relErr := filepath.Rel(root, file)
		if relErr != nil {
			rel = file
		}
		rel = filepath.ToSlash(rel)
		if globMatch(pattern, rel) || globMatch(pattern, path.Base(rel)) {
			hits = append(hits, rel)
			if len(hits) >= maxMatches {
				return false
			}
		}
		return true
	})
	if len(hits) == 0 {
		return fmt.Sprintf("no files match %q", pattern), nil
	}
	sort.Strings(hits)
	return clip(ctx, strings.Join(hits, "\n")), nil
}

// globMatch 用模式匹配以斜杠分隔的路径。"**" 段
// 匹配零个或多个路径段；其他段使用 path.Match 语法。
func globMatch(pattern, name string) bool {
	pattern = strings.TrimPrefix(filepath.ToSlash(pattern), "./")
	name = strings.TrimPrefix(filepath.ToSlash(name), "./")
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegments(pattern, name []string) bool {
	if len(pattern) == 0 {
		return len(name) == 0
	}
	if pattern[0] == "**" {
		if matchSegments(pattern[1:], name) {
			return true
		}
		for i := range name {
			if matchSegments(pattern[1:], name[i+1:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	matched, err := path.Match(pattern[0], name[0])
	if err != nil || !matched {
		return false
	}
	return matchSegments(pattern[1:], name[1:])
}
