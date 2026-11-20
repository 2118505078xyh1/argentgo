package app

// 工具执行期的结构化审计上下文。goose 审计指出：工具调用在策略/审批阶段已有
// 审计记录，但执行完成时缺少一份结构化、已脱敏的上下文（工具名、参数摘要、
// 结果状态、耗时与时间戳）。这里提供一个按需生成它的纯函数，以及默认关闭的
// 接线接缝；默认运行既不产生额外输出，也不改变工具的成功/失败判定。

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

const (
	// toolAuditMaxSummaryRunes 是单个摘要字段保留的最大符文数。超限时在符文
	// 边界截断并追加标记，CJK 与 emoji 不会被切成半个 UTF-8 字符。
	toolAuditMaxSummaryRunes = 256
	// toolAuditTruncatedMark 标识摘要已被截断，避免把省略误认为原文结尾。
	toolAuditTruncatedMark = "…[truncated]"
)

// toolAuditResult 是一次已完成工具调用交给审计上下文的结果快照。把耗时与错误
// 一并放进 result，是为了让上下文生成函数保持确定性：同样的入参与时钟总得到
// 同样的映射，读写状态只在调用方发生。
type toolAuditResult struct {
	output  string        // 工具原始输出；脱敏后作为结果摘要
	err     error         // 非 nil 表示调用失败，状态记为 error
	elapsed time.Duration // 执行耗时，格式化为字符串写入上下文
}

// toolAuditContext 生成一次工具调用的结构化审计上下文。它只依赖入参与当前
// 时间，不读写外部状态；所有值都是字符串且已经脱敏、按符文边界截断。
func toolAuditContext(toolName, args string, result toolAuditResult) map[string]string {
	return toolAuditContextAt(toolName, args, result, time.Now().UTC())
}

// toolAuditContextAt 是注入时钟的纯函数版本，便于测试精确断言时间戳与耗时。
// at 为零值时仍按给定值格式化，不做隐式 clock 回退，保证可重复性。
func toolAuditContextAt(toolName, args string, result toolAuditResult, at time.Time) map[string]string {
	status := "ok"
	if result.err != nil {
		status = "error"
	}
	elapsed := result.elapsed
	if elapsed < 0 {
		elapsed = 0
	}
	// 失败但输出为空时，用错误文本充当摘要，审计员仍能知道失败原因。
	output := result.output
	if result.err != nil && output == "" {
		output = result.err.Error()
	}
	return map[string]string{
		"tool":    auditSummary(toolName),
		"args":    auditArgsSummary(args),
		"result":  auditSummary(output),
		"status":  status,
		"elapsed": elapsed.String(),
		"ts":      at.Format(time.RFC3339Nano),
	}
}

// auditArgsSummary 对 JSON 参数先做结构化脱敏（覆盖值本身不像凭证、但键名
// 是 api_key/password/token 之类的字段），再做统一的长度截断。非 JSON 输入
// 退化为普通文本脱敏，不会丢失原文。
func auditArgsSummary(args string) string {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		return ""
	}
	return auditSummary(security.RedactJSON([]byte(trimmed)))
}

// auditSummary 脱敏一段文本并在符文边界截断到上限。Redact 只替换匹配到的
// 字节区间，其余字节（含 CJK 多字节字符）原样保留，因此天然 CJK 安全；
// 截断走 rune 切片，绝不会留下孤立的中继字节。
func auditSummary(text string) string {
	if text == "" {
		return ""
	}
	text = security.Redact(text)
	if utf8.RuneCountInString(text) <= toolAuditMaxSummaryRunes {
		return text
	}
	return string([]rune(text)[:toolAuditMaxSummaryRunes]) + toolAuditTruncatedMark
}
