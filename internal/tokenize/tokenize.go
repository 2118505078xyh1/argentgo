// Package tokenize 保存 agent 预算循环和工具输出量提示
// 共享的 token 成本估算。它位于两个包之下，使
// 宽 rune 表恰好只有一个定义：过去它被复制三份
// （agent、bash 量提示、repo map）且注释要求人工保持
// 这些副本同步。
//
// 该估算刻意廉价且刻意高估：它
// 用于阻止提示溢出模型上下文窗口，而非为请求计价。
// 宽（CJK/全角/emoji）rune 在每个 provider 的 tokenizer 中大约耗费一个 token；
// 其他 rune 遵循 chars/4 规则。
// 单独计数避免了 chars/4 单独对 CJK 的严重低估，
// 同时相对于最高效的 tokenizer 仍是高估（
// 对预算而言这是安全的方向）。
package tokenize

// CharsPerToken 是英文文本和代码的经验法则比率。
const CharsPerToken = 4

// Estimate 返回 text 的估计 token 成本。空文本成本为零。
func Estimate(text string) int {
	if text == "" {
		return 0
	}
	// 无分配 ASCII 探测：逐字节扫描，全 < 0x80 即为纯 ASCII。
	// 纯 ASCII 直接按 chars/4 向上取整，与 estimateUncached 的快速路径
	// 公式逐值一致；完全跳过 hashContent 与 sharedCache。
	// 原因：对大段 ASCII 输出做 []byte 全量拷贝加 SHA256 的分配开销，
	// 高于省下的逐字节扫描，正是 P0 性能回归的根源。
	ascii := true
	for i := 0; i < len(text); i++ {
		if text[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return (len(text) + CharsPerToken - 1) / CharsPerToken
	}
	// 非 ASCII 走原缓存路径不变。
	key := hashContent(text)
	if value, ok := sharedCache.get(key, text); ok {
		return value
	}
	value := estimateUncached(text)
	sharedCache.put(key, text, value)
	return value
}

// estimateUncached 保存原本的估算逻辑，不含缓存层。
// 把它与 Estimate 分开，确保命中与未命中走完全相同的计算路径，
// 缓存只影响是否复用结果，不改变任何输入对应的输出。
func estimateUncached(text string) int {
	// 快速路径：纯 ASCII 文本既不需要 rune 解码也不需要宽 rune
	// 表，且该估算对完整 transcript 每轮运行数次。
	for i := 0; i < len(text); i++ {
		if text[i] >= 0x80 {
			return estimateMixed(text)
		}
	}
	return (len(text) + CharsPerToken - 1) / CharsPerToken
}

// estimateMixed 是对至少含一个非 ASCII 字节的文本的 rune 感知路径。
func estimateMixed(text string) int {
	wide, narrow := 0, 0
	for _, r := range text {
		if IsWide(r) {
			wide++
		} else {
			narrow++
		}
	}
	return wide + (narrow+CharsPerToken-1)/CharsPerToken
}

// IsWide 报告 r 是否属于 tokenizer 视为每个 rune 大约一个 token 的文字：
// 汉字、假名、韩文、全角形式、CJK 标点和常见 emoji。
func IsWide(r rune) bool {
	return (r >= 0x1100 && r <= 0x115F) || // Hangul Jamo
		(r >= 0x2E80 && r <= 0xA4CF) || // CJK radicals, Kangxi, Han
		(r >= 0xAC00 && r <= 0xD7A3) || // Hangul syllables
		(r >= 0xF900 && r <= 0xFAFF) || // CJK compatibility ideographs
		(r >= 0xFE10 && r <= 0xFE19) || // CJK punctuation
		(r >= 0xFE30 && r <= 0xFE6F) || // CJK compatibility forms
		(r >= 0xFF00 && r <= 0xFF60) || // fullwidth forms
		(r >= 0xFFE0 && r <= 0xFFE6) ||
		(r >= 0x1F300 && r <= 0x1FAFF) || // symbols/emoji are commonly 1+ tokens
		(r >= 0x20000 && r <= 0x3FFFD) // CJK extension planes
}
