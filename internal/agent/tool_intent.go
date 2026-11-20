package agent

import (
	"bytes"
	"encoding/json"
)

// 本文件实现 omp G1 描述的"简洁意图"（`i`）管线。模型被要求在每个工具调用中
// 把 `i` 字符串作为第一个属性；agent 在工具看到参数之前将其剥离，
// 并把它保留在生成的事件上，以便事后解释冗长或畸形的调用。
// 这些辅助函数是纯函数，且刻意不参与 schema 生成：是否、何时公布该额外属性，
// 由注册表（不在本文件职责范围内）决定。

const intentKey = "i"

// orderedField 是 JSON 对象的一个键值对。encoding/json 的 map 会丢失键顺序，
// 因此改为把对象解码为有序切片：调用方需要 `i` 作为*第一个*属性，
// 同时其他每个字段保持其原始位置和原始编码。
type orderedField struct {
	key   string
	value json.RawMessage
}

// decodeObject 把 raw 解析为顶层字段的有序列表。对无效 JSON 或任何非 JSON 对象的值返回 false。
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
	if _, err := dec.Token(); err != nil { // 结束的 '}'
		return nil, false
	}
	return fields, true
}

// encodeObject 从有序字段重建 JSON 对象，保留其顺序以及每个值的原始字节。
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

// ExtractIntent 从工具调用参数对象中移除 `i` 键，并返回其字符串值。
// 清理后的参数是工具实际收到的内容。没有 `i` 或输入不是对象时，args 会逐字节原样返回。
// 无效 JSON 的处理方式相同。
func ExtractIntent(args json.RawMessage) (intent string, cleaned json.RawMessage) {
	fields, ok := decodeObject(args)
	if !ok {
		return "", args
	}

	out := make([]orderedField, 0, len(fields))
	found := false
	for _, f := range fields {
		if f.key == intentKey {
			found = true
			// 非字符串的 `i` 不携带可用意图，但仍会被剥离，
			// 使工具永远看不到这个内部字段。
			var s string
			if err := json.Unmarshal(f.value, &s); err == nil {
				intent = s
			}
			continue
		}
		out = append(out, f)
	}
	if !found {
		return "", args
	}
	return intent, encodeObject(out)
}
