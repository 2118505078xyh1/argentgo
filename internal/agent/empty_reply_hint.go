package agent

import (
	"unicode/utf8"
)

// 本文件为"空回复纠正"提供有针对性且可独立测试的中文提示生成能力。
// 与循环里那条固定的英文 emptyReplyCorrection 不同，这里的提示会根据回复
// 实际携带的内容（推理、工具调用）按情形生成，并随连续空回复次数逐级增强语气。
// 生成过程是纯函数：只读参数、不访问时钟或外部状态、不修改任何输入，
// 相同参数总是逐字节返回相同的字符串；输出均为固定 UTF-8/CJK 模板，天然安全且有界。

const (
	// emptyReplyHintMaxBytes 是单条纠正提示的字节上限。
	// 三种基准提示都固定且很短，该常量用于锁定"有界"这一不变式：
	// 即便日后有人往模板里追加可变内容，也会在返回前被裁剪到该上限以内。
	emptyReplyHintMaxBytes = 512

	// emptyReplyHintTurnCap 是把连续纠正次数折算成语气级别的封顶值。
	// 超过该值后语气不再增强，使输出与异常巨大的 turn 值无关（稳定且有界）。
	emptyReplyHintTurnCap = 3
)

// 三种情形的基准提示，均为固定的简体中文模板（合法 UTF-8，CJK 安全）。
const (
	// emptyReplyHintToolCalls 对应"声明了工具调用却没有可见答复"：
	// 最可操作的情形，因此优先级最高——先要求执行工具并汇报结果。
	emptyReplyHintToolCalls = "上一轮只声明了工具调用，却没有执行也没有可见答复。请执行这些工具，并在完成后给出用户能看到的答复。"

	// emptyReplyHintReasoning 对应"有思考但没有可见答复"：
	// 要求把内部思考的结论写成用户能看到的答复。
	emptyReplyHintReasoning = "上一轮只有思考过程，没有给出可见答复。请把思考结论写成用户能看到的答复或下一步操作。"

	// emptyReplyHintPure 对应"纯空"：既无推理也无工具调用，要求直接给出答复。
	emptyReplyHintPure = "上一轮回复没有任何内容。请直接给出可见的答复或下一步操作。"
)

// emptyReplyHintEscalation 是随连续纠正次数追加的语气后缀（下标 = 级别 - 1）。
// 级别 1 不加后缀；级别越高要求越明确。全部为固定中文模板，保持 CJK 安全与有界。
var emptyReplyHintEscalation = [emptyReplyHintTurnCap]string{
	"",
	" 请勿再次以空回复结束本轮。",
	" 这是连续第三次提醒：必须给出可见答复，否则本轮将被判定为无进展并停止。",
}

// emptyReplyHint 根据一次"无可见答复"回复的特征生成有针对性的中文纠正提示。
//
// 三个参数只用于选择模板，不做 I/O、不修改状态：
//   - hasReasoning：回复是否携带推理文本（没有可见答复时才有意义）。
//   - toolCalls：回复声明的工具调用数量；正数表示存在工具调用。
//   - turn：连续空回复的纠正次数（从 1 开始）；非正值按 1 处理，超过
//     emptyReplyHintTurnCap 按封顶值处理。
//
// 情形优先级（从高到低）：有工具调用 > 有推理 > 纯空。
// 即使 hasReasoning 与 toolCalls 同时为真，也按"有工具调用"处理，因为执行工具
// 并汇报结果是此时最具体、最可操作的纠正方向。
//
// 函数保证三条性质：
//   - CJK 安全：输出是合法 UTF-8，兜底裁剪也只落在 rune 边界，不会切断多字节字符。
//   - 有界：输出字节数不超过 emptyReplyHintMaxBytes。
//   - 稳定：相同输入逐字节一致，且 turn 超过封顶后输出不再变化。
func emptyReplyHint(hasReasoning bool, toolCalls int, turn int) string {
	// 按优先级选择基准提示。
	var base string
	switch {
	case toolCalls > 0:
		base = emptyReplyHintToolCalls
	case hasReasoning:
		base = emptyReplyHintReasoning
	default:
		base = emptyReplyHintPure
	}

	// 把 turn 折算为受限的语气级别，避免异常值影响输出。
	level := turn
	if level < 1 {
		level = 1
	}
	if level > emptyReplyHintTurnCap {
		level = emptyReplyHintTurnCap
	}

	hint := base + emptyReplyHintEscalation[level-1]
	// 防御性兜底：模板本身很短，不会触发裁剪；但约束在此显式成立，
	// 日后若加入可变内容也不会破坏"有界 + CJK 安全"的承诺。
	return clipHintHead(hint, emptyReplyHintMaxBytes)
}

// clipHintHead 保留 text 的前 maxBytes 字节，必要时在末尾追加省略号以示截断。
// 切点会前移到 rune 边界，因此即使落在 CJK 等多字节字符内部也不会切断字符，
// 返回结果始终是合法 UTF-8。maxBytes 非正或文本本身不超时原样返回。
func clipHintHead(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	const ellipsis = "…"
	budget := maxBytes - len(ellipsis)
	if budget <= 0 {
		return clipHeadBytes(text, maxBytes)
	}
	return clipHeadBytes(text, budget) + ellipsis
}

// clipHeadBytes 返回 text 的前 maxBytes 字节，并保证结果末尾落在 rune 边界。
// 调用方需保证 maxBytes 小于 len(text)（否则已经原样返回），因此索引不会越界。
func clipHeadBytes(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}
