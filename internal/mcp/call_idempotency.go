package mcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// callIdempotencyKeyHexLen 是幂等键使用的 sha256 摘要长度（十六进制字符数）。
// 16 个字符提供 64 位区分度，足以覆盖单次会话内的工具调用数量。
const callIdempotencyKeyHexLen = 16

// invalidParamsSentinel 是非法参数的规范化占位符。它以原始 NUL 字节开头，
// 合法 JSON 序列化后绝不会产生这种字节序列（NUL 在 JSON 中只能是 \u0000 转义），
// 因此不会与任何合法参数的规范化结果相撞。
const invalidParamsSentinel = "\x00<invalid-params>"

// callIdempotencyKey 为一次工具调用生成稳定的幂等键。
//
// 它把方法名与规范化后的参数拼成摘要输入，取 sha256 的前
// callIdempotencyKeyHexLen 个十六进制字符。参数规范化会递归地把对象键按字典序
// 排列（encoding/json 对 map 键天然排序），数组则保持原顺序，数字保留原始字面量
// 以避免浮点舍入带来的不确定性。于是仅对象键顺序不同的两次调用会得到同一个键；
// 而方法名、数组顺序或任何取值的差异都会产生不同的键。
//
// 方法名与规范化参数之间用 NUL 分隔，避免 ("ab", "c") 与 ("a", "bc") 这类边界
// 拼接产生相同的摘要输入。空参数（nil、空切片或纯空白）统一映射到 "null"，
// 非法的 JSON 参数映射到 invalidParamsSentinel，二者都保持稳定且不互相混淆。
//
// 该函数不依赖任何外部状态，相同的入参永远返回相同的结果，可安全并发调用。
func callIdempotencyKey(method string, params json.RawMessage) string {
	canonical := canonicalCallParams(params)
	sum := sha256.Sum256([]byte(method + "\x00" + canonical))
	return hex.EncodeToString(sum[:])[:callIdempotencyKeyHexLen]
}

// canonicalCallParams 把调用参数规范化为确定性的字符串。
//
// 空参数返回 "null"；非合法 JSON 返回 invalidParamsSentinel。合法 JSON 先用
// UseNumber 解码，使数字保留原始字面量，再重新序列化——encoding/json 会按键
// 排序 map、把 \uXXXX 转义归一为对应的 UTF-8 字符，因此中英文等 CJK 内容既不会
// 被破坏，也不会因转义写法不同而改变结果。
func canonicalCallParams(params json.RawMessage) string {
	trimmed := bytes.TrimSpace(params)
	if len(trimmed) == 0 {
		return "null"
	}
	// json.Valid 要求整段输入恰好是一个 JSON 值（仅允许首尾空白），
	// 这样可排除截断或附带多余 token 的非法输入。
	if !json.Valid(trimmed) {
		return invalidParamsSentinel
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return invalidParamsSentinel
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return invalidParamsSentinel
	}
	return string(raw)
}
