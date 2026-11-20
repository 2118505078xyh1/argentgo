package mcp

import (
	"encoding/json"
	"sort"
	"strings"
)

// toolSearchTextMaxBytes 是工具检索文本允许占用的最大字节数。
// 检索文本会参与排序与匹配，过长会拖慢搜索并浪费内存，因此需要设置上界。
const toolSearchTextMaxBytes = 2048

// toolSearchText 为一个工具构造用于检索的纯文本。它依次纳入：
//   - 工具名；
//   - 工具名的下划线替换形态（以下划线分割的词改用空格连接，
//     使用户用 "web fetch" 这类空格查询也能命中 web_fetch）；
//   - 工具描述；
//   - schema 中 properties 的每个键名（参数名）及其描述。
//
// 片段按上述固定顺序拼接，schema 属性键按字典序排列，因此结果是稳定有序的；
// 内容相同的片段（忽略大小写）只保留首次出现，实现去重。整段文本最终按
// UTF-8 rune 边界截断到不超过 toolSearchTextMaxBytes 字节，故不会切断 CJK
// 等多字节字符。缺失、非法或结构不符的 schema 会被安全忽略，只保留名字与描述。
//
// 该函数不依赖任何外部状态，相同的入参永远返回相同的结果。
func toolSearchText(name, description string, schema json.RawMessage) string {
	fragments := make([]string, 0, 8)
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" {
			fragments = append(fragments, s)
		}
	}

	trimmedName := strings.TrimSpace(name)
	add(trimmedName)
	add(strings.ReplaceAll(trimmedName, "_", " "))
	add(description)
	fragments = append(fragments, schemaSearchFragments(schema)...)

	seen := make(map[string]bool, len(fragments))
	parts := make([]string, 0, len(fragments))
	for _, fragment := range fragments {
		key := strings.ToLower(fragment)
		if seen[key] {
			continue
		}
		seen[key] = true
		parts = append(parts, fragment)
	}

	// 复用 truncateToolName：它按 rune 边界做字节截断，天然保证多字节字符安全。
	return truncateToolName(strings.Join(parts, " "), toolSearchTextMaxBytes)
}

// schemaSearchFragments 从 inputSchema 中提取 properties 的键名与每项描述。
// 属性键按字典序返回以保证稳定顺序；非法 JSON、非对象 properties、非字符串
// 描述都会被跳过，不会因为 schema 格式异常而污染或中断检索文本的生成。
func schemaSearchFragments(schema json.RawMessage) []string {
	if len(schema) == 0 {
		return nil
	}
	var root struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &root); err != nil {
		return nil
	}

	keys := make([]string, 0, len(root.Properties))
	for key := range root.Properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make([]string, 0, len(keys)*2)
	for _, key := range keys {
		if trimmed := strings.TrimSpace(key); trimmed != "" {
			out = append(out, trimmed)
		}
		var property struct {
			Description string `json:"description"`
		}
		if err := json.Unmarshal(root.Properties[key], &property); err != nil {
			continue
		}
		if text := strings.TrimSpace(property.Description); text != "" {
			out = append(out, text)
		}
	}
	return out
}
