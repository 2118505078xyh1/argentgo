package tools

import (
	"bytes"
	"encoding/json"
)

// 本文件实现 omp G1 描述的"简洁意图"（`i`）机制。
// 模型被要求在每个工具调用中把 `i` 字符串作为第一个属性；
// agent 在工具看到参数之前剥离它，并把它
// 保留在结果事件上，以便事后解释冗长或格式错误的调用。
// 这些辅助函数是纯函数，刻意与 schema
// 生成分开：注册表（不在本文件的职责范围内）决定何时、
// 是否公布这个额外属性。

const (
	intentKey = "i"
	// intentSchemaValue 是为 `i` 公布的 JSON Schema 片段。
	intentSchemaValue = `{"type":"string","description":"concise intent for this call"}`
)

// orderedField 是 JSON 对象的一个键值对。encoding/json 的 map
// 会丢失键顺序，因此对象改为解码为有序切片：
// 调用方需要 `i` 作为*第一个*属性，同时每个其他字段保持
// 原有位置和原始编码。
type orderedField struct {
	key   string
	value json.RawMessage
}

// decodeObject 把 raw 解析为顶层字段的有序列表。对无效 JSON 或
// 任何非 JSON 对象的值返回 false。
func decodeObject(raw json.RawMessage) ([]orderedField, bool) {
	if !json.Valid(raw) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, false
	}
	var fields []orderedField
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, false
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, false
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, false
		}
		fields = append(fields, orderedField{key: key, value: value})
	}
	if _, err := dec.Token(); err != nil { // closing '}'
		return nil, false
	}
	return fields, true
}

// encodeObject 从有序字段重建 JSON 对象，同时保留
// 它们的顺序和每个值的原始字节。
func encodeObject(fields []orderedField) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(f.key)
		b.Write(key)
		b.WriteByte(':')
		b.Write(f.value)
	}
	b.WriteByte('}')
	return json.RawMessage(b.Bytes())
}

// field 返回名为 key 的第一个顶层键的原始值。
func field(fields []orderedField, key string) (json.RawMessage, bool) {
	for _, f := range fields {
		if f.key == key {
			return f.value, true
		}
	}
	return nil, false
}

// InjectIntentSchema 给工具的参数 schema 添加一个 `i` 属性。该
// 属性插入到 "properties" 内部的最前面，并把 `i` 追加到
// 顶层的 "required" 数组（不存在时创建）。其他每个字段保持
// 原有位置和原始编码。
//
// 该函数是幂等的：如果 `i` 已经是属性，则原样返回 params。
// 非对象和无效 JSON 也原样返回。
func InjectIntentSchema(params json.RawMessage) json.RawMessage {
	fields, ok := decodeObject(params)
	if !ok {
		return params
	}

	// 幂等性：已经公布 `i` 的 schema 保持不动。
	propsRaw, hasProps := field(fields, "properties")
	if hasProps {
		props, ok := decodeObject(propsRaw)
		if !ok {
			return params
		}
		if _, exists := field(props, intentKey); exists {
			return params
		}
	}

	// 构建新的 "properties" 对象，把 `i` 放在最前面，保留每个现有
	// 属性在其原有顺序中位于其后。
	intentProp := orderedField{key: intentKey, value: json.RawMessage(intentSchemaValue)}
	var newProps []orderedField
	newProps = append(newProps, intentProp)
	if hasProps {
		if existing, ok := decodeObject(propsRaw); ok {
			newProps = append(newProps, existing...)
		}
	}
	propsValue := encodeObject(newProps)

	// 把 `i` 加入 "required"，保留现有条目。格式错误的
	// 格式错误的 "required" 保持原样，而不是被悄悄重写。
	requiredRaw, hasRequired := field(fields, "required")
	var requiredValue json.RawMessage
	if hasRequired {
		var names []string
		if err := json.Unmarshal(requiredRaw, &names); err != nil {
			return params
		}
		already := false
		for _, name := range names {
			if name == intentKey {
				already = true
				break
			}
		}
		if !already {
			names = append(names, intentKey)
		}
		requiredValue, _ = json.Marshal(names)
	} else {
		requiredValue = json.RawMessage(`["i"]`)
	}

	// 就地替换（或追加）两个受影响的顶层字段。
	out := make([]orderedField, 0, len(fields)+2)
	propsWritten, requiredWritten := false, false
	for _, f := range fields {
		switch f.key {
		case "properties":
			out = append(out, orderedField{key: "properties", value: propsValue})
			propsWritten = true
		case "required":
			out = append(out, orderedField{key: "required", value: requiredValue})
			requiredWritten = true
		default:
			out = append(out, f)
		}
	}
	if !propsWritten {
		out = append(out, orderedField{key: "properties", value: propsValue})
	}
	if !requiredWritten {
		out = append(out, orderedField{key: "required", value: requiredValue})
	}
	return encodeObject(out)
}
