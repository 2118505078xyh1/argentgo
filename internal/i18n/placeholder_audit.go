package i18n

import (
	"fmt"
	"sort"
)

// 占位符审计来源表的稳定标识，写入 PlaceholderIssue.Table 供调用方区分。
const (
	placeholderTableCatalog       = "catalog"
	placeholderTableErrorMessages = "error messages"
)

// PlaceholderIssue 描述同一条文案在 en-US 与 zh-CN 之间占位符标记的不一致。
// 两个方向以已排序的切片给出：MissingInEn 中的标记由 zh-CN 提供而 en-US 缺失
// （或数量不足），MissingInZh 反之。标记形如 "%s"、"{{name}}"、"{0}"，
// 出现多次时用 "NAME*N" 描述期望数量，因此既涵盖集合差异也涵盖数量差异。
type PlaceholderIssue struct {
	Key         string   // 文案条目键：catalog key 或错误码
	Table       string   // 来源表：placeholderTableCatalog / placeholderTableErrorMessages
	MissingInEn []string // zh-CN 有而 en-US 缺失或数量不足的标记
	MissingInZh []string // en-US 有而 zh-CN 缺失或数量不足的标记
}

// Problems 以稳定顺序展开为可读的问题描述，供 doctor 命令或测试输出。
func (p PlaceholderIssue) Problems() []string {
	out := make([]string, 0, len(p.MissingInEn)+len(p.MissingInZh))
	for _, marker := range p.MissingInEn {
		out = append(out, p.Table+" key="+p.Key+" en-US missing placeholder: "+marker)
	}
	for _, marker := range p.MissingInZh {
		out = append(out, p.Table+" key="+p.Key+" zh-CN missing placeholder: "+marker)
	}
	return out
}

// isASCIIDigit 报告 c 是否为 ASCII 数字。
func isASCIIDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

// braceMarkerAt 解析从 s[start]=='{' 开始的单花括号占位符。
// 内容必须是完整的 ASCII 标识符或十进制数字并以 '}' 结束，
// 因此 "{name}" 与 "{0}" 会被识别，而 "{a-b}"、"{" 则不会。
// 返回的 marker 含两端花括号，end 为刚越过的索引。
func braceMarkerAt(s string, start int) (marker string, end int, ok bool) {
	i := start + 1
	if i >= len(s) {
		return "", 0, false
	}
	switch {
	case placeholderNameStart(s[i]):
		for i < len(s) && placeholderNameByte(s[i]) {
			i++
		}
	case isASCIIDigit(s[i]):
		for i < len(s) && (isASCIIDigit(s[i]) || s[i] == '_') {
			i++
		}
	default:
		return "", 0, false
	}
	if i >= len(s) || s[i] != '}' {
		return "", 0, false
	}
	return s[start : i+1], i + 1, true
}

// doubleBraceMarkerAt 解析从 s[start]=='{' 且 s[start+1]=='{' 开始的
// 双花括号命名占位符，例如 "{{name}}"。单独成对的 "{{" / "}}" 不是占位符。
func doubleBraceMarkerAt(s string, start int) (marker string, end int, ok bool) {
	i := start + 2
	if i >= len(s) {
		return "", 0, false
	}
	switch {
	case placeholderNameStart(s[i]):
		for i < len(s) && placeholderNameByte(s[i]) {
			i++
		}
	case isASCIIDigit(s[i]):
		for i < len(s) && (isASCIIDigit(s[i]) || s[i] == '_') {
			i++
		}
	default:
		return "", 0, false
	}
	if i+1 >= len(s) || s[i] != '}' || s[i+1] != '}' {
		return "", 0, false
	}
	return s[start : i+2], i + 2, true
}

// placeholderMarkers 从文案中提取占位符标记的多重集，识别三类语法：
//
//   - fmt 动词：%s、%d、%[2]s 等，统一规范化为 "%" + 动词字母；"%%"
//     是字面百分号，不计入；畸形或末尾的 '%' 被忽略。
//   - 双花括号命名占位符：{{name}}。
//   - 单花括号占位符：{name}、{0}。
//
// 双花括号整体消费，避免把 {{name}} 再重复计为内部的 {name}。
// 多字节（CJK）文本逐字节跳过，不会被误识别为标记。
func placeholderMarkers(s string) map[string]int {
	out := map[string]int{}
	for i := 0; i < len(s); {
		switch s[i] {
		case '%':
			if i+1 < len(s) && s[i+1] == '%' {
				i += 2
				continue
			}
			j := i + 1
			for j < len(s) && isFmtDirectiveByte(s[j]) {
				j++
			}
			if j < len(s) && isASCIILetter(s[j]) {
				out["%"+string(s[j])]++
				i = j + 1
				continue
			}
			i++
		case '{':
			if i+1 < len(s) && s[i+1] == '{' {
				if marker, end, ok := doubleBraceMarkerAt(s, i); ok {
					out[marker]++
					i = end
					continue
				}
				i += 2
				continue
			}
			if marker, end, ok := braceMarkerAt(s, i); ok {
				out[marker]++
				i = end
				continue
			}
			i++
		default:
			i++
		}
	}
	return out
}

// describeMarker 渲染带计数的标记：出现一次只写标记本身，
// 多次写 "标记*次数"，与 describeCounts 的写法保持一致。
func describeMarker(marker string, count int) string {
	if count <= 1 {
		return marker
	}
	return fmt.Sprintf("%s*%d", marker, count)
}

// diffMarkers 比较两个占位符多重集，返回两个方向各自"不足"的标记。
// 数量以较多一侧为期望值，因此同时涵盖缺失与数量不足两种情况。
func diffMarkers(en, zh map[string]int) (missingInEn, missingInZh []string) {
	missingInEn = make([]string, 0)
	missingInZh = make([]string, 0)
	for marker, zhCount := range zh {
		if en[marker] < zhCount {
			missingInEn = append(missingInEn, describeMarker(marker, zhCount))
		}
	}
	for marker, enCount := range en {
		if zh[marker] < enCount {
			missingInZh = append(missingInZh, describeMarker(marker, enCount))
		}
	}
	sort.Strings(missingInEn)
	sort.Strings(missingInZh)
	return missingInEn, missingInZh
}

// checkPlaceholderTable 对一份 en/zh 文案表逐键比较占位符。
// 只有两种语言文本都非空的条目才参与比较：单边空文本属于文本缺失，
// 由 AuditTranslations / 键对齐审计负责，这里不重复报告。
func checkPlaceholderTable(table map[string]localeTexts, name string) []PlaceholderIssue {
	out := make([]PlaceholderIssue, 0)
	for key, text := range table {
		if text.en == "" || text.zh == "" {
			continue
		}
		missingInEn, missingInZh := diffMarkers(placeholderMarkers(text.en), placeholderMarkers(text.zh))
		if len(missingInEn) == 0 && len(missingInZh) == 0 {
			continue
		}
		out = append(out, PlaceholderIssue{
			Key:         key,
			Table:       name,
			MissingInEn: missingInEn,
			MissingInZh: missingInZh,
		})
	}
	return out
}

// placeholderTable 是带稳定名称的 en/zh 文案表视图，供审计与测试复用。
type placeholderTable struct {
	name    string
	entries map[string]localeTexts
}

// auditPlaceholderTables 对任意数量的文案表做占位符审计并按
// Table、Key 稳定排序。它是纯函数，测试借此用模拟表验证排序与不一致报告。
func auditPlaceholderTables(tables ...placeholderTable) []PlaceholderIssue {
	issues := make([]PlaceholderIssue, 0)
	for _, table := range tables {
		issues = append(issues, checkPlaceholderTable(table.entries, table.name)...)
	}
	sort.SliceStable(issues, func(i, j int) bool {
		a, b := issues[i], issues[j]
		if a.Table != b.Table {
			return a.Table < b.Table
		}
		return a.Key < b.Key
	})
	return issues
}

// AuditPlaceholders 扫描主文案表（catalog）与错误码表（errorMessageTable），
// 比较每条 en/zh 文案的占位符标记是否一致，并以稳定顺序报告不一致的条目。
//
// 它是纯函数：只读取嵌入的文案表，不修改 T / errorMessage 的查找行为，
// 不改任何文案字节，也不向 Missing 记录任何内容。
func AuditPlaceholders() []PlaceholderIssue {
	return auditPlaceholderTables(
		placeholderTable{name: placeholderTableCatalog, entries: catalogView()},
		placeholderTable{name: placeholderTableErrorMessages, entries: errorMessagesView()},
	)
}
