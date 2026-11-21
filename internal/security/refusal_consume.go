package security

// 本文件提供“消费侧”辅助：把 ReasonFromError 产出的结构化拒绝原因转成模型
// 可读提示、重试决策、稳定的单行审计描述，以及把多个拒绝归并成最严重的一个。
// 全部为纯函数，只读入参，不修改任何现有错误类型或错误文本，当前零调用方。

import (
	"strings"
	"unicode/utf8"
)

// ModelHintFor 把 err 映射成模型可读的自我修正提示。
// 未匹配任何已知拒绝时返回空串，调用方应据此跳过提示注入。
// 它只调用 ReasonFromError，不会修改 err。
func ModelHintFor(err error) string {
	r, ok := ReasonFromError(err)
	if !ok {
		return ""
	}
	return r.ModelHint()
}

// ShouldRetry 判断 err 所代表的拒绝是否值得模型自动重试。
// 只有“可恢复的工具错误”返回 true；人工否决、硬性策略/沙箱拦截、审批通道
// 不可用以及任何未匹配的拒绝一律返回 false（fail-closed）。
func ShouldRetry(err error) bool {
	r, ok := ReasonFromError(err)
	if !ok {
		return false
	}
	return r.Kind == RefusalKindToolError && r.Recoverable
}

// RefusalAuditLine 生成一条稳定的单行审计描述，只包含 Kind 与 Tool。
// Detail 刻意不被写入——它可能携带模型输出或工具原始返回，进入审计日志会放大
// 泄露面；需要细节的调用方应使用已经脱敏的 AuditLog.Record。
// Tool 中的控制字符会被转义以保证单行；中日韩等可打印字符原样保留（CJK 安全）。
func RefusalAuditLine(r RefusalReason) string {
	return "refusal kind=" + escapeAuditToken(string(r.Kind)) +
		" tool=" + escapeAuditToken(r.Tool)
}

// hexDigits 供控制字符转义使用，保持输出确定且可读。
const hexDigits = "0123456789abcdef"

// escapeAuditToken 把 s 转义成可安全写入单行审计日志的 token。
// 反斜杠、换行/回车/制表符等 ASCII 控制字符，以及 U+0085/U+2028/U+2029 等
// 行分隔字符都会被转成可见转义；其余 rune（含 CJK、emoji）逐字节原样保留，
// 因此不会切坏 UTF-8 多字节序列，同一输入永远得到同一结果。
func escapeAuditToken(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '\\':
			b.WriteString("\\\\")
		case r == '\n':
			b.WriteString("\\n")
		case r == '\r':
			b.WriteString("\\r")
		case r == '\t':
			b.WriteString("\\t")
		case r == utf8.RuneError && size == 1:
			// 非法 UTF-8 字节按 \xNN 逐字节转义，保证单行且可逆。
			b.WriteString("\\x")
			c := s[i]
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0x0f])
		case r < 0x20 || r == 0x7f:
			b.WriteString("\\x")
			b.WriteByte(hexDigits[byte(r)>>4])
			b.WriteByte(hexDigits[byte(r)&0x0f])
		case r == '\u0085' || r == '\u2028' || r == '\u2029':
			b.WriteString("\\u")
			b.WriteByte(hexDigits[(r>>12)&0xf])
			b.WriteByte(hexDigits[(r>>8)&0xf])
			b.WriteByte(hexDigits[(r>>4)&0xf])
			b.WriteByte(hexDigits[r&0xf])
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// refusalSeverity 给出类别的严重程度，数值越大越严重。
// 未匹配的零值最轻；工具错误比不可用更严重，人工否决比工具错误更严重。
func refusalSeverity(k RefusalKind) int {
	switch k {
	case RefusalKindPolicyDeny:
		return 5
	case RefusalKindSandboxDenied:
		return 4
	case RefusalKindApprovalDeclined:
		return 3
	case RefusalKindToolError:
		return 2
	case RefusalKindUnavailable:
		return 1
	default:
		return 0
	}
}

// MergeRefusals 返回 errs 中最严重的拒绝原因（取映射后的整个 RefusalReason，
// 含 Tool/Detail/Recoverable）。严重程度按 policy-deny > sandbox-denied >
// approval-declined > tool-error > unavailable > 零值 排列。
// 未匹配的错误与空 errs 一律贡献零值；同分时先出现者胜出，保证稳定。
// 它只读 errs，不修改任何传入的错误对象。
func MergeRefusals(errs ...error) RefusalReason {
	best := RefusalReason{}
	bestSeverity := 0
	for _, err := range errs {
		r, ok := ReasonFromError(err)
		if !ok {
			continue
		}
		severity := refusalSeverity(r.Kind)
		if severity > bestSeverity {
			best = r
			bestSeverity = severity
		}
	}
	return best
}
