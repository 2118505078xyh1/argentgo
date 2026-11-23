package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ---- stdlib-portable productivity tools ----
//
// diff 用有界的前缀/后缀块比较两个工作区文件。usage
// 和 history 通过此处声明的消费侧接口报告运行/会话数据，
// 使 internal/tools 保持独立于 internal/agent（runState）和
// internal/session。app 用 SetUsageSource 和
// SetHistorySource 接线真实数据源；在此之前这些工具报告数据不可用。

// ---- diff ----

type diffTool struct{}

var _ Tool = (*diffTool)(nil)

func (diffTool) Name() string { return "diff" }

func (diffTool) Description() string {
	return "Compare two workspace files line by line: added/removed/changed counts plus a bounded unified-style hunk. Read-only."
}

func (diffTool) Mutating() bool { return false }

func (diffTool) Schema() string {
	return `{"type":"object","properties":{"path_a":{"type":"string","description":"first workspace file (relative path)"},"path_b":{"type":"string","description":"second workspace file (relative path)"}},"required":["path_a","path_b"]}`
}

func (diffTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		PathA string `json:"path_a"`
		PathB string `json:"path_b"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("diff: bad arguments: %w", err)
	}
	if strings.TrimSpace(args.PathA) == "" || strings.TrimSpace(args.PathB) == "" {
		return "", fmt.Errorf("diff: both path_a and path_b are required")
	}
	absA, err := resolve(root, args.PathA)
	if err != nil {
		return "", fmt.Errorf("diff: path_a: %w", err)
	}
	absB, err := resolve(root, args.PathB)
	if err != nil {
		return "", fmt.Errorf("diff: path_b: %w", err)
	}
	before, err := readFileLimited(absA, smallFileMaxBytes)
	if err != nil {
		return "", fmt.Errorf("diff: read path_a: %w", err)
	}
	after, err := readFileLimited(absB, smallFileMaxBytes)
	if err != nil {
		return "", fmt.Errorf("diff: read path_b: %w", err)
	}
	return clip(ctx, renderFileDiff(args.PathA, args.PathB, before, after)), nil
}

const (
	diffContextLines = 3  // identical lines shown on either side of a hunk
	diffHunkLines    = 20 // maximum paired changed lines shown before collapsing
)

// renderFileDiff 修剪公共前缀和后缀，然后把变化的
// 中间部分渲染为紧凑的统一风格块。计数从同一个
// 中间区域推导，因此数字始终与显示的行一致。CRLF 由
// splitDiffLines 规范化，因此仅行尾变化不构成差异。
func renderFileDiff(pathA, pathB string, before, after []byte) string {
	a := splitDiffLines(before)
	b := splitDiffLines(after)

	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix &&
		a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	midA := a[prefix : len(a)-suffix]
	midB := b[prefix : len(b)-suffix]

	changed := min(len(midA), len(midB))
	removed := len(midA) - changed
	added := len(midB) - changed

	var sb strings.Builder
	fmt.Fprintf(&sb, "diff %s -> %s\n", pathA, pathB)
	fmt.Fprintf(&sb, "+%d added  -%d removed  ~%d changed\n", added, removed, changed)
	if added == 0 && removed == 0 && changed == 0 {
		fmt.Fprintf(&sb, "no differences (%d lines identical)\n", len(a))
		return sb.String()
	}
	fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", prefix+1, len(midA), prefix+1, len(midB))

	ctxStart := prefix - diffContextLines
	if ctxStart < 0 {
		ctxStart = 0
	}
	for _, line := range a[ctxStart:prefix] {
		sb.WriteString(" " + line + "\n")
	}

	span := max(len(midA), len(midB))
	shown := min(span, diffHunkLines)
	for i := 0; i < shown; i++ {
		if i < len(midA) {
			sb.WriteString("-" + midA[i] + "\n")
		}
		if i < len(midB) {
			sb.WriteString("+" + midB[i] + "\n")
		}
	}
	if shown < span {
		fmt.Fprintf(&sb, "... %d more changed lines ...\n", span-shown)
	}

	ctxAfterStart := len(a) - suffix
	ctxAfterEnd := min(ctxAfterStart+diffContextLines, len(a))
	for _, line := range a[ctxAfterStart:ctxAfterEnd] {
		sb.WriteString(" " + line + "\n")
	}
	return sb.String()
}

// ---- usage ----

const usageUnavailable = "usage data unavailable"

// UsageSource 以 JSON 字符串提供当前运行的 token/成本用量。
// 在消费侧声明，使 internal/tools 不导入 internal/agent
// 及其 runState。app 接线：在适配器上实现 UsageJSON，该适配器包装
// runState（例如其累计的 llm.Usage / 花费美元数）并实现
// Registry.SetUsageSource(adapter)，一旦运行态存在即可调用。
type UsageSource interface {
	UsageJSON() string
}

type usageTool struct {
	source UsageSource
}

var _ Tool = (*usageTool)(nil)

func (*usageTool) Name() string { return "usage" }

func (*usageTool) Description() string {
	return "Report the current run's accumulated token and cost usage as JSON. Read-only."
}

func (*usageTool) Mutating() bool { return false }

func (*usageTool) Schema() string { return `{"type":"object","properties":{}}` }

func (t *usageTool) Run(ctx context.Context, _ string, _ json.RawMessage) (string, error) {
	if t.source == nil {
		return usageUnavailable, nil
	}
	return clip(ctx, t.source.UsageJSON()), nil
}

// SetUsageSource 安装 usage 工具背后的数据源。遵循
// SetCheckpointSource 的模式，工具在原位置重建，因此注册
// 顺序和公布的 spec 保持稳定。nil 数据源使工具报告
// 用量数据不可用，而不是让运行失败。
func (r *Registry) SetUsageSource(src UsageSource) {
	t := &usageTool{source: src}
	if _, exists := r.tools[t.Name()]; !exists {
		r.order = append(r.order, t.Name())
	}
	r.tools[t.Name()] = t
}

// ---- history ----

const historyUnavailable = "history unavailable"

// HistorySource 搜索当前会话的 JSONL 记录。在消费侧声明，
// 使 internal/tools 不导入 internal/session。app 接线：
// 在覆盖活动 *session.Session 的适配器上实现 Search，并在
// 会话已知后调用 Registry.SetHistorySource(adapter)。适配器
// 决定空查询以及非正或过大限制的行为。
type HistorySource interface {
	Search(query string, limit int) string
}

type historyTool struct {
	source HistorySource
}

var _ Tool = (*historyTool)(nil)

func (*historyTool) Name() string { return "history" }

func (*historyTool) Description() string {
	return "Search the current session's records for a substring in messages and tool calls. Read-only."
}

func (*historyTool) Mutating() bool { return false }

func (*historyTool) Schema() string {
	return `{"type":"object","properties":{"query":{"type":"string","description":"substring to match; empty lists the most recent records"},"limit":{"type":"integer","description":"maximum matches to return; default is left to the source"}}}`
}

func (t *historyTool) Run(ctx context.Context, _ string, raw json.RawMessage) (string, error) {
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("history: bad arguments: %w", err)
	}
	if t.source == nil {
		return historyUnavailable, nil
	}
	return clip(ctx, t.source.Search(args.Query, args.Limit)), nil
}

// SetHistorySource 安装 history 工具背后的数据源。工具
// 在原位置重建，因此注册顺序保持稳定；nil 数据源使
// 工具报告历史不可用，而不是让运行失败。
func (r *Registry) SetHistorySource(src HistorySource) {
	t := &historyTool{source: src}
	if _, exists := r.tools[t.Name()]; !exists {
		r.order = append(r.order, t.Name())
	}
	r.tools[t.Name()] = t
}
