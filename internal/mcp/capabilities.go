package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
)

// ServerCapabilities 是服务器在 initialize 响应中宣告的能力集合。
// nil 指针表示该能力未被声明。
// MCP 把能力值编码为对象（例如 {"listChanged":true}），
// 因此 ParseCapabilities 把任何非 null 值的存在映射为 true；
// 显式的 JSON false 保留为 false。
type ServerCapabilities struct {
	Tools     *bool
	Prompts   *bool
	Resources *bool

	// declared 记录服务器是否发送了 capabilities 对象。
	// 它区分"服务器什么都没说"（旧服务器：继续探测）
	// 与"服务器宣告了能力但没有这一项"（跳过该
	// 请求，而不是得到空结果和 stdout 噪声）。
	declared bool
}

// Declared 报告服务器是否返回了 capabilities 对象。为 false 时
// 服务器没有说明其能力，因此调用方应继续探测，
// 而不是把每个缺失的标志都当作不支持。
func (c ServerCapabilities) Declared() bool { return c.declared }

// Enumerated 报告服务器是否至少提到了一种我们理解的能力。
// 空的能力对象（"{}"）在仍提供工具的服务器中很常见，因此它被
// 当作"无可用信息"：调用方继续探测。一旦服务器列出了某项能力，
// 省略另一项就是对应列表方法不受支持的有意义
// 信号。
func (c ServerCapabilities) Enumerated() bool {
	return c.Tools != nil || c.Prompts != nil || c.Resources != nil
}

// Supports 报告指定的能力是否被显式声明为
// 支持。名称匹配不区分大小写。nil 标志（未声明）
// 和显式 false 都返回 false：未声明即不支持。
// 必须容忍没有 capabilities 对象的服务器的调用方，
// 应在依赖此方法前检查 Declared。
func (c ServerCapabilities) Supports(cap string) bool {
	var flag *bool
	switch strings.ToLower(strings.TrimSpace(cap)) {
	case "tools":
		flag = c.Tools
	case "prompts":
		flag = c.Prompts
	case "resources":
		flag = c.Resources
	default:
		return false
	}
	return flag != nil && *flag
}

// ParseCapabilities 解码 initialize 响应的 capabilities 成员。
// 缺失、null、非对象或格式错误的载荷会产生未声明的集合
// （Declared()==false 且每个标志为 nil），使调用方继续探测。键
// 匹配不区分大小写；除 false 外的任何非 null 值都表示
// 服务器支持该能力。
func ParseCapabilities(raw json.RawMessage) ServerCapabilities {
	var caps ServerCapabilities
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || isJSONNull(t) || t[0] != '{' || !json.Valid(t) {
		return caps
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(t, &fields); err != nil {
		return caps
	}
	caps.declared = true
	for key, value := range fields {
		flag := capabilityFlag(value)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "tools":
			caps.Tools = flag
		case "prompts":
			caps.Prompts = flag
		case "resources":
			caps.Resources = flag
		}
	}
	return caps
}

// capabilityFlag 把一个能力值转换为支持位：false 保持
// false，null/缺失保持 nil，其他任何值（包括 MCP
// 通常发送的对象）都表示支持。
func capabilityFlag(raw json.RawMessage) *bool {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || isJSONNull(t) {
		return nil
	}
	var b bool
	if err := json.Unmarshal(t, &b); err == nil {
		return &b
	}
	supported := true
	return &supported
}
