package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// ---- context_pack ----
//
// context_pack 是 read 的以间隔为中心窗口的多文件对应物：它在
// 字节预算内围绕编辑锚点打包真实文件内容，使
// 模型可以跳过"读取整个文件"的往返。它只读、有界，且
// 从不拆分 UTF-8 rune。

const (
	contextPackDefaultBudget = 8000
	contextPackMaxBudget     = 32000
	// 较小的预算甚至容不下截断标记，因此过小的
	// 值会被向上钳制，而不是发出超出预算的标记。
	contextPackMinBudget = 64
	// 每侧行窗口上限，与 readTool 的 readWindowCap 一致。
	contextPackWindowCap = 200
	// 自动选择的邻居文件有上限，使单次调用不会遍历巨大的树。
	contextPackMaxFiles = 24
)

type contextPackTool struct{}

func (contextPackTool) Name() string { return "context_pack" }

func (contextPackTool) Description() string {
	return "Pack real file contents around an edit anchor under a byte budget; " +
		"the target file is always emitted last (nearest-last). " +
		"files overrides the automatic selection of sibling files plus repo entry points. " +
		"For the target, set line with before/after (each capped at 200) to pack only a " +
		"gap-centred window instead of the whole file. " +
		"budget_bytes defaults to 8000 and is capped at 32000; packing stops at a whole " +
		"block boundary and reports [truncated at N bytes] when the budget is hit."
}

func (contextPackTool) Mutating() bool { return false }

func (contextPackTool) Schema() string {
	return `{"type":"object","properties":{"target":{"type":"string","description":"workspace-relative edit anchor file; always packed last"},"files":{"type":"array","items":{"type":"string"},"description":"explicit workspace-relative files to pack; overrides automatic selection"},"budget_bytes":{"type":"integer","description":"total output byte budget (default 8000, cap 32000)"},"line":{"type":"integer","description":"1-based anchor line; when > 0 only the gap window of the target is emitted"},"before":{"type":"integer","description":"lines above the anchor (0-200, capped)"},"after":{"type":"integer","description":"lines below the anchor (0-200, capped)"}},"required":["target"]}`
}

// registerContextPack 在 memory_* 尾部之前安装该工具，使
// 该尾部仍与末尾的 tool_search 相邻。它导出是为了让拥有 Registry
// 但无法编辑 tools_registry.go 的调用方也能安装该工具；
// 默认的 New/NewWithLimit 路径已经调用它。重复调用为空操作。
func registerContextPack(r *Registry) {
	if r == nil {
		return
	}
	if _, exists := r.tools[contextPackTool{}.Name()]; exists {
		return
	}
	tool := contextPackTool{}
	r.tools[tool.Name()] = tool
	idx := len(r.order)
	for i, name := range r.order {
		if strings.HasPrefix(name, "memory_") || name == "tool_search" {
			idx = i
			break
		}
	}
	r.order = append(r.order, "")
	copy(r.order[idx+1:], r.order[idx:])
	r.order[idx] = tool.Name()
}

type contextPackArgs struct {
	Target      string   `json:"target"`
	Files       []string `json:"files"`
	BudgetBytes int      `json:"budget_bytes"`
	Line        int      `json:"line"`
	Before      int      `json:"before"`
	After       int      `json:"after"`
}

func (contextPackTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args contextPackArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("context_pack: bad arguments: %w", err)
	}
	if strings.TrimSpace(args.Target) == "" {
		return "", fmt.Errorf("context_pack: target is required")
	}

	targetAbs, err := resolve(root, args.Target)
	if err != nil {
		return "", err
	}
	targetRel, err := contextPackRel(root, targetAbs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(targetAbs)
	if err != nil {
		return "", fmt.Errorf("context_pack: target %q: %w", args.Target, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("context_pack: target %q is not a regular file", args.Target)
	}

	targetContent, err := contextPackRead(targetAbs)
	if err != nil {
		return "", fmt.Errorf("context_pack: target %q: %w", args.Target, err)
	}
	if args.Line > 0 {
		targetContent = contextPackWindow(targetContent, args.Line, args.Before, args.After)
	}

	others, err := contextPackOthers(root, args.Files, targetRel)
	if err != nil {
		return "", err
	}
	blocks := make([]string, 0, len(others)+1)
	for _, rel := range others {
		abs, err := resolve(root, rel)
		if err != nil {
			return "", err
		}
		content, err := contextPackRead(abs)
		if err != nil {
			// 缺失或不可读的邻居文件不能毁掉整个打包；
			// 显式路径越界已在上面的解析中被拒绝。
			continue
		}
		blocks = append(blocks, fmt.Sprintf("## %s ##\n%s", rel, content))
	}
	// 最近优先：锚点是模型读取的最后一个块。
	blocks = append(blocks, fmt.Sprintf("## %s ##\n%s", targetRel, targetContent))

	out, _ := contextPackAssemble(blocks, contextPackBudget(args.BudgetBytes))
	return out, nil
}

// contextPackOthers 选择非目标文件。显式文件优先；否则
// 取目标的同级文件加上 repo 入口点。结果会去重
// 并排序，使同目录（最近）的文件排在最后，紧邻目标。
//
// nil/空的区别很重要：省略 `files` 字段表示
// "自动选择"，而显式提供的空数组表示"没有其他
// 文件"（json.Unmarshal 对 `[]` 产生非 nil 的空切片）。这里用
// len() 会悄悄重新启用自动选择并泄漏同级文件内容。
func contextPackOthers(root string, explicit []string, targetRel string) ([]string, error) {
	var rels []string
	if explicit != nil {
		for _, raw := range explicit {
			abs, err := resolve(root, raw)
			if err != nil {
				return nil, err
			}
			rel, err := contextPackRel(root, abs)
			if err != nil {
				return nil, err
			}
			rels = append(rels, rel)
		}
	} else {
		var err error
		rels, err = contextPackAuto(root, targetRel)
		if err != nil {
			return nil, err
		}
	}

	seen := make(map[string]bool, len(rels))
	uniq := make([]string, 0, len(rels))
	for _, rel := range rels {
		if rel == "" || rel == targetRel || seen[rel] {
			continue
		}
		seen[rel] = true
		uniq = append(uniq, rel)
	}
	targetDir := filepath.ToSlash(filepath.Dir(targetRel))
	sort.Slice(uniq, func(i, j int) bool {
		ri, rj := contextPackRank(uniq[i], targetDir), contextPackRank(uniq[j], targetDir)
		if ri != rj {
			return ri < rj
		}
		return uniq[i] < uniq[j]
	})
	if len(uniq) > contextPackMaxFiles {
		// 最近的文件排在最后，因此保留尾部。
		uniq = uniq[len(uniq)-contextPackMaxFiles:]
	}
	return uniq, nil
}

func contextPackRank(rel, targetDir string) int {
	if filepath.ToSlash(filepath.Dir(rel)) == targetDir {
		return 1
	}
	return 0
}

// contextPackAuto 选择目标旁边加上工作区根目录的
// 入口点的文件。只考虑直接子项，跳过符号链接，
// 并由 repo_map 忽略列表把构建/CI 噪声挡在包外。
func contextPackAuto(root, targetRel string) ([]string, error) {
	rootAbs, err := resolve(root, ".")
	if err != nil {
		return nil, err
	}
	targetAbs, err := resolve(root, targetRel)
	if err != nil {
		return nil, err
	}
	var rels []string
	seen := make(map[string]bool)
	addDir := func(dir string, entryPointsOnly bool) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if entry.IsDir() || !entry.Type().IsRegular() || repoMapIgnoredDirs[entry.Name()] {
				continue
			}
			if entryPointsOnly && !repoMapEntryPoints[strings.ToLower(entry.Name())] {
				continue
			}
			abs := filepath.Join(dir, entry.Name())
			rel, err := contextPackRel(rootAbs, abs)
			if err != nil || rel == targetRel || seen[rel] {
				continue
			}
			seen[rel] = true
			rels = append(rels, rel)
		}
	}
	addDir(filepath.Dir(targetAbs), false)
	addDir(rootAbs, true)
	return rels, nil
}

// contextPackWindow 沿用 readTool 的以间隔为中心窗口：before/after 是
// 钳制在 [0, 200] 的独立每侧预算，且尾随换行
// 哨兵不算作真实行。
func contextPackWindow(content string, line, before, after int) string {
	if content == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	total := len(lines)
	if strings.HasSuffix(content, "\n") {
		total--
	}
	clamp := func(v int) int {
		if v < 0 {
			return 0
		}
		if v > contextPackWindowCap {
			return contextPackWindowCap
		}
		return v
	}
	start := line - clamp(before)
	if start < 1 {
		start = 1
	}
	end := line + clamp(after)
	if end > total {
		end = total
	}
	if start > total || end < start {
		return ""
	}
	return strings.Join(lines[start-1:end], "\n")
}

func contextPackBudget(v int) int {
	if v <= 0 {
		return contextPackDefaultBudget
	}
	if v > contextPackMaxBudget {
		return contextPackMaxBudget
	}
	if v < contextPackMinBudget {
		return contextPackMinBudget
	}
	return v
}

// contextPackAssemble 连接完整的块，直到下一个块会超出
// 字节预算。因此截断发生在块边界：不会把任何块或
// 任何 UTF-8 rune 切掉一半。标记自身的字节会被预留，所以
// 返回的字符串永不超过预算。
func contextPackAssemble(blocks []string, budget int) (string, bool) {
	included := make([]string, 0, len(blocks))
	used := 0
	truncated := false
	for _, block := range blocks {
		seg := block
		if len(included) > 0 {
			seg = "\n" + block
		}
		if used+len(seg) > budget {
			truncated = true
			break
		}
		included = append(included, block)
		used += len(seg)
	}
	if !truncated {
		return strings.Join(included, "\n"), false
	}
	marker := func(n int) string { return fmt.Sprintf("\n[truncated at %d bytes]", n) }
	m := marker(used)
	for used+len(m) > budget && len(included) > 0 {
		last := included[len(included)-1]
		included = included[:len(included)-1]
		used -= len(last)
		if len(included) > 0 {
			used-- // the separator before the removed block
		}
		m = marker(used)
	}
	return strings.Join(included, "\n") + m, true
}

func contextPackRel(root, abs string) (string, error) {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	if rel == "" || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("path %q is outside the workspace", abs)
	}
	return rel, nil
}

func contextPackRead(path string) (string, error) {
	// 输出本就受 clip 预算约束，读入超过小文件预算的巨型文本纯属浪费且
	// 会有「文件 + 字符串副本 + 行切片」三份驻留的 OOM 风险。
	data, err := readFileLimited(path, smallFileMaxBytes)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("%s is not UTF-8 text", path)
	}
	return string(data), nil
}
