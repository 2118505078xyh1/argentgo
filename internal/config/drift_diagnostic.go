package config

import (
	"fmt"
	"sort"
	"strings"
)

// driftOK 是配置漂移诊断无差异时返回的固定摘要字面量。
const driftOK = "配置漂移: 无"

// driftSeverityLowMax 是 low 级别的差异数量上限：1..driftSeverityLowMax 条
// 普通差异判为 low，超过则判为 medium；high 只由敏感键触发（见 DriftSeverity）。
const driftSeverityLowMax = 2

// driftNewlineReplacer 把 Key 中内嵌的换行压成单个空格，保证摘要"一项一行"。
// 它按完整 UTF-8 字节序列匹配换行符，不会切坏文本里的 CJK 字符。
var driftNewlineReplacer = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ")

// driftKindLabels 把差异类别映射为中文摘要标签。未知类别回退为原文，
// 保证新增类别时摘要仍可渲染。
var driftKindLabels = map[string]string{
	"added":   "新增",
	"removed": "删除",
	"changed": "变更",
}

// DriftEntry 是配置漂移诊断发现的一处差异。
//
// Key 是发生差异的配置键；Before 是变更前的值，After 是变更后的值。
// Kind 取 "added"（仅出现在变更后）、"removed"（仅出现在变更前）或
// "changed"（两侧都有但值不同）。新增项的 Before 为空，删除项的 After 为空。
type DriftEntry struct {
	Key    string
	Before string
	After  string
	Kind   string
}

// DiagnoseDrift 按 key 比较前后两份快照，聚合出所有差异。
//
// 它是一个纯函数：只读取 before/after，不修改入参、不访问全局状态；相同输入
// 总是产生相同输出。对每个键的判断规则：
//   - 两侧都有且值相等：不是差异，跳过；
//   - 两侧都有但值不等：Kind = "changed"；
//   - 仅在 before：Kind = "removed"，After 为空；
//   - 仅在 after：Kind = "added"，Before 为空。
//
// 结果按 Key 字典序稳定排序（UTF-8 字节序，CJK 键安全）；无差异时返回 nil。
// 该函数默认没有调用点，仅作为 doctor 等诊断入口按需使用的基础原语。
func DiagnoseDrift(before, after map[string]string) []DriftEntry {
	// 先收集所有出现在任一侧的键；键来自 map，天然唯一，无需去重。
	keys := make([]string, 0, len(before)+len(after))
	for k := range before {
		keys = append(keys, k)
	}
	for k := range after {
		if _, exists := before[k]; !exists {
			keys = append(keys, k)
		}
	}

	var out []DriftEntry
	for _, k := range keys {
		beforeValue, inBefore := before[k]
		afterValue, inAfter := after[k]
		switch {
		case inBefore && inAfter:
			if beforeValue != afterValue {
				out = append(out, DriftEntry{Key: k, Before: beforeValue, After: afterValue, Kind: "changed"})
			}
		case inBefore:
			out = append(out, DriftEntry{Key: k, Before: beforeValue, Kind: "removed"})
		default:
			out = append(out, DriftEntry{Key: k, After: afterValue, Kind: "added"})
		}
	}
	return sortDriftEntries(out)
}

// DriftSeverity 根据差异条目与敏感键表给出风险级别："high"/"medium"/"low"/"none"。
//
// 判定优先级：
//   - 任一条目的 Key 命中 sensitiveKeys（值为 true）时返回 "high"，与数量无关；
//   - 否则按差异数量分级：0 条为 "none"，1..driftSeverityLowMax 条为 "low"，
//     超过则为 "medium"。
//
// 它是一个纯函数：只读取两个入参，不修改、不缓存；sensitiveKeys 为 nil 时
// 视为没有敏感键，仅按数量分级。
func DriftSeverity(entries []DriftEntry, sensitiveKeys map[string]bool) string {
	if len(entries) == 0 {
		return "none"
	}
	for _, e := range entries {
		if sensitiveKeys[e.Key] {
			return "high"
		}
	}
	if len(entries) <= driftSeverityLowMax {
		return "low"
	}
	return "medium"
}

// DriftSummary 把差异条目渲染为稳定的中文摘要，供 doctor 等诊断入口直接展示。
//
// 输出遵循以下约定：
//   - 无差异（nil 或空切片）返回固定字面量 driftOK；
//   - 有差异先输出一行计数 "配置漂移: N 处"，再逐项输出，
//     每项一行，格式为 "  [级别] Key"；
//   - 摘要只包含 Key 与类别，绝不输出 Before/After 原文，避免泄露值；
//   - 条目先按 Key 字典序稳定排序，故相同输入集合无论初始顺序如何都得到
//     逐字节一致的文本；排序在副本上进行，不修改调用方持有的切片；
//   - Key 中的 CR/LF 被压成空格，保证一项恰好一行，CJK 文本保持完整。
func DriftSummary(entries []DriftEntry) string {
	if len(entries) == 0 {
		return driftOK
	}

	ordered := sortDriftEntries(entries)

	var b strings.Builder
	fmt.Fprintf(&b, "配置漂移: %d 处\n", len(ordered))
	for _, e := range ordered {
		label := e.Kind
		if zh, ok := driftKindLabels[e.Kind]; ok {
			label = zh
		}
		fmt.Fprintf(&b, "  [%s] %s\n", label, driftNewlineReplacer.Replace(e.Key))
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// sortDriftEntries 返回按 Key 字典序稳定排序后的副本。入参为 nil 时副本也为
// nil，排序 nil 切片是安全的空操作；调用方持有的切片顺序与内容不受影响。
func sortDriftEntries(entries []DriftEntry) []DriftEntry {
	ordered := append([]DriftEntry(nil), entries...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Key < ordered[j].Key
	})
	return ordered
}
