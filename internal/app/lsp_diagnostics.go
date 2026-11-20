package app

// 诊断桥接。internal/lsp 已经提供版本感知、可等待、会丢弃过期发布的获取方式（Client.Diagnostics）。
// 本文件把它呈现为中性 app 侧 DTO 加上一个纯格式化器，
// 使调用方能在不导入 internal/lsp、也不改动任何 tools 签名的情况下，
// 把一段紧凑的"仅错误"文本块附加到工具结果上。
//
// 优雅降级：缺失/不支持的语言服务、缓慢的服务或任何传输错误都会产生空结果且不返回错误。
// 诊断仅供参考；它们绝不能使一个原本成功的操作失败，也不改变默认行为。

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/lsp"
)

const (
	// diagnosticsWait 限制一次 Diagnostics 调用在刷新文档后等待新发布的时间。
	// 它是建议性的：缓慢的服务不能拖住运行，因此超时会降级为空结果。
	diagnosticsWait = 2 * time.Second

	// maxReportDiagnostics 是 FormatDiagnostics 应用的每文件上限。
	maxReportDiagnostics = 20

	// diagnosticsTruncFmt 在每文件上限丢弃条目时追加。
	diagnosticsTruncFmt = "... (%d more error(s) truncated)"
)

// NeutralDiagnostic 是供 app 侧消费者使用的提供方中立的问题报告。
// 与 LSP 线上格式不同，Line 和 Column 是从 1 开始的，因此可以直接粘贴到 file:line:column 引用中。
type NeutralDiagnostic struct {
	Line     int
	Column   int
	Severity string // "error"、"warning"、"information" 或 "hint"
	Message  string
	Source   string
}

// fetchDiagnostics 是测试替换的接缝，以避免使用真实语言服务。
// 生产环境刷新文档（didOpen/didChange），然后等待新发布；
// lsp 客户端的版本新鲜度过滤确保较旧的发布绝不会被当作当前结果返回。
var fetchDiagnostics = func(ctx context.Context, client *lsp.Client, absPath string) ([]lsp.Diagnostic, error) {
	uri, err := client.Open(ctx, absPath)
	if err != nil {
		return nil, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, lspCallTimeout)
	defer cancel()
	return client.Diagnostics(waitCtx, uri, diagnosticsWait)
}

// Diagnostics 用其语言服务刷新 filePath，并返回它为该路径发布的诊断，行列号从 1 开始。
//
// 当没有配置或安装语言服务、扩展名不受支持、文档无法打开或等待超时时，
// 它优雅降级为空切片且不返回错误。诊断仅供参考，绝不能改变触发它们的操作的结果。
func (a *lspIdeAdapter) Diagnostics(ctx context.Context, filePath string) ([]NeutralDiagnostic, error) {
	client, err := a.clientForPath(filePath)
	if err != nil {
		return []NeutralDiagnostic{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	items, err := fetchDiagnostics(ctx, client, a.resolvePath(filePath))
	if err != nil {
		// 超时、取消和传输错误都会降级为空。特别是超时可能只有过期的缓存诊断，
		// 对于决定修复什么的模型而言，这比没有诊断更糟。
		return []NeutralDiagnostic{}, nil
	}
	return mapNeutralDiagnostics(items), nil
}

// mapNeutralDiagnostics 把 lsp 诊断（位置从 0 开始）转换为位置从 1 开始的中性 DTO。
// 它始终返回非 nil 的切片。
func mapNeutralDiagnostics(items []lsp.Diagnostic) []NeutralDiagnostic {
	out := make([]NeutralDiagnostic, 0, len(items))
	for _, d := range items {
		out = append(out, NeutralDiagnostic{
			Line:     d.Range.Start.Line + 1,
			Column:   d.Range.Start.Character + 1,
			Severity: d.Severity,
			Message:  d.Message,
			Source:   d.Source,
		})
	}
	return out
}

// FormatDiagnostics 只把错误级别的诊断渲染为紧凑的
// "path:line:column [source]: message" 行，按位置排序，每文件最多 max 条，
// 丢弃条目时带有截断标记。NeutralDiagnostic 中的行和列已经是从 1 开始的。
// 非正的 max 使用 maxReportDiagnostics；没有错误时返回空字符串。
//
// 这是纯函数：输入切片从不被修改，也不执行 I/O，因此可以在任何工具结果上安全调用。
func FormatDiagnostics(path string, diags []NeutralDiagnostic, max int) string {
	if max <= 0 {
		max = maxReportDiagnostics
	}
	errs := make([]NeutralDiagnostic, 0, len(diags))
	for _, d := range diags {
		if strings.EqualFold(strings.TrimSpace(d.Severity), "error") {
			errs = append(errs, d)
		}
	}
	if len(errs) == 0 {
		return ""
	}
	sort.SliceStable(errs, func(i, j int) bool {
		if errs[i].Line != errs[j].Line {
			return errs[i].Line < errs[j].Line
		}
		return errs[i].Column < errs[j].Column
	})

	shown := len(errs)
	if shown > max {
		shown = max
	}
	var b strings.Builder
	for _, d := range errs[:shown] {
		b.WriteString(path)
		if path != "" {
			b.WriteByte(':')
		}
		b.WriteString(strconv.Itoa(d.Line))
		if d.Column > 0 {
			b.WriteByte(':')
			b.WriteString(strconv.Itoa(d.Column))
		}
		if d.Source != "" {
			b.WriteString(" [")
			b.WriteString(d.Source)
			b.WriteByte(']')
		}
		b.WriteString(": ")
		b.WriteString(strings.TrimSpace(d.Message))
		b.WriteByte('\n')
	}
	if dropped := len(errs) - shown; dropped > 0 {
		fmt.Fprintf(&b, diagnosticsTruncFmt, dropped)
		b.WriteByte('\n')
	}
	return strings.TrimSuffix(b.String(), "\n")
}
