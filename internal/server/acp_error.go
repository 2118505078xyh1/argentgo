package server

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"strings"
)

// 本文件为 ACP 失败路径提供人可读的恢复文案能力。
//
// 背景：JSON-RPC 的 code/message 只说明"出了什么错"，但 goose 审计指出
// 客户端在 -32000（需要认证）和 error.data.reason=credits_exhausted（额度
// 耗尽）这两类失败上拿不到"下一步该做什么"。这里用一个纯函数把恢复指引
// 追加到原始 message 之后，而不改动 code 或 data 的形状。
//
// 接线是可选的且默认关闭：现有的 RunACP 行为完全不变，只有显式调用
// RunACPWithFriendlyErrors 的集成方才会启用友好文案。

const (
	// acpAuthRecoveryHint 是 -32000（需要认证）时追加的恢复指引。
	acpAuthRecoveryHint = "请重新登录提供方后重试"
	// acpCreditsRecoveryHint 是额度耗尽时的基础恢复指引。
	acpCreditsRecoveryHint = "额度已用尽，请充值后重试"
	// acpRechargeLinkPrefix 用于把充值链接接到恢复指引之后。
	acpRechargeLinkPrefix = "；充值地址："
	// acpHintSeparator 在原消息与恢复指引之间分隔，保留原始诊断信息。
	acpHintSeparator = "；"
	// acpCreditsExhaustedReason 是 error.data.reason 中额度耗尽的原因值。
	acpCreditsExhaustedReason = "credits_exhausted"
)

// errorDataKeys 是按优先级尝试的充值链接键名。首个取到字符串值的键生效。
var errorDataKeys = [...]string{"url", "rechargeUrl", "recharge_url"}

// formatACPError 把 ACP 原始错误消息转换成带人可读恢复指引的消息。
//
// 规则：
//   - code == -32000（codeServerError，ACP 约定为"需要认证"）时，追加
//     "请重新登录提供方后重试"。
//   - data["reason"] == "credits_exhausted" 时，追加额度耗尽指引；若
//     data 中带有合法的充值链接，则一并附上（链接经过校验并去掉 userinfo）。
//   - 其余情况原样返回 message，绝不改写或截断，CJK 文本保持不变。
//
// 该函数是纯函数：不读取全局状态、不发 IO、对 nil data 也安全。
func formatACPError(code int, message string, data map[string]any) string {
	if code == codeServerError {
		return appendACPHint(message, acpAuthRecoveryHint)
	}
	if reason, _ := data["reason"].(string); reason == acpCreditsExhaustedReason {
		hint := acpCreditsRecoveryHint
		if link, ok := rechargeLink(data); ok {
			hint += acpRechargeLinkPrefix + link
		}
		return appendACPHint(message, hint)
	}
	return message
}

// appendACPHint 在保留原消息的前提下追加恢复指引。空消息只返回指引。
// Go 的字符串拼接按字节进行，不会拆开 UTF-8 序列，因此 CJK 文本安全。
func appendACPHint(message, hint string) string {
	if strings.TrimSpace(message) == "" {
		return hint
	}
	return message + acpHintSeparator + hint
}

// rechargeLink 从 error.data 中提取并校验充值链接。只接受带主机名的
// http/https 绝对 URL，并去掉 URL 中的 userinfo，防止把内嵌的账号口令
// 回显给客户端。第二个返回值为 false 表示没有可用的安全链接。
func rechargeLink(data map[string]any) (string, bool) {
	for _, key := range errorDataKeys {
		raw, ok := data[key].(string)
		if !ok || strings.TrimSpace(raw) == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "", false
		}
		if u.User != nil {
			u.User = nil
		}
		return u.String(), true
	}
	return "", false
}

// errorDataMap 把 ACP 失败路径上可能出现的 error.data 形态归一化为
// map[string]any，供 formatACPError 读取。未知形态返回 nil（等价于 data 缺失）。
func errorDataMap(data any) map[string]any {
	switch d := data.(type) {
	case map[string]any:
		return d
	case turnEndErrorData:
		return map[string]any{"turnEndReason": string(d.TurnEndReason)}
	default:
		return nil
	}
}

// fail 发出一条 JSON-RPC 错误回复。仅在启用友好文案时才调用
// formatACPError；默认路径与 peer.fail / peer.failWithData 完全一致，
// 因此现有客户端和测试看到的 code/message/data 不变。
func (s *acpState) fail(id json.RawMessage, code int, message string, data any) {
	if s.friendlyErrors {
		message = formatACPError(code, message, errorDataMap(data))
	}
	if data == nil {
		s.peer.fail(id, code, message)
		return
	}
	s.peer.failWithData(id, code, message, data)
}

// RunACPWithFriendlyErrors 与 RunACP 相同，但对错误回复启用人可读的
// 恢复文案（见 formatACPError）。它是 opt-in 入口：RunACP 本身保持
// 默认行为不变，调用方需要显式选择本函数才会启用。
func RunACPWithFriendlyErrors(ctx context.Context, in io.Reader, out io.Writer, cfg Config) error {
	return runACP(ctx, in, out, cfg, true)
}
