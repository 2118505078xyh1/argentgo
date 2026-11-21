// Package agentpolicy 存放 agent 决策层的纯函数：它们只读取参数、不做 I/O、
// 不持有 runState，因此可以脱离循环独立测试，也让 agent 内核更专注于状态机。
//
// 本包只依赖标准库与 internal/llm（线协议类型），不 import internal/agent 或任何
// 其他业务包——这保证依赖方向单向、无环。agent 侧通过类型别名与薄转发函数复用
// 这些实现，所有既有调用点与对外 API 保持不变。
package agentpolicy

import (
	"encoding/json"
	"strings"
)

// NormalizeArgs 规范化原始 JSON 参数对象，使同一调用的两种写法可比较相等：
// 递归排序对象的键并去除空白。非 JSON 参数则折叠空白。
func NormalizeArgs(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}"
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return strings.Join(strings.Fields(raw), " ")
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return string(normalized)
}
