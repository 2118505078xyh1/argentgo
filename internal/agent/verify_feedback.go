package agent

import (
	"strings"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

const (
	// defaultVerifyMaxBytes 限制一条反馈消息中输出部分的大小。
	defaultVerifyMaxBytes = 4096
	verifyTruncatedMarker = "... (truncated)"

	// verifyFixInstruction 在一次可操作的验证 FAIL 之后开启下一轮模型调用。
	// 它对应 aider 风格的"修复下方错误"前言：该结论是一条修复请求，而不仅仅是一行状态。
	// 保持为常量，使 HasDiagnosticContent 能把它作为样板内容剥离。
	verifyFixInstruction = "Fix the errors below, if possible."
	// verifyAnchorExplanation 引出带 █ 标记的源码窗口。仅当反馈确实包含这样的窗口时才会添加，
	// 这样普通的日志尾部不会出现无法解释的标记噪音。
	verifyAnchorExplanation = "Relevant line(s) marked with █."

	// verifyAnchorPad 是 AnchorSourceLines 在有问题的 file:line 引用两侧输出的上下文行数。
	// 两行足以给模型提供可采取行动的上下文，又不会用噪音填满有界的反馈。
	verifyAnchorPad = 2
	// verifyAnchorSep 把脱敏后的日志尾部与源码窗口分隔开。
	verifyAnchorSep = "\n"
)

// VerificationFeedback 渲染一段紧凑、脱敏、有界的反馈字符串，
// 适合在验证命令之后作为下一条用户/工具消息注入。
//
// PASS 只有结论行：调用方很少需要成功检查的原始输出。FAIL 只追加输出的尾部，
// 因为验证工具会把可操作的错误放在最后打印。输出在裁剪之前先经 llm.RedactText 脱敏，
// 且裁剪永不会切断 UTF-8 rune。maxBytes 限制输出部分（结论行始终保留）；
// 非正值选用 defaultVerifyMaxBytes。该函数是纯函数：无 I/O、无共享状态。
//
// 这是一个构建块。在拥有验证的那一层接线（例如在 workflow 的 StepCheck 之后，
// 或在验证工具调用之后的 dispatch 路径中），把返回的字符串追加到下一条模型消息即可。
//
// VerificationFeedback 刻意不做源码行锚定，也不添加修复指令前言：
// 它的输出与锚定之前的行为逐字节一致。拥有面向模型的下一轮次的调用方（验证循环）
// 应直接调用 VerificationFeedbackAt，这样 FAIL 也会携带"修复下方错误"指令和带 █ 标记的源码窗口。
func VerificationFeedback(label string, passed bool, output string, maxBytes int) string {
	return renderVerifyFeedback(label, passed, output, maxBytes, "", false)
}

// VerificationFeedbackAt 渲染与 VerificationFeedback 相同的有界、脱敏反馈，
// 并针对可操作的 FAIL 做了两项清晰度增强：
//
//   - 文本以 verifyFixInstruction 开头，使模型把该结论读作修复请求，而非一行裸状态；
//   - 当 root 能产生可用的 file:line 引用时，在带 █ 标记的源码窗口之前立即输出
//     verifyAnchorExplanation。
//
// 该说明以窗口实际包含 █ 为条件，因此普通日志尾部绝不会多出一个无法解释的标记头。
// 整个输出部分（尾部 + 分隔符 + 说明 + 锚定窗口）保持在 maxBytes 之内：
// 预算中有一部分专为锚定保留，因此它绝不会挤掉其他内容或超出界限。
//
// PASS 或空输出仍只返回结论行。没有可用锚点的 FAIL（包括不可读/root 之外的文件）
// 会得到修复指令，但不会有 █ 说明。
//
// 应用侧接线（由 internal/app 负责，而非本文件）：internal/app/verify_wire.go 中的 NewVerifyFunc
// 已经捕获了 `root`（它被用作 command.Dir）。在 NewVerifyFunc 返回的 Verify 回调中，
// 用 VerificationFeedbackAt 组合反馈，例如：
//
//	fb := agent.VerificationFeedbackAt(label, passed, output, maxBytes, root)
//
// 并把 `root` 一路传入验证循环（Options -> verifyAfterBatch -> BuildVerifyFeedback ->
// VerificationFeedbackAt），使模型看到源码窗口，而不只是日志尾部。
func VerificationFeedbackAt(label string, passed bool, output string, maxBytes int, root string) string {
	return renderVerifyFeedback(label, passed, output, maxBytes, root, true)
}

// renderVerifyFeedback 是共享实现。fixPreamble 仅在 VerificationFeedbackAt 中为 true；
// VerificationFeedback 为不注入面向模型的下一轮次的调用方保留原始布局。
func renderVerifyFeedback(label string, passed bool, output string, maxBytes int, root string, fixPreamble bool) string {
	label = strings.ToUpper(strings.TrimSpace(label))
	if label == "" {
		label = "VERIFY"
	}
	verdict := "PASS"
	if !passed {
		verdict = "FAIL"
	}
	head := "[verify: " + label + "] " + verdict
	if passed {
		return head
	}
	clean := llm.RedactText(strings.TrimSpace(output))
	if clean == "" {
		return head
	}
	budget := resolveVerifyMaxBytes(maxBytes)

	prefix := head + "\n"
	if fixPreamble {
		prefix = verifyFixInstruction + "\n" + prefix
	}
	plain := prefix + clipVerifyTail(clean, budget)

	// 空的 root（旧版默认值）或没有可用锚点的输出，会使反馈没有源码窗口，
	// 因此也省略 █ 说明。
	if strings.TrimSpace(root) == "" {
		return plain
	}
	anchor := AnchorSourceLines(clean, root, verifyAnchorPad)
	if anchor == "" {
		return plain
	}

	// 最多预留一半预算给源码窗口，使日志尾部保有空间，
	// 然后把剩余部分（减去分隔符和可选的说明行）还给尾部。
	anchorRoom := budget / 2
	if anchorRoom <= 0 {
		return plain
	}
	anchor = clipTailBytes(anchor, anchorRoom)
	if anchor == "" {
		return plain
	}

	// 被裁剪的窗口可能丢失标记行；仅在仍保留标记行时才解释 █。
	explanation := ""
	if strings.Contains(anchor, "█") {
		explanation = verifyAnchorExplanation + "\n"
	}
	tailRoom := budget - len(verifyAnchorSep) - len(explanation) - len(anchor)
	if tailRoom < 0 {
		return plain
	}
	return prefix + clipVerifyTail(clean, tailRoom) + verifyAnchorSep + explanation + anchor
}

// HasDiagnosticContent 报告一段验证反馈字符串，除了始终存在的样板内容之外，
// 是否带有任何实质性的诊断信息。样板内容包括：
// "[verify: X] PASS/FAIL" 结论头、VerificationFeedbackAt 添加的修复指令和 █ 说明行、
// 反思提示、脱敏占位符（[redacted]/[REDACTED]/[email]）以及空行。
//
// 它是纯函数且刻意保守：任何留存的非样板行——错误消息、file:line 引用、CJK 正文、
// 带 █ 锚定的源码窗口——都会使它返回 true。因此调用方可以用 false 结果检测 aider-P2 情形：
// 一次 FAIL 消耗了一个反思轮次，却给出了一条模型无法采取行动的反馈消息（只有头部 + 样板）。
//
// 它既适用于原始验证输出，也适用于 BuildVerifyFeedback/VerificationFeedbackAt 返回的完整字符串，
// 因为它会在任何位置剥离已知的样板内容，而不是假定固定的布局。
func HasDiagnosticContent(feedback string) bool {
	for _, line := range strings.Split(feedback, "\n") {
		text := strings.TrimSpace(line)
		if text == "" || isVerifyVerdictLine(text) || isVerifyReflectionHintLine(text) ||
			text == verifyFixInstruction || text == verifyAnchorExplanation {
			continue
		}
		if isOnlyRedactionPlaceholders(text) {
			continue
		}
		return true
	}
	return false
}

// isVerifyVerdictLine 识别 "[verify: LABEL] PASS|FAIL" 头部行。
func isVerifyVerdictLine(line string) bool {
	if !strings.HasPrefix(line, "[verify:") {
		return false
	}
	_, rest, ok := strings.Cut(line, "]")
	if !ok {
		return false
	}
	verdict := strings.TrimSpace(rest)
	return verdict == "PASS" || verdict == "FAIL"
}

// isVerifyReflectionHintLine 识别 BuildVerifyFeedback 附加到每条 FAIL 的防重试行。
// 它是指引而非诊断内容，因此它本身不得使 HasDiagnosticContent 返回 true。
func isVerifyReflectionHintLine(line string) bool {
	return strings.HasPrefix(line, verifyReflectionHintPrefix) &&
		strings.HasSuffix(line, verifyReflectionHintSuffix)
}

// isOnlyRedactionPlaceholders 报告 text 是否完全由 llm.RedactText 替换机密和邮箱所用的占位符组成。
// 输出完全是被脱敏值的 FAIL 不携带任何可用诊断。
func isOnlyRedactionPlaceholders(text string) bool {
	for _, placeholder := range []string{"[redacted]", "[REDACTED]", "[email]"} {
		text = strings.ReplaceAll(text, placeholder, "")
	}
	return strings.TrimSpace(text) == ""
}

// VerifyResult 是供 FormatVerifyResults 使用的一次检查结果。
type VerifyResult struct {
	Label  string
	Passed bool
	Output string
}

// FormatVerifyResults 把多个检查批量合并为一条有界的反馈字符串。
// 每个结果由 VerificationFeedback 渲染；拼接后的文本会保留尾部裁剪到 maxBytes，
// 因此一长串失败不会撑爆上下文窗口。保留的是最近的（最后的）失败。
func FormatVerifyResults(results []VerifyResult, maxBytes int) string {
	if len(results) == 0 {
		return ""
	}
	parts := make([]string, 0, len(results))
	for _, result := range results {
		parts = append(parts, VerificationFeedback(result.Label, result.Passed, result.Output, maxBytes))
	}
	return clipVerifyTail(strings.Join(parts, "\n"), resolveVerifyMaxBytes(maxBytes))
}

func resolveVerifyMaxBytes(maxBytes int) int {
	if maxBytes <= 0 {
		return defaultVerifyMaxBytes
	}
	return maxBytes
}

// clipVerifyTail 只保留 text 的最后 maxBytes 字节，并在前面加上截断标记。
// 保留的切片起始于 rune 边界，因此即使切点落在多字节 rune 内部，结果也始终是合法 UTF-8。
func clipVerifyTail(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	if len(verifyTruncatedMarker) >= maxBytes {
		return clipTailBytes(verifyTruncatedMarker, maxBytes)
	}
	return verifyTruncatedMarker + clipTailBytes(text, maxBytes-len(verifyTruncatedMarker))
}

// clipTailBytes 返回 text 的最后 maxBytes 字节；当起点会切断 UTF-8 序列时，
// 将其前移到下一个 rune 边界。
func clipTailBytes(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	start := len(text) - maxBytes
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
}
