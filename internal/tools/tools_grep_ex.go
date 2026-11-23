package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// 本文件把深度检索原语 GrepEx 装配为只读工具 grep_ex。
//
// 与 grep 工具一样，它刻意不进入 builtin_register.go 的静态注册表，
// 也不被 New/NewWithLimit 调用，因此默认 Specs()/Names() 与 golden
// 快照逐字节不变。只有显式调用 RegisterGrepEx 才会把它加入某个 Registry。

// grepExTool 是 GrepEx 的只读工具封装：路径先经 resolve 收进工作区，
// 再把结果按 output_mode 格式化为 ripgrep 风格的稳定文本。
type grepExTool struct{}

var _ Tool = (*grepExTool)(nil)

func (grepExTool) Name() string { return "grep_ex" }

func (grepExTool) Description() string {
	return "Deep content search with optional context lines, include glob and multiple output modes. Read-only; paths are confined to the workspace."
}

func (grepExTool) Mutating() bool { return false }

func (grepExTool) Schema() string {
	return `{"type":"object","properties":{"pattern":{"type":"string","description":"Regular expression to search for in file contents."},"path":{"type":"string","description":"File or directory to search; defaults to the workspace root."},"include":{"type":"string","description":"Glob filter applied to relative paths and file names, e.g. *.go."},"context":{"type":"integer","description":"Number of context lines to show before and after each match."},"before":{"type":"integer","description":"Context lines before each match; overrides context when positive."},"after":{"type":"integer","description":"Context lines after each match; overrides context when positive."},"case_sensitive":{"type":"boolean","description":"Whether the search is case-sensitive; defaults to false."},"output_mode":{"type":"string","enum":["content","files_with_matches","count"],"description":"Output format: content (default), files_with_matches, or count."}},"required":["pattern"]}`
}

// grepExToolArgs 是 Run 接受的 JSON 参数。字段语义与 GrepExOptions 一致；
// case_sensitive 直接映射（省略时为 false，即大小写不敏感）。
type grepExToolArgs struct {
	Pattern       string `json:"pattern"`
	Path          string `json:"path"`
	Include       string `json:"include"`
	Context       int    `json:"context"`
	Before        int    `json:"before"`
	After         int    `json:"after"`
	CaseSensitive bool   `json:"case_sensitive"`
	OutputMode    string `json:"output_mode"`
}

func (t grepExTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args grepExToolArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("grep_ex: bad arguments: %w", err)
	}

	// path 省略时默认工作区根目录；无论显式还是默认都经 resolve 收边界。
	base := args.Path
	if strings.TrimSpace(base) == "" {
		base = root
	}
	resolved, err := resolve(root, base)
	if err != nil {
		return "", err
	}

	matches, files, outcome, err := GrepExWithOutcome(GrepExOptions{
		Pattern:       args.Pattern,
		Path:          resolved,
		RelRoot:       resolved,
		Ctx:           ctx,
		Context:       args.Context,
		Before:        args.Before,
		After:         args.After,
		OutputMode:    args.OutputMode,
		Include:       args.Include,
		CaseSensitive: args.CaseSensitive,
		MaxPerFile:    grepPerFileLimit(),
	})
	if err != nil {
		return "", err
	}

	text := formatGrepExOutput(matches, files, args.OutputMode)
	text = appendSearchNote(text, outcome, ctx)
	if out, handled := spillToolOutput(ctx, text); handled {
		return out, nil
	}
	return clip(ctx, text), nil
}

// Meta 把空的 grep_ex 结果标记为无用（与 grepTool.Meta 同语义）。
func (grepExTool) Meta(_ json.RawMessage, output string, err error) ToolMeta {
	if err != nil {
		return ToolMeta{}
	}
	return ToolMeta{Useless: strings.TrimSpace(output) == "(no matches)"}
}

// formatGrepExOutput 把 GrepEx 的结果格式化为 ripgrep 风格的稳定文本：
//   - content：命中行用 path:line: text，上下文行用 path-line- text；
//   - files_with_matches：排序后的路径，每行一个；
//   - count：排序后的 path:count；
//   - 空结果统一返回 "(no matches)"。
//
// 未知 output_mode 按 content 处理；遍历 map 前先排序，保证输出稳定。
func formatGrepExOutput(matches []GrepExMatch, files map[string]int, mode string) string {
	if len(matches) == 0 {
		return "(no matches)"
	}

	var b strings.Builder
	switch mode {
	case grepExModeFilesWithMatches:
		for _, p := range sortedGrepExPaths(files) {
			b.WriteString(p)
			b.WriteByte('\n')
		}

	case grepExModeCount:
		for _, p := range sortedGrepExPaths(files) {
			fmt.Fprintf(&b, "%s:%d\n", p, files[p])
		}

	default: // content
		for _, m := range matches {
			// Before = lines[start:i]，其真实行号为 m.Line-len(Before) ... m.Line-1。
			for j, before := range m.Before {
				fmt.Fprintf(&b, "%s-%d- %s\n", m.Path, m.Line-len(m.Before)+j, before)
			}
			fmt.Fprintf(&b, "%s:%d: %s\n", m.Path, m.Line, m.Text)
			// After = lines[i+1:end+1]，其真实行号为 m.Line+1 ... m.Line+len(After)。
			for j, after := range m.After {
				fmt.Fprintf(&b, "%s-%d- %s\n", m.Path, m.Line+1+j, after)
			}
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func sortedGrepExPaths(files map[string]int) []string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// RegisterGrepEx 把 grep_ex 显式加入 r（opt-in）。它是幂等的：
// 重复调用或已存在同名工具时不再插入；已有工具顺序不变，新工具追加在末尾。
// 传入 nil 是空操作。本函数绝不被 New/NewWithLimit 调用，因此默认不
// 注册时 Specs() 与 golden 快照完全一致。
func RegisterGrepEx(r *Registry) {
	if r == nil {
		return
	}
	const name = "grep_ex"
	if _, exists := r.tools[name]; exists {
		return
	}
	r.tools[name] = grepExTool{}
	r.order = append(r.order, name)
}
