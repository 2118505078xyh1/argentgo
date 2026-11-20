package app

// 工具结果预览的截断元数据。codex 审计指出：事件流里的工具结果缺少结构化
// 元数据，上层无法判断看到的是摘要还是全文。这里提供一个纯函数，在同时持有
// 原始输出与投递输出时生成一份字符串映射；它零副作用、不接线、不改任何事件
// 字节，调用方按需取用。

import "strconv"

// toolOutputPreviewMeta 比较原始输出 raw 与投递输出 delivered，生成截断元数据。
//
// 判定规则：delivered 在符文边界上是 raw 的严格前缀（raw 以 delivered 开头且
// 更长）时视为截断，truncated=true，并附带原始/投递的符文数与字节数以及省略量。
// 相等、内容不同、投递更长都不算截断，truncated=false。
// 空值也有定义：投递为空且原始非空是最大截断；原始为空则永不截断。
// 比较按 rune 切片进行，中文与 emoji 不会被切成半个 UTF-8 字符。
func toolOutputPreviewMeta(raw, delivered string) map[string]string {
	rawRunes := []rune(raw)
	deliveredRunes := []rune(delivered)

	meta := map[string]string{
		"original_runes":  strconv.Itoa(len(rawRunes)),
		"delivered_runes": strconv.Itoa(len(deliveredRunes)),
		"original_bytes":  strconv.Itoa(len(raw)),
		"delivered_bytes": strconv.Itoa(len(delivered)),
		"truncated":       "false",
	}
	// 严格前缀才算截断：长度相等（含相等内容）与更长（delivered 不可能是 raw 的前缀）都排除。
	if len(deliveredRunes) >= len(rawRunes) || !runePrefix(rawRunes, deliveredRunes) {
		return meta
	}
	meta["truncated"] = "true"
	meta["omitted_runes"] = strconv.Itoa(len(rawRunes) - len(deliveredRunes))
	meta["omitted_bytes"] = strconv.Itoa(len(raw) - len(delivered))
	return meta
}

// runePrefix 报告 prefix 是否为 full 的前缀。按 rune 比较，天然 CJK 安全，
// 不会把多字节字符的中继字节误当作边界。
func runePrefix(full, prefix []rune) bool {
	if len(prefix) > len(full) {
		return false
	}
	for i, r := range prefix {
		if full[i] != r {
			return false
		}
	}
	return true
}
