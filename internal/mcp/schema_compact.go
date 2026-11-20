package mcp

import (
	"bytes"
	"encoding/json"
)

// 分级有损压缩的力度参数。它们只影响压缩多少，不影响输出的合法性。
const (
	// compactMaxDepth 是对象/数组允许保留的最大嵌套深度；比它更深的
	// 复合子树会被折叠为最小的类型占位符。
	compactMaxDepth = 5
	// compactCompositionBranches 是每个 anyOf / oneOf / allOf 保留的
	// 分支数上限；靠前的分支优先，因为 JSON Schema 中分支顺序有意义。
	compactCompositionBranches = 2
)

// compactSchema 在 inputSchema 超过 maxBytes 时按四级逐步有损压缩，
// 一旦输出达标就停止；四级都用尽仍超限时返回最紧凑的尽力结果。
//
// 级别顺序（与 codex 工具系统审计建议一致）：
//
//  1. 递归去掉 description；
//  2. 递归去掉 $defs / $definitions；
//  3. 折叠超过 compactMaxDepth 的深层对象/数组；
//  4. 把 anyOf / oneOf / allOf 截断到 compactCompositionBranches 个分支。
//
// 保证：
//   - 未超限（len(schema) <= maxBytes）时逐字节原样返回；
//   - 非法 JSON 原样返回，绝不猜测如何修复；
//   - 输出总是合法 JSON；同一输入总是得到同一输出（encoding/json
//     对对象键排序，且用 json.Number 保留数字字面量避免精度漂移）；
//   - 全程不切字节，CJK 等多字节 UTF-8 安全。
//
// maxBytes <= 0 会让任何非空 schema 触发压缩（尽力而为，不保证结果
// 一定小于 maxBytes）。调用方若想关闭压缩，传入 0 并不要调用本函数，
// 或直接使用 normalizeInputSchema 的结果。
func compactSchema(schema json.RawMessage, maxBytes int) json.RawMessage {
	if len(schema) <= maxBytes {
		return schema
	}
	if !json.Valid(schema) {
		return schema
	}

	// UseNumber 让大整数等数字保持原始字面量，避免 float64 往返时被
	// 改写成科学计数法或丢失精度。
	dec := json.NewDecoder(bytes.NewReader(schema))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return schema
	}

	// 四级叠加，每级后检查体积；达标即提前返回。
	levels := []func(){
		func() { dropDescriptions(root) },
		func() { dropDefs(root) },
		func() { root = collapseDeepObjects(root, 0) },
		func() { trimComposition(root, compactCompositionBranches) },
	}
	var out []byte
	for _, level := range levels {
		level()
		candidate, err := json.Marshal(root)
		if err != nil {
			// root 来自合法 JSON，理论上不会失败；万一失败，宁可
			// 返回原始 schema，也不返回半个 JSON。
			return schema
		}
		out = candidate
		if len(out) <= maxBytes {
			return out
		}
	}
	return out // 所有级别都用尽仍超限：返回最紧凑的尽力结果
}

// dropDescriptions 递归删除 description 成员。
func dropDescriptions(v any) {
	switch node := v.(type) {
	case map[string]any:
		delete(node, "description")
		for _, child := range node {
			dropDescriptions(child)
		}
	case []any:
		for _, child := range node {
			dropDescriptions(child)
		}
	}
}

// dropDefs 递归删除 $defs / $definitions 成员。$definitions 是较老的
// draft 拼写，一并处理，避免残留体积最大的引用表。
func dropDefs(v any) {
	switch node := v.(type) {
	case map[string]any:
		delete(node, "$defs")
		delete(node, "$definitions")
		for _, child := range node {
			dropDefs(child)
		}
	case []any:
		for _, child := range node {
			dropDefs(child)
		}
	}
}

// collapseDeepObjects 把深度超过 compactMaxDepth 的复合节点折叠成最小
// 的占位符：对象只保留 type（缺省为 "object"），数组变为空数组。标量
// 原样保留，不受深度影响。depth 从根节点 0 开始逐层递增。
func collapseDeepObjects(v any, depth int) any {
	switch node := v.(type) {
	case map[string]any:
		if depth > compactMaxDepth {
			return typePlaceholder(node)
		}
		for key, child := range node {
			node[key] = collapseDeepObjects(child, depth+1)
		}
	case []any:
		if depth > compactMaxDepth {
			return []any{}
		}
		for i, child := range node {
			node[i] = collapseDeepObjects(child, depth+1)
		}
	}
	return v
}

// typePlaceholder 从对象中只保留 type 成员，作为深层折叠后的占位符。
func typePlaceholder(node map[string]any) map[string]any {
	if typ, ok := node["type"]; ok {
		return map[string]any{"type": typ}
	}
	return map[string]any{"type": "object"}
}

// trimComposition 把 anyOf / oneOf / allOf 截断到 limit 个分支。
// 截尾而非随机取样，保证结果确定且保留作者认为更重要的靠前分支。
func trimComposition(v any, limit int) {
	switch node := v.(type) {
	case map[string]any:
		for _, key := range []string{"anyOf", "oneOf", "allOf"} {
			if arr, ok := node[key].([]any); ok && len(arr) > limit {
				node[key] = append([]any(nil), arr[:limit]...)
			}
		}
		for _, child := range node {
			trimComposition(child, limit)
		}
	case []any:
		for _, child := range node {
			trimComposition(child, limit)
		}
	}
}
