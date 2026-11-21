package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// RecoveryTool 是 read_output_artifact 的实现：一个 opt-in 的只读工具，
// 让调用方按含端点的 1-based 行范围取回此前被 SpillLargeOutput 省略的
// 溢出产出。
//
// 它刻意不进入 builtin_register.go 的静态注册表，也不被
// New/NewWithLimit 调用，因此 Specs()/Names() 的默认清单逐字节不变。
// 只有显式调用 RegisterRecovery 才会把它加入某个 Registry。所有
// 路径校验（遍历段、规范产出名、目录边界、符号链接）都由绑定的
// SpillRecovery 完成；本工具不重新实现校验链，因而不可能与
// SpillRecovery.Read 产生安全语义上的分歧。
type RecoveryTool struct {
	recovery *SpillRecovery
}

var _ Tool = (*RecoveryTool)(nil)

// NewRecoveryTool 把溢出产出目录 dir 绑定到一个恢复工具实例。
// dir 为空时沿用 NewSpillRecovery 的约定（按当前目录处理）。
func NewRecoveryTool(dir string) *RecoveryTool {
	return &RecoveryTool{recovery: NewSpillRecovery(dir)}
}

func (t *RecoveryTool) Name() string { return "read_output_artifact" }

func (t *RecoveryTool) Description() string {
	return "Read back a previously spilled output artifact by 1-based inclusive line range. " +
		"Only paths that the spill layer itself wrote (flat canonical artifact names, no traversal, no symlinks) are accepted. " +
		"start_line below 1 is clamped to 1; end_line <= 0 reads to end of file. Read-only."
}

func (t *RecoveryTool) Mutating() bool { return false }

func (t *RecoveryTool) Schema() string {
	return `{"type":"object","properties":{"path":{"type":"string","description":"absolute path of the spill artifact to read back"},"start_line":{"type":"integer","description":"1-based first line (clamped to 1 when < 1)"},"end_line":{"type":"integer","description":"1-based inclusive last line; <= 0 reads to end of file"}},"required":["path"]}`
}

// recoveryToolArgs 是 Run 接受的 JSON 参数。path 是溢出标记公布的
// 绝对路径；start_line/end_line 省略时为 0，分别被 SpillRecovery
// 解释为"首行"与"读到文件末尾"。
type recoveryToolArgs struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

// Run 解析 path/start_line/end_line 后委托给构造时绑定的
// SpillRecovery.Read。注册表的 root 与本工具无关：恢复视图只认
// 构造时绑定的产出目录，绝不借 root 扩大可读范围。
func (t *RecoveryTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args recoveryToolArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("read_output_artifact: bad arguments: %w", err)
	}
	_ = ctx
	_ = root
	return t.recovery.Read(args.Path, args.StartLine, args.EndLine)
}

// RegisterRecovery 把恢复工具显式加入 r（opt-in）。它是幂等的：
// 重复调用或已存在同名工具时不再插入；已有工具的顺序不受影响，
// 新工具追加在末尾。传入 nil 是空操作。
//
// 本函数绝不被 New/NewWithLimit 调用；只有需要恢复能力的调用方
// （例如未来的 TUI/session 接线层）才会显式调用它。因此默认不
// 注册时 Specs() 与 golden 快照完全一致。
func RegisterRecovery(r *Registry, dir string) {
	if r == nil {
		return
	}
	const name = "read_output_artifact"
	if _, exists := r.tools[name]; exists {
		return
	}
	r.tools[name] = NewRecoveryTool(dir)
	r.order = append(r.order, name)
}
