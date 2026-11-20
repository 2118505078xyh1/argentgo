package server

import "github.com/2118505078xyh1/argentgo/internal/security"

// abortResult 生成结构化的取消结果响应，形如
// {"cancelled":true,"reason":"..."}。
//
// success 映射为响应中的 cancelled 字段；reason 是可选的原因说明，
// 会在跨越传输边界前先经 security.Redact 脱敏，避免把具有凭据形状的
// 片段回显给只持有 bearer token 的客户端。这是纯函数：不触碰任何
// 传输状态，且 reason 中的 CJK 等非 ASCII 文本按 UTF-8 原样保留
// （encoding/json 默认不会把 CJK 转义为 \uXXXX）。
func abortResult(success bool, reason string) map[string]any {
	return map[string]any{
		"cancelled": success,
		"reason":    security.Redact(reason),
	}
}
