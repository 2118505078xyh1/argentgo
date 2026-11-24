package agent

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"sync/atomic"
)

// coerceExactIntLimit 是 float64 能精确表示的整数边界（2 的 53 次方）。
// 通用 JSON 解码把数字落地为 float64；超过该边界的整数强转会悄悄丢失精度，
// 因此这里一律拒绝：宁可让工具维持原样失败，也不把数据改错。
const coerceExactIntLimit int64 = 1 << 53

// toolArgCoercion 控制是否把保守强转接线到派发路径，默认关闭。
var toolArgCoercion atomic.Bool

// SetToolArgumentCoercion 打开或关闭工具参数的保守强转。关闭（默认）时，工具收到的
// 参数字节与既有行为完全一致；打开后，仅当 schema 明确声明数字/布尔类型且字符串值
// 可无损转换时才改写对应字段。这是进程级开关，由 app 层在启动时统一设置，
// 避免把语义变更悄悄引入既有运行。
func SetToolArgumentCoercion(enabled bool) {
	toolArgCoercion.Store(enabled)
}

// toolArgumentCoercionEnabled 报告当前是否启用了工具参数强转。
func toolArgumentCoercionEnabled() bool {
	return toolArgCoercion.Load()
}

// coerceToolArguments 按 schema 的 properties 声明，把参数表中可无损转换的字符串
// 转成对应的数字或布尔值，返回新的 map，绝不修改入参。schema 解析失败、类型不明
// 或转换有歧义时，相关字段保持原值。
func coerceToolArguments(args map[string]any, schema json.RawMessage) map[string]any {
	out, _ := coerceToolArgumentsChanged(args, schema)
	return out
}

// coerceToolArgumentsChanged 与 coerceToolArguments 相同，并额外报告是否真的有字段被改写。
func coerceToolArgumentsChanged(args map[string]any, schema json.RawMessage) (map[string]any, bool) {
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	if len(args) == 0 || len(schema) == 0 {
		return out, false
	}
	var doc struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &doc); err != nil || len(doc.Properties) == 0 {
		return out, false
	}
	changed := false
	for key, rawProp := range doc.Properties {
		v, ok := out[key]
		if !ok {
			continue
		}
		if converted, yes := coerceValue(v, rawProp); yes {
			out[key] = converted
			changed = true
		}
	}
	return out, changed
}

// coerceValue 按单个属性的 schema 对一个值做保守强转。第二个返回值表示是否发生改写。
// 只有明确的字符串→数字/布尔转换，以及明确的嵌套对象/数组递归才会发生改写。
func coerceValue(v any, rawSchema json.RawMessage) (any, bool) {
	var prop struct {
		Type       json.RawMessage            `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Items      json.RawMessage            `json:"items"`
	}
	if err := json.Unmarshal(rawSchema, &prop); err != nil {
		return nil, false
	}
	types := decodeTypeList(prop.Type)

	switch typed := v.(type) {
	case string:
		return coerceString(typed, types)
	case map[string]any:
		if len(prop.Properties) == 0 || !allowsType(types, "object") {
			return nil, false
		}
		nested := make(map[string]any, len(typed))
		for k, val := range typed {
			nested[k] = val
		}
		changed := false
		for key, rawProp := range prop.Properties {
			val, ok := nested[key]
			if !ok {
				continue
			}
			if converted, yes := coerceValue(val, rawProp); yes {
				nested[key] = converted
				changed = true
			}
		}
		if !changed {
			return nil, false
		}
		return nested, true
	case []any:
		if len(prop.Items) == 0 || !allowsType(types, "array") {
			return nil, false
		}
		out := make([]any, len(typed))
		changed := false
		for i, val := range typed {
			if converted, yes := coerceValue(val, prop.Items); yes {
				out[i] = converted
				changed = true
			} else {
				out[i] = val
			}
		}
		if !changed {
			return nil, false
		}
		return out, true
	}
	return nil, false
}

// decodeTypeList 把 schema 的 type 字段解析为类型名列表，兼容单个字符串和联合数组两种写法。
func decodeTypeList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if single == "" {
			return nil
		}
		return []string{single}
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		return many
	}
	return nil
}

// allowsType 报告 types 是否允许目标类型；未声明 type 的 schema 不额外限制。
func allowsType(types []string, want string) bool {
	if len(types) == 0 {
		return true
	}
	return containsType(types, want)
}

func containsType(types []string, want string) bool {
	for _, t := range types {
		if t == want {
			return true
		}
	}
	return false
}

// coerceString 仅在 schema 不含 string（否则字符串本身合法，强转即歧义）且存在
// 可无损转换的目标类型时改写字符串。布尔只认 JSON 字面量 true/false，
// 数字先校验是否为合法 JSON 数字字面量，避免把 CJK 数字或带符号的怪写法当数字。
// array/object 只接受能严格解析成对应 JSON 复合类型的字符串（pi 对 edits 参数的兼容）。
func coerceString(s string, types []string) (any, bool) {
	if len(types) == 0 || containsType(types, "string") {
		return nil, false
	}
	for _, t := range types {
		switch t {
		case "boolean":
			switch s {
			case "true":
				return true, true
			case "false":
				return false, true
			}
		case "integer", "number":
			if f, ok := parseCoercibleNumber(s, t == "integer"); ok {
				return f, true
			}
		case "array", "object":
			// pi 兼容：部分模型把声明为 array/object 的参数（如 edit 的 edits）
			// 发成 JSON 字符串。仅在 schema 明确该类型、且字符串能无损解析成
			// 对应类型时才改写；类型不符或非法 JSON 一律保留原样。
			if parsed, ok := parseCoercibleComposite(s, t); ok {
				return parsed, true
			}
		}
	}
	return nil, false
}

// parseCoercibleComposite 把字符串解析成目标复合类型（array/object），
// 要求解析结果的类型与 schema 声明严格一致，避免把对象塞进数组字段之类的误转。
func parseCoercibleComposite(s, want string) (any, bool) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return nil, false
	}
	var parsed any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil, false
	}
	switch want {
	case "array":
		if arr, ok := parsed.([]any); ok {
			return arr, true
		}
	case "object":
		if obj, ok := parsed.(map[string]any); ok {
			return obj, true
		}
	}
	return nil, false
}

// parseCoercibleNumber 先验证字符串是合法的 JSON 数字字面量，再按整数/浮点路径解析。
// 整数字面量必须落在 float64 的精确表示范围内；浮点路径拒绝 NaN 与无穷。
func parseCoercibleNumber(s string, integerOnly bool) (float64, bool) {
	if s == "" {
		return 0, false
	}
	var num json.Number
	if err := json.Unmarshal([]byte(s), &num); err != nil {
		return 0, false
	}
	if i, err := num.Int64(); err == nil {
		if i > coerceExactIntLimit || i < -coerceExactIntLimit {
			return 0, false
		}
		return float64(i), true
	}
	if integerOnly {
		return 0, false
	}
	f, err := num.Float64()
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// coerceToolArgumentsJSON 对原始 JSON 参数字符串做同样的保守强转。没有字段变化时
// 原样返回，确保开关打开但无需改写时字节完全不变。输出禁用 HTML 转义，使 CJK
// 与非 ASCII 文本保持原样。
func coerceToolArgumentsJSON(raw string, schema json.RawMessage) string {
	if len(schema) == 0 || strings.TrimSpace(raw) == "" {
		return raw
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil || args == nil {
		return raw
	}
	coerced, changed := coerceToolArgumentsChanged(args, schema)
	if !changed {
		return raw
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(coerced); err != nil {
		return raw
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// toolSchemaFor 返回本次运行向模型公布的、名为 name 的工具 schema；找不到该工具
// 或它没有参数文档时返回空值，调用方据此跳过强转。
func (s *runState) toolSchemaFor(name string) json.RawMessage {
	for i := range s.specs {
		if s.specs[i].Function.Name == name {
			return s.specs[i].Function.Parameters
		}
	}
	return nil
}
