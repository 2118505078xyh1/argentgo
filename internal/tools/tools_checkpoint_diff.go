package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
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
	return "Summarize a checkpoint's per-file changes: paths, before/after SHA-256 and added/removed lines. Read-only."
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
	change.Added, change.Removed = DiffLineCounts(before, after)
	return change
}

const maxDiffLinesForLCS = 4000

// DiffLineCounts 返回 before 与 after 之间的 (新增, 删除) 行数。
// 它使用有界 LCS，使未变化的行不被计为编辑；
// 对于非常大的输入，它回退到整文件计数，而不是
// 分配一个平方级的表。CRLF 会被规范化为 LF，因此仅行尾
// 变化不会夸大结果。
func DiffLineCounts(before, after []byte) (added, removed int) {
	a := splitDiffLines(before)
	b := splitDiffLines(after)
	if len(a) > maxDiffLinesForLCS || len(b) > maxDiffLinesForLCS ||
		len(a)*len(b) > maxDiffLinesForLCS*maxDiffLinesForLCS {
		return len(b), len(a)
	}
	// dp[i][j] 是 a[:i] 与 b[:j] 的 LCS 长度。
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := 1; i <= len(a); i++ {
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
	common := dp[len(a)][len(b)]
	return len(b) - common, len(a) - common
}

func splitDiffLines(data []byte) []string {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
