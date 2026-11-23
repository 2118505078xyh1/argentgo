package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const checkpointDiffUnavailable = "checkpoint diff unavailable: no checkpoint source configured"

// FileChange 是 checkpoint_diff 渲染的、中性的逐文件差异摘要。
// 它只镜像检查点记录能提供的信息（路径、前/后
// 哈希、可选行数），使本包无需导入
// internal/session。
type FileChange struct {
	Path         string `json:"path"`
	Status       string `json:"status,omitempty"` // modified | added | deleted | unchanged
	BeforeSHA256 string `json:"before_sha256,omitempty"`
	AfterSHA256  string `json:"after_sha256,omitempty"`
	Added        int    `json:"added,omitempty"`
	Removed      int    `json:"removed,omitempty"`
	// #44：有界的逐行 hunk，头部带 +N/-M 计数。下面三个标志在无法给出
	// hunk 时解释原因：binary/oversized 只给标志，degraded 只给 ±N 计数。
	Hunks     []DiffHunk `json:"hunks,omitempty"`
	Binary    bool       `json:"binary,omitempty"`
	Oversized bool       `json:"oversized,omitempty"`
	Degraded  bool       `json:"degraded,omitempty"`
}

// DiffHunk 是一个统一 diff 片段：Header 形如
// "@@ -1,3 +1,4 @@ (+1/-0)"，Lines 的每项以空格/'+'/'-' 开头表示上下文/新增/删除。
type DiffHunk struct {
	Header string   `json:"header"`
	Lines  []string `json:"lines"`
}

// CheckpointSource 提供检查点差异数据。它在消费侧、
// 即此处声明，使 internal/tools 保持独立于 internal/session。
//
// app 适配器包装一个会话和工作区根目录：按 id 找到检查点
// （空表示最新），用 FileChangeFromBytes 把每个 session.CheckpointFile 映射为
// FileChange，使用 f.Before 和当前工作区字节，并
// 返回切片。用 Registry.SetCheckpointSource 注册该适配器。
type CheckpointSource interface {
	Diff(id string) ([]FileChange, error)
}

// SetCheckpointSource 注册只读的 checkpoint_diff 工具。在
// internal/app 知道会话目录后调用它。传入 nil 注册
// 一个报告"checkpoint diff unavailable"而非让运行失败的工具。
func (r *Registry) SetCheckpointSource(src CheckpointSource) {
	t := newCheckpointDiffTool(src)
	if _, exists := r.tools[t.Name()]; !exists {
		r.order = append(r.order, t.Name())
	}
	r.tools[t.Name()] = t
}

type checkpointDiffTool struct {
	source CheckpointSource
}

var _ Tool = (*checkpointDiffTool)(nil)

func newCheckpointDiffTool(source CheckpointSource) *checkpointDiffTool {
	return &checkpointDiffTool{source: source}
}

func (checkpointDiffTool) Name() string { return "checkpoint_diff" }

func (checkpointDiffTool) Description() string {
	return "Summarize a checkpoint's per-file changes: paths, before/after SHA-256, added/removed lines and bounded line-level hunks. Read-only."
}

func (checkpointDiffTool) Mutating() bool { return false }

func (checkpointDiffTool) Schema() string {
	return `{"type":"object","properties":{"checkpoint_id":{"type":"string","description":"checkpoint record id; empty selects the latest"},"root":{"type":"string","description":"optional workspace root override; defaults to the tool root"}},"required":["checkpoint_id"]}`
}

func (t *checkpointDiffTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		CheckpointID string `json:"checkpoint_id"`
		Root         string `json:"root"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("checkpoint_diff: bad arguments: %w", err)
	}
	// 工作区根目录在构造时绑定到适配器。可选的
	// root 字段为 schema 兼容性而接受，但这里
	// 不需要，因为 Tool.Run 已收到权威的注册表根目录。
	_ = args.Root
	_ = root

	if t.source == nil {
		return checkpointDiffUnavailable, nil
	}
	changes, err := t.source.Diff(args.CheckpointID)
	if err != nil {
		return checkpointDiffUnavailable + ": " + err.Error(), nil
	}
	return clipCheckpointDiff(ctx, renderCheckpointDiff(changes)), nil
}

const checkpointDiffMarker = "\n... [truncated] ...\n"

// clipCheckpointDiff 是严格有界的裁剪：clipTo 在其头尾预算之上
// 再加一个标记，因此把调用方的上限减去标记大小，
// 使整个输出（含标记）保持在配置的上限内。
func clipCheckpointDiff(ctx context.Context, text string) string {
	limit := outputLimit(ctx) - len(checkpointDiffMarker)
	if limit < 1 {
		limit = 1
	}
	return clipTo(text, limit)
}

type checkpointDiffOutput struct {
	Count int          `json:"count"`
	Files []FileChange `json:"files"`
}

// renderCheckpointDiff 发出紧凑 JSON，路径顺序稳定，使
// 模型无论数据源迭代顺序如何都看到确定性的摘要。
func renderCheckpointDiff(changes []FileChange) string {
	files := make([]FileChange, 0, len(changes))
	files = append(files, changes...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	out := checkpointDiffOutput{Count: len(files), Files: files}
	data, err := json.Marshal(out)
	if err != nil {
		return checkpointDiffUnavailable + ": encode diff"
	}
	return string(data)
}

// FileChangeFromBytes 用检查点适配器手头的前/后字节
// 构建 FileChange。nil 切片表示文件在那一侧不存在；
// 非 nil 的空切片表示一个存在的空文件。
func FileChangeFromBytes(path string, before, after []byte) FileChange {
	change := FileChange{Path: path, Status: "modified"}
	switch {
	case before == nil && after != nil:
		change.Status = "added"
	case before != nil && after == nil:
		change.Status = "deleted"
	}
	if before != nil {
		sum := sha256.Sum256(before)
		change.BeforeSHA256 = hex.EncodeToString(sum[:])
	}
	if after != nil {
		sum := sha256.Sum256(after)
		change.AfterSHA256 = hex.EncodeToString(sum[:])
	}
	d := computeLineDiffDeadline(before, after, time.Now().Add(checkpointDiffTimeout))
	change.Added, change.Removed = d.added, d.removed
	change.Hunks, change.Binary, change.Oversized, change.Degraded = d.hunks, d.binary, d.oversized, d.degraded
	return change
}

const (
	maxDiffLinesForLCS = 4000
	// 单侧字节超过此上限：只给 oversized 标志，不生成 hunk。
	maxDiffBytesForHunks = 1 << 20
	// 所有 hunk 累计行数硬上限，超出即降级并标记 degraded。
	maxHunkLinesForDiff = 400
	// 变更点两侧的上下文行数。
	hunkContextLines = 3
	// LCS 超时预算：到期即只给 ±N 计数（degraded）。
	checkpointDiffTimeout = 50 * time.Millisecond
)

// DiffLineCounts 返回 before 与 after 之间的 (新增, 删除) 行数。
// 它复用与 hunk 相同的有界 LCS；超大/超时输入回退到整文件计数，
// 不分配平方级的表。CRLF 被规范化为 LF，仅行尾变化不夸大结果。
func DiffLineCounts(before, after []byte) (added, removed int) {
	d := computeLineDiffDeadline(before, after, time.Now().Add(checkpointDiffTimeout))
	return d.added, d.removed
}

// lineDiffResult 是一次逐行 diff 的全部产物。hunks 为空时，
// binary/oversized/degraded 至少有一个为真，解释为何没有行级细节。
type lineDiffResult struct {
	added, removed              int
	hunks                       []DiffHunk
	binary, oversized, degraded bool
}

type diffOp struct {
	kind byte // ' ' 上下文 | '-' 删除 | '+' 新增
	text string
}

// computeLineDiffDeadline 用纯标准库 LCS 生成有界 hunk。deadline 由调用方
// 注入（生产为 50ms 后），测试可传入已过期的时间确定性地验证超时降级。
func computeLineDiffDeadline(before, after []byte, deadline time.Time) lineDiffResult {
	var res lineDiffResult
	// 二进制与超大文件只给标志 + 回退计数，不做逐行处理。
	if bytes.IndexByte(before, 0) >= 0 || bytes.IndexByte(after, 0) >= 0 {
		res.binary = true
		res.added, res.removed = fallbackLineCounts(before, after)
		return res
	}
	if len(before) > maxDiffBytesForHunks || len(after) > maxDiffBytesForHunks {
		res.oversized = true
		res.added, res.removed = fallbackLineCounts(before, after)
		return res
	}
	a, b := splitDiffLines(before), splitDiffLines(after)
	if len(a) > maxDiffLinesForLCS || len(b) > maxDiffLinesForLCS ||
		len(a)*len(b) > maxDiffLinesForLCS*maxDiffLinesForLCS {
		res.degraded = true
		res.added, res.removed = len(b), len(a)
		return res
	}

	// dp[i][j] 是 a[:i] 与 b[:j] 的 LCS 长度。
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := 1; i <= len(a); i++ {
		if i%256 == 0 && time.Now().After(deadline) {
			res.degraded = true
			res.added, res.removed = len(b), len(a)
			return res
		}
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	// 回溯成操作序列，再反转到正向顺序。
	ops := make([]diffOp, 0, len(a)+len(b))
	i, j := len(a), len(b)
	for i > 0 && j > 0 {
		switch {
		case a[i-1] == b[j-1]:
			ops = append(ops, diffOp{' ', a[i-1]})
			i--
			j--
		case dp[i-1][j] >= dp[i][j-1]:
			ops = append(ops, diffOp{'-', a[i-1]})
			i--
		default:
			ops = append(ops, diffOp{'+', b[j-1]})
			j--
		}
	}
	for i > 0 {
		ops = append(ops, diffOp{'-', a[i-1]})
		i--
	}
	for j > 0 {
		ops = append(ops, diffOp{'+', b[j-1]})
		j--
	}
	for l, r := 0, len(ops)-1; l < r; l, r = l+1, r-1 {
		ops[l], ops[r] = ops[r], ops[l]
	}
	for _, op := range ops {
		switch op.kind {
		case '+':
			res.added++
		case '-':
			res.removed++
		}
	}
	res.hunks, res.degraded = buildDiffHunks(ops)
	return res
}

func fallbackLineCounts(before, after []byte) (int, int) {
	return len(splitDiffLines(after)), len(splitDiffLines(before))
}

// buildDiffHunks 把操作序列切成带上下文的 hunk，并受 maxHunkLinesForDiff
// 总行数约束。返回是否因预算被截断（degraded）。
func buildDiffHunks(ops []diffOp) ([]DiffHunk, bool) {
	changed := make([]int, 0)
	for idx, op := range ops {
		if op.kind != ' ' {
			changed = append(changed, idx)
		}
	}
	if len(changed) == 0 {
		return nil, false
	}

	// 每个 op 处的新/旧行号（1-based），供 hunk 头使用。
	oldNo, newNo := make([]int, len(ops)), make([]int, len(ops))
	oldLine, newLine := 1, 1
	for idx, op := range ops {
		oldNo[idx], newNo[idx] = oldLine, newLine
		switch op.kind {
		case ' ':
			oldLine++
			newLine++
		case '-':
			oldLine++
		case '+':
			newLine++
		}
	}

	var hunks []DiffHunk
	degraded := false
	total := 0
	for g := 0; g < len(changed); {
		end := g + 1
		for end < len(changed) && changed[end]-changed[end-1] <= 2*hunkContextLines {
			end++
		}
		first, last := changed[g], changed[end-1]
		start := first - hunkContextLines
		if start < 0 {
			start = 0
		}
		stop := last + hunkContextLines + 1
		if stop > len(ops) {
			stop = len(ops)
		}
		if total+(stop-start) > maxHunkLinesForDiff {
			remaining := maxHunkLinesForDiff - total
			if remaining <= 0 {
				return hunks, true
			}
			stop = start + remaining
			degraded = true
		}
		added, removed := 0, 0
		lines := make([]string, 0, stop-start)
		for idx := start; idx < stop; idx++ {
			op := ops[idx]
			lines = append(lines, string(op.kind)+op.text)
			switch op.kind {
			case '+':
				added++
			case '-':
				removed++
			}
		}
		header := fmt.Sprintf("@@ -%d,%d +%d,%d @@ (+%d/-%d)",
			oldNo[start], countDiffKind(ops[start:stop], ' ', '-'),
			newNo[start], countDiffKind(ops[start:stop], ' ', '+'),
			added, removed)
		hunks = append(hunks, DiffHunk{Header: header, Lines: lines})
		total += stop - start
		g = end
	}
	return hunks, degraded
}

func countDiffKind(ops []diffOp, kinds ...byte) int {
	n := 0
	for _, op := range ops {
		for _, k := range kinds {
			if op.kind == k {
				n++
				break
			}
		}
	}
	return n
}

func splitDiffLines(data []byte) []string {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
