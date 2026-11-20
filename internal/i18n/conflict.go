package i18n

// HasPlaceholderConflict 报告 text 是否混合了本包支持的两种
// 占位符风格：位置 fmt 动词如 %s 或 %d（T 通过 fmt.Sprintf 格式化），
// expands.
//
// 同时包含两种风格的字符串无法被正确格式化。把它
// 传给 T 会使每个 {name} 原样保留（并把杂散大括号交给
// Sprintf），而传给 Format 会使每个 %s 原样保留。
// 因此混合它们的目录条目是翻译 bug；调用方可以
// 在测试或 doctor 命令中使用这个谓词，在它到达 UI 之前
// 暴露问题。它是附加的，不改变 T 或 Format 的行为。
//
// 转义被尊重，使用与翻译审计相同的扫描器：
// "%%" 是字面百分号，"{{name}}" / "}}" 是字面大括号。这样的
// 文本不算占位符。无效的大括号形状如
// "{a-b}" 也不算。
//
// 检查是纯的：它不读取目录或全局状态，也不在
// Missing 中记录任何内容。
func HasPlaceholderConflict(text string) bool {
	return len(formatVerbs(text)) > 0 && len(namedPlaceholders(text)) > 0
}
