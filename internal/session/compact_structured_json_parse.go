package session

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"unicode"
)

// 结构化摘要 JSON 的字段别名。键名先经 normalizeSummaryFieldKey 归一化，再按
// 下列顺序匹配；顺序即优先级，同一桶内所有命中的别名都会被合并（去重且保序）。
// 这样既能容纳 LLM 常见的 snake_case / camelCase 变体，也能吃下已有的规范键，
// 以及以简体中文书写的字段名。
var (
	summaryProgressAliases = []string{
		"progress", "userintent", "intent", "goals", "goal", "completed",
		"completeditems", "workdone", "workcompleted", "achievements",
		"changes", "fileschanged", "filesmodified", "actionstaken",
		"modifications",
		"进展", "进度", "已完成", "完成事项", "工作进展", "目标", "改动", "修改文件",
	}
	summaryUncertaintiesAliases = []string{
		"uncertainties", "uncertainty", "errorsandfixes", "errors", "error",
		"failures", "failure", "issues", "problems", "unknowns", "concerns",
		"blockers", "openquestions", "risks", "warnings",
		"不确定性", "不确定事项", "不确定", "失败", "错误", "问题", "阻碍", "风险", "待确认",
	}
	summaryUserActionsAliases = []string{
		"useractions", "useraction", "nextstep", "nextsteps", "actions",
		"actionitems", "todos", "todo", "followups", "followup",
		"recommendations", "suggestedactions", "suggestions",
		"用户操作", "用户行动", "下一步", "后续", "待办", "建议",
	}
	// 当桶的值是对象而非字符串数组时，用这些键提取其中的文本。
	summaryItemTextAliases = []string{
		"text", "content", "description", "item", "message", "title",
		"action", "step", "detail", "name", "value",
		"文本", "内容", "描述", "标题", "步骤",
	}
)

// parseStructuredSummaryJSON 是从自由文本中宽容提取结构化摘要 JSON 的纯函数。
//
// 容错策略（按尝试顺序）：
//  1. 整段文本本身就是一个 JSON 对象；
//  2. ```json 代码围栏中的内容；
//  3. 全文中按大括号平衡切分出的所有对象（含嵌套对象，作为多候选兜底）。
//
// 字段通过别名表宽容映射（如 user_intent→progress、errors_and_fixes→uncertainties、
// next_step→userActions），值支持字符串数组、单个字符串以及带文本字段的对象数组。
// 解析失败或三个桶都为空时返回 false，调用方应回退到原始自由文本。
//
// 该函数不修改任何全局状态，也不触碰 Compaction.Summary；CJK 内容按原样保留。
func parseStructuredSummaryJSON(raw string) (StructuredSummary, bool) {
	for _, candidate := range extractSummaryJSONCandidates(raw) {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(candidate), &obj); err != nil || len(obj) == 0 {
			continue
		}
		normalized := normalizeSummaryFieldObject(obj)
		out := StructuredSummary{
			Progress:      collectSummaryBucket(normalized, summaryProgressAliases),
			Uncertainties: collectSummaryBucket(normalized, summaryUncertaintiesAliases),
			UserActions:   collectSummaryBucket(normalized, summaryUserActionsAliases),
		}
		if summaryIsEmpty(out) {
			continue
		}
		return out, true
	}
	return StructuredSummary{}, false
}

// ParseStructuredSummaryLenient 是 ParseStructuredSummary 的显式 opt-in 宽容版本。
//
// 它先尝试从文本中提取并宽容解析 JSON 结构化摘要（多候选 + 字段别名 + 原文回退），
// 失败时再回退到 ParseStructuredSummary 的 markdown 标题解析。这是默认关闭的接线：
// 现有调用方（Compaction.Structured、Compaction.StructuredJSON）仍使用
// ParseStructuredSummary，行为逐字节不变；只有显式调用本函数才启用 JSON 路径。
func ParseStructuredSummaryLenient(text string) (StructuredSummary, bool) {
	if out, ok := parseStructuredSummaryJSON(text); ok {
		return out, true
	}
	return ParseStructuredSummary(text)
}

// extractSummaryJSONCandidates 按优先级收集可能是 JSON 对象的文本片段。
// 候选去重，顺序即尝试顺序；围栏内容优先于散落在散文中的片段。
func extractSummaryJSONCandidates(raw string) []string {
	var out []string
	seen := make(map[string]bool)
	add := func(s string) {
		s = strings.TrimPrefix(strings.TrimSpace(s), "\ufeff")
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}

	add(raw)
	for _, fenced := range summaryFencedBlocks(raw) {
		for _, obj := range summaryBalancedObjects(fenced) {
			add(obj)
		}
	}
	for _, obj := range summaryBalancedObjects(raw) {
		add(obj)
	}
	return out
}

// summaryFencedBlocks 提取 Markdown 代码围栏（``` 或 ```json 等）中的内容。
// 按行扫描，忽略围栏标记行两侧的空白与回车符。
func summaryFencedBlocks(raw string) []string {
	var out []string
	var buf strings.Builder
	inFence := false
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if !inFence {
			if strings.HasPrefix(trimmed, "```") {
				inFence = true
				buf.Reset()
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			out = append(out, buf.String())
			inFence = false
			continue
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	return out
}

// summaryBalancedObjects 扫描 s，返回所有大括号平衡的 {...} 子串。
// 扫描器理解 JSON 字符串与反斜杠转义，字符串里的大括号不会被误认为结构边界。
// 按 '{' 出现顺序返回，因此外层对象先于其嵌套对象，符合"先整体后局部"的直觉。
// 逐字节扫描对 UTF-8 安全：多字节字符的续字节均大于 0x80，不会匹配这里的 ASCII 分隔符。
func summaryBalancedObjects(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		if end, ok := summaryMatchingBrace(s, i); ok {
			out = append(out, s[i:end+1])
		}
	}
	return out
}

// summaryMatchingBrace 返回从 start 处的 '{' 开始、与之配对的 '}' 的下标。
func summaryMatchingBrace(s string, start int) (int, bool) {
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch c {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// normalizeSummaryFieldKey 归一化 JSON 字段名：忽略大小写、下划线、连字符与空格，
// 只保留字母和数字（含 CJK）。例如 user_intent、user-intent 与 UserIntent 都会
// 归一化为 userintent。
func normalizeSummaryFieldKey(key string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(key) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// normalizeSummaryFieldObject 把对象的键归一化，并按原键排序后保留首次出现的值，
// 使别名匹配具备确定性的优先级，不受 map 遍历随机性影响。
func normalizeSummaryFieldObject(obj map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(obj))
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		nk := normalizeSummaryFieldKey(k)
		if nk == "" {
			continue
		}
		if _, exists := out[nk]; !exists {
			out[nk] = obj[k]
		}
	}
	return out
}

// collectSummaryBucket 按别名顺序合并一个桶的所有字符串条目，去重且保序。
func collectSummaryBucket(obj map[string]json.RawMessage, aliases []string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, alias := range aliases {
		raw, ok := obj[alias]
		if !ok {
			continue
		}
		for _, item := range summaryStringItems(raw) {
			item = strings.TrimSpace(item)
			if item == "" || isSummaryNoneMarker(item) || seen[item] {
				continue
			}
			seen[item] = true
			out = append(out, item)
		}
	}
	return out
}

// summaryStringItems 把一个 JSON 字段值宽容地转成字符串条目列表。
// 支持字符串数组、单个字符串，以及带文本字段的对象；对象没有常见文本字段时，
// 按归一化键名顺序平铺其中的字符串值作为兜底。其余形态（数字、布尔、null）被跳过，
// 不会让整次解析失败。
func summaryStringItems(raw json.RawMessage) []string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}

	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil {
		var out []string
		for _, elem := range arr {
			out = append(out, summaryStringItems(elem)...)
		}
		return out
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []string{s}
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	normalized := normalizeSummaryFieldObject(obj)
	for _, alias := range summaryItemTextAliases {
		if v, ok := normalized[alias]; ok {
			if got := summaryStringItems(v); len(got) > 0 {
				return got
			}
		}
	}
	keys := make([]string, 0, len(normalized))
	for k := range normalized {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		out = append(out, summaryStringItems(normalized[k])...)
	}
	return out
}
