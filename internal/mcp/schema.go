package mcp

import (
	"bytes"
	"encoding/json"
)

// normalizeInputSchema 在 tools/list 的 inputSchema 暴露为
// Tool.Schema 之前对其进行清理。当某个宣告的参数对象格式错误时，
// 提供方会拒绝整个工具请求批次，因此解码器修复真实 MCP 服务器
// 会弄错的三种形态：
//
//   - 没有 "type"：强制为 "object"（JSON Schema 参数需要显式类型）；
//   - "properties" 不是 JSON 对象：丢弃它；
//   - "required" 不是字符串数组：丢弃它。
//
// 其他所有成员都保留。已经通过全部三项检查的 schema 会被
// 逐字节返回，因此行为良好的服务器在键顺序、空白或编码上
// 完全看不到变化。
func normalizeInputSchema(raw json.RawMessage) json.RawMessage {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || t[0] != '{' || !json.Valid(t) {
		// 没有值得保留的对象成员（空、非对象或已损坏）。
		return json.RawMessage(`{"type":"object"}`)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(t, &fields); err != nil {
		return json.RawMessage(`{"type":"object"}`)
	}
	changed := false

	if typ, ok := fields["type"]; !ok || isJSONNull(typ) {
		fields["type"] = json.RawMessage(`"object"`)
		changed = true
	}
	if props, ok := fields["properties"]; ok && !isJSONObject(props) {
		delete(fields, "properties")
		changed = true
	}
	if required, ok := fields["required"]; ok && !isStringArray(required) {
		delete(fields, "required")
		changed = true
	}
	if !changed {
		return raw // 干净的 schema：精确保留原始编码
	}
	out, err := json.Marshal(fields)
	if err != nil {
		// RawMessage 值是合法 JSON，因此这不可能发生；万一发生，保留
		// 原始内容而不是凭空捏造一个 schema。
		return raw
	}
	return out
}

func isJSONNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 4 && string(t) == "null"
}

func isJSONObject(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && t[0] == '{' && json.Valid(t)
}

func isStringArray(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || t[0] != '[' || !json.Valid(t) {
		return false
	}
	var items []json.RawMessage
	if err := json.Unmarshal(t, &items); err != nil {
		return false
	}
	for _, item := range items {
		var text string
		if err := json.Unmarshal(item, &text); err != nil {
			return false
		}
	}
	return true
}
