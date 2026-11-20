package config

import (
	"fmt"
	"sort"
	"strings"
)

// diagnosticSeverity 把诊断类别映射为审计报告使用的严重级别。
// invalid 是无法执行的硬性错误，conflict 是需要留意的取值冲突，
// unsupported 是已被忽略的旧设置，仅供参考。未知类别一律按提示处理，
// 以免将来新增类别时被格式化器静默丢弃。
func diagnosticSeverity(kind DiagnosticKind) string {
	switch kind {
	case DiagnosticInvalid:
		return "error"
	case DiagnosticConflict:
		return "warning"
	case DiagnosticUnsupported:
		return "info"
	default:
		return "info"
	}
}

// diagnosticNewlineReplacer 把消息内嵌的换行压成单个空格，
// 保证"一条诊断一行"的契约。它按完整的 UTF-8 字节序列匹配换行符，
// 不会切坏消息里的 CJK 字符。
var diagnosticNewlineReplacer = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ")

// formatAllDiagnostics 把全部配置诊断格式化为换行分隔的多行文本，
// 供 dsh 配置审计一次性报告所有问题，而不是像 Validate 那样只返回
// 首个错误。
//
// 该函数是纯函数：不修改传入的切片，也不读取任何全局可变状态。
// 输出遵循以下约定：
//   - 空输入（nil 或长度为 0）返回空串；
//   - 每行按"严重级别/类别 路径: 消息"排列，路径、消息、严重级别齐全；
//   - 先按 (路径, 类别, 消息) 字典序稳定排序，再丢弃完全相同的相邻项，
//     因此同一组诊断无论以何种顺序传入，都得到逐字节一致的文本；
//   - 消息内的 CR/LF 被替换为空格，确保一条诊断恰好占一行；
//   - 全程按字符串（UTF-8）处理，不按字节截断，CJK 文本保持完整。
func formatAllDiagnostics(diags []Diagnostic) string {
	if len(diags) == 0 {
		return ""
	}

	// 复制后排序，避免改变调用方持有的切片元素顺序。
	ordered := append([]Diagnostic(nil), diags...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Path != ordered[j].Path {
			return ordered[i].Path < ordered[j].Path
		}
		if ordered[i].Kind != ordered[j].Kind {
			return ordered[i].Kind < ordered[j].Kind
		}
		return ordered[i].Message < ordered[j].Message
	})

	// Diagnostic 全由可比较的字符串字段组成，可直接在排序后的切片上
	// 原地去重：完全相同的项排序后必定相邻。
	unique := ordered[:0]
	for _, d := range ordered {
		if len(unique) > 0 && unique[len(unique)-1] == d {
			continue
		}
		unique = append(unique, d)
	}

	lines := make([]string, 0, len(unique))
	for _, d := range unique {
		lines = append(lines, formatDiagnosticLine(d))
	}
	return strings.Join(lines, "\n")
}

// formatDiagnosticLine 把单条诊断渲染为一行文本。
func formatDiagnosticLine(d Diagnostic) string {
	message := diagnosticNewlineReplacer.Replace(d.Message)
	return fmt.Sprintf("%s/%s %s: %s", diagnosticSeverity(d.Kind), d.Kind, d.Path, message)
}

// ValidateAll 是 dsh 配置审计等场景的 opt-in 入口：它与 Validate 执行
// 相同的校验集合，但借助 formatAllDiagnostics 一次返回全部诊断，而不是
// 只给出首个错误。默认路径（Validate / Load）的行为完全不变，
// 因此现有调用方和兼容性测试不受影响；需要完整问题清单的调用方
// 显式改用本方法即可。
func (c Config) ValidateAll() error {
	diagnostics := c.Diagnostics()
	report := formatAllDiagnostics(diagnostics)
	if report == "" {
		return nil
	}
	return fmt.Errorf("config: found %d configuration problem(s):\n%s", len(diagnostics), report)
}
