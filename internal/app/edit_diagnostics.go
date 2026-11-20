package app

// 编辑后 LSP 诊断的接线层。这里把已有的 Diagnostics / FormatDiagnostics 能力
// 接到写类工具的成功返回路径上：开关默认关闭，关闭时 hookedTools.Run 不会
// 多调用任何函数，默认运行与未接线前逐字节一致。
//
// 诊断是建议性的：没有语言服务器、扩展不支持、拉取超时或传输错误都会退化成
// 空结果且不报错，绝不改变工具本身的输出与成功/失败判定。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// EnableEditDiagnostics 是编辑后诊断的总开关。默认关闭：只有显式设置
// ARGENT_EDIT_DIAGNOSTICS=1 时才为 true，因此所有默认运行保持不变。
// 开启后 hookedTools.Run 在 write/edit/multi_edit 成功返回时，按 mutationPaths
// 报告的路径拉取 LSP 错误诊断并附加到工具输出；它也可以被配置层直接赋值。
var EnableEditDiagnostics = os.Getenv("ARGENT_EDIT_DIAGNOSTICS") == "1"

// editDiagnosticsLabel 是附加在工具输出末尾的诊断块标签，与 [hook] 注释区分，
// 让模型能分清哪些是语言服务器报告而非钩子建议。
const editDiagnosticsLabel = "[lsp diagnostics]"

// isEditDiagnosticsTool 限定只有三类写工具触发诊断。读类工具与其它变更工具
// （如补丁工具）保持原样，避免扩大默认行为之外的触发面。
func isEditDiagnosticsTool(name string) bool {
	switch name {
	case "write", "edit", "multi_edit":
		return true
	default:
		return false
	}
}

// editDiagnosticsForPath 是诊断拉取的可替换接缝。生产实现复用进程级的 LSP
// 管理器（与 NewLspIdeAdapter 一致），按 root 解析相对路径后调用 Diagnostics
// 并用 FormatDiagnostics 只保留错误级别；无服务器或超时返回空串。
// 测试可替换为离线假实现，避免依赖真实语言服务器。
var editDiagnosticsForPath = func(ctx context.Context, root, path string) string {
	if root != "" {
		if absolute, err := filepath.Abs(root); err == nil && absolute != "" {
			root = absolute
		}
	}
	adapter := &lspIdeAdapter{mgr: getSharedManager(), root: root}
	diags, err := adapter.Diagnostics(ctx, path)
	if err != nil || len(diags) == 0 {
		return ""
	}
	return FormatDiagnostics(path, diags, 0)
}

// appendEditDiagnostics 在写类工具成功返回后按需附加诊断。它在开关关闭、
// 没有 mutationPaths 接缝、非写类工具三种情况下立即返回，零副作用。
// 诊断以注释形式追加，绝不替换工具本身的输出。
func (h *hookedTools) appendEditDiagnostics(ctx context.Context, name, args string, out *string) {
	if !EnableEditDiagnostics || h.mutationPaths == nil || !isEditDiagnosticsTool(name) {
		return
	}
	root := ""
	if h.bridge != nil {
		root = h.bridge.cwd
	}
	for _, path := range h.mutationPaths(name, args) {
		note := editDiagnosticsForPath(ctx, root, path)
		if note == "" {
			continue
		}
		*out = appendDiagnosticNote(*out, note)
	}
}

// appendDiagnosticNote 把一段诊断追加到工具输出末尾并打上标签。空消息是
// 无操作，输出为空时直接成为首行，不会引入前导空行。
func appendDiagnosticNote(out, message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return out
	}
	block := editDiagnosticsLabel + " " + message
	if out == "" {
		return block
	}
	return out + "\n" + block
}
