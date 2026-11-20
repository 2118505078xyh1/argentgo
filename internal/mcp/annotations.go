package mcp

import (
	"bytes"
	"encoding/json"
)

// ToolAnnotations 是 MCP 工具 annotations 成员的完整数据模型。
// 字段语义遵循 MCP 规范：readOnlyHint、idempotentHint 缺省为 false，
// destructiveHint、openWorldHint 缺省为 true（详见各字段注释）。
// 本类型是纯加法能力：它不改变 Tool.ReadOnly 既有的 readOnlyHint 行为。
type ToolAnnotations struct {
	// ReadOnly 对应 readOnlyHint：为 true 时工具不修改环境。缺省 false。
	ReadOnly bool `json:"read_only,omitempty"`
	// Destructive 对应 destructiveHint：为 true 时工具可能执行破坏性更新；
	// 仅在 ReadOnly 为 false 时有意义。缺省 true（MCP 规范默认值）。
	Destructive bool `json:"destructive,omitempty"`
	// Idempotent 对应 idempotentHint：为 true 时重复调用不会产生额外效果；
	// 仅在 ReadOnly 为 false 时有意义。缺省 false。
	Idempotent bool `json:"idempotent,omitempty"`
	// OpenWorld 对应 openWorldHint：为 true 时工具可能与开放的外部实体交互。
	// 缺省 true（MCP 规范默认值）。
	OpenWorld bool `json:"open_world,omitempty"`
	// Title 对应 title，是工具的人类可读标题，可包含简体中文等多语言文本。
	Title string `json:"title,omitempty"`
}

// parseToolAnnotations 解析 tools/list 返回的 annotations 成员。
// 它是纯函数：不访问网络、不修改入参；非法输入一律按“未声明”处理，返回
// MCP 规范默认值。各成员独立解析，某个字段类型错误不会丢弃其他字段。
func parseToolAnnotations(raw json.RawMessage) ToolAnnotations {
	// MCP 规范为两个提示位规定了非假默认值：缺省时按“破坏性、开放世界”处理。
	out := ToolAnnotations{Destructive: true, OpenWorld: true}

	t := bytes.TrimSpace(raw)
	if len(t) == 0 || isJSONNull(t) || !isJSONObject(t) {
		return out
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(t, &fields); err != nil {
		return out
	}

	// readBool 返回字段的布尔值与是否成功声明；缺失、null、类型错误均视为未声明。
	readBool := func(key string) (bool, bool) {
		v, ok := fields[key]
		if !ok || isJSONNull(v) {
			return false, false
		}
		var b bool
		if err := json.Unmarshal(v, &b); err != nil {
			return false, false
		}
		return b, true
	}
	if v, ok := readBool("readOnlyHint"); ok {
		out.ReadOnly = v
	}
	if v, ok := readBool("destructiveHint"); ok {
		out.Destructive = v
	}
	if v, ok := readBool("idempotentHint"); ok {
		out.Idempotent = v
	}
	if v, ok := readBool("openWorldHint"); ok {
		out.OpenWorld = v
	}
	if v, ok := fields["title"]; ok && !isJSONNull(v) {
		var title string
		if err := json.Unmarshal(v, &title); err == nil {
			out.Title = title
		}
	}
	return out
}

// annotationsLegacyDecodable 复刻 ListTools 历史上内联结构体的解码约束：
// annotations 必须是对象或 null，且其中的 readOnlyHint（若出现）必须是布尔值。
// 仅用于决定“是否跳过坏工具”，不改变新 DTO 的宽松解析语义。
func annotationsLegacyDecodable(raw json.RawMessage) bool {
	if len(raw) == 0 || isJSONNull(raw) {
		return true
	}
	var probe struct {
		ReadOnlyHint bool `json:"readOnlyHint"`
	}
	return json.Unmarshal(raw, &probe) == nil
}
