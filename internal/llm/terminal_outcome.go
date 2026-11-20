package llm

import (
	"net/http"
	"regexp"
)

// maxTerminalScanBytes 限制终态结果分类器检查响应体的篇幅。
// 提供方的错误载荷很小；畸形或恶意的响应体
// 不得让分类变成无界操作。
const maxTerminalScanBytes = 64 * 1024

// terminalOutcomePatterns 列出提供方报告的、无法通过重试解决的
// 结果：账单无法结算、账目记录无法生成，
// 或某个策略/hook 已经拒绝了请求。它们对应
// Codex 拒绝重试的终态类别。模式匹配
// 不区分大小写，并容忍词间的 '_'、'-' 和空格，因此
// JSON 错误码和易读消息都能被识别。
//
// 顺序很重要：更具体的码排在较短的前缀之前，因此
// TerminalOutcomeReason 报告准确的原因（spend_limit_exceeded 而非
// spend_limit）。
var terminalOutcomePatterns = []struct {
	reason string
	re     *regexp.Regexp
}{
	{"session_budget_exceeded", regexp.MustCompile(`(?i)session[\s_-]*budget[\s_-]*exceeded`)},
	{"organization_budget_exceeded", regexp.MustCompile(`(?i)organization[\s_-]*budget[\s_-]*exceeded`)},
	{"spend_limit_exceeded", regexp.MustCompile(`(?i)spend[\s_-]*limit[\s_-]*exceeded`)},
	{"spend_limit", regexp.MustCompile(`(?i)spend[\s_-]*limit`)},
	{"usage_accounting_failed", regexp.MustCompile(`(?i)usage[\s_-]*accounting[\s_-]*failed`)},
	{"inference_hook_denied", regexp.MustCompile(`(?i)inference[\s_-]*hook[\s_-]*denied`)},
	{"billing_hard_limit_reached", regexp.MustCompile(`(?i)billing[\s_-]*hard[\s_-]*limit[\s_-]*reached`)},
	{"insufficient_quota", regexp.MustCompile(`(?i)insufficient[\s_-]*quota`)},
}

// IsTerminalProviderOutcome 报告某提供方响应是否描述了一个
// 终态结果：重试无法消除该状况（会话或
// 组织预算已用尽、支出上限已触发、用量计费
// 失败、推理 hook 拒绝了请求等）。这类响应
// 必须原样交回调用方，而不是等待，
// 因为任何退避都不会让它成功。
//
// body 应为已经脱敏的响应体（调用方传入有界前缀）。
// 该分类器从不记录或回显响应体；它返回的唯一字符串
// 就是下面固定的规范原因。脱敏被防御性地
// 再次应用，使恰好含模式词的密钥不会
// 造成误报。扫描受 maxTerminalScanBytes 限制，
// 因此畸形响应体也保持有界。HTTP 402（Payment Required）
// 本身即为终态，即使响应体为空或无法识别。
func IsTerminalProviderOutcome(body []byte, statusCode int) bool {
	return TerminalOutcomeReason(body, statusCode) != ""
}

// TerminalOutcomeReason 返回某个终态提供方结果的规范原因字符串，
// 或当响应非终态时返回 ""。调用方可以把返回的原因
// 包含在面向用户的消息中，
// 而绝不暴露原始响应体。
func TerminalOutcomeReason(body []byte, statusCode int) string {
	// 在任何分配或匹配之前先限制扫描范围。
	if len(body) > maxTerminalScanBytes {
		body = body[:maxTerminalScanBytes]
	}
	if len(body) > 0 {
		// 防御性脱敏：凭据形态的值不得影响
		// 判定。正常调用方已经传入脱敏后的响应体。
		text := RedactText(string(body))
		for _, p := range terminalOutcomePatterns {
			if p.re.MatchString(text) {
				return p.reason
			}
		}
	}
	if statusCode == http.StatusPaymentRequired {
		return "payment_required"
	}
	return ""
}
