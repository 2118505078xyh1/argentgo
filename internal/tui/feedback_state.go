package tui

import (
	"strings"
	"unicode/utf8"
)

// FeedbackDraft 是"两步拒绝 → 反馈输入行"流程里的一个纯数据草稿：它既
// 描述输入行当前是否激活（Active），也持有已键入的反馈文本（Text）。
//
// 本文件刻意只提供无状态、零 I/O 的原语：所有方法都用值接收者，返回新的
// 草稿值而不修改入参，也不触碰 Model、终端或任何全局状态。调用方（未来的
// 审批接线层）负责在恰当时机把 Active 置位、把最终 Text 组装成拒绝原因。
// 当前没有任何调用方；默认零值完全惰性，对界面无任何影响。
type FeedbackDraft struct {
	Active bool
	Text   string
}

// maxFeedbackRunes 限制反馈文本长度，按 Unicode 码点（rune）计数而非字节
// 计数，使 CJK 汉字与 emoji 不会被拦腰截断。取 2000 与常见审批理由的长度
// 量级相当；达到上限后 AppendRune 直接丢弃新字符，保持草稿不变。
const maxFeedbackRunes = 2000

// 反馈输入行识别的按键串。用字符串常量而非 Key.Kind，是为了让本原语层
// 与终端字节流解码彻底解耦（tui 不依赖任何上层输入分派）。调用方把解码后
// 的按键映射为这些字符串后再传给 ShouldFinish 即可。
const (
	feedbackKeyEnter = "enter"
	feedbackKeyEsc   = "esc"
)

// feedbackHint 是反馈输入行激活时状态栏展示的提示。与 turn_projection.go
// 的阶段文案保持一致：稳定中文常量，日后可整批迁往 locale 表。
const feedbackHint = "请输入拒绝理由，回车提交，Esc 取消"

// AppendRune 把 rune r 追加到文本末尾并返回新草稿。它保证 rune 边界安全：
// string(r) 总是合法的 UTF-8 编码，因此不会切断已有的多字节字符。文本达到
// maxFeedbackRunes 后追加被忽略。本方法与 Active 无关，Active 只影响
// ShouldFinish / ApplyDraftToSignal 这类"状态 → 行为"的投影。
func (d FeedbackDraft) AppendRune(r rune) FeedbackDraft {
	if utf8.RuneCountInString(d.Text) >= maxFeedbackRunes {
		return d
	}
	d.Text += string(r)
	return d
}

// Backspace 删除文本末尾的一个 rune 并返回新草稿。借助
// utf8.DecodeLastRuneInString 定位最后一个码点的字节宽度，因此删除 CJK 汉字
// 或 emoji（乃至任何多字节字符）都不会留下半个 UTF-8 序列。空文本时返回
// 原值。
//
// 注意安全边界是"码点"而非"字素簇"：由多个码点组成的复杂 emoji（如带 ZWJ
// 的组合）会逐个码点回退，但每一步都保证输出是合法 UTF-8、不切坏任何单个
// 字符。
func (d FeedbackDraft) Backspace() FeedbackDraft {
	if d.Text == "" {
		return d
	}
	_, size := utf8.DecodeLastRuneInString(d.Text)
	if size <= 0 {
		return d
	}
	d.Text = d.Text[:len(d.Text)-size]
	return d
}

// ShouldFinish 报告按键 key 是否应当结束反馈输入行。
//
//   - 草稿未激活（零值默认）时恒为 false：默认关闭，零行为。
//   - feedbackKeyEsc：取消，无论有无文本都结束。
//   - feedbackKeyEnter：提交，但仅在 TrimSpace 后仍有文本时结束，避免误按
//     回车提交空反馈。
//   - 其余按键：false。
//
// 这是纯谓词：不消耗按键、不修改草稿。调用方仍需自行区分"提交"与"取消"
// （用同一个 key 与常量比较即可），以便在提交时把 Text 组装成拒绝原因。
func (d FeedbackDraft) ShouldFinish(key string) bool {
	if !d.Active {
		return false
	}
	switch key {
	case feedbackKeyEsc:
		return true
	case feedbackKeyEnter:
		return strings.TrimSpace(d.Text) != ""
	default:
		return false
	}
}

// ApplyDraftToSignal 把反馈草稿投影到渲染信号上：反馈激活时仅把 StatusText
// 替换为反馈提示，ShowSpinner / AcceptInput 等其它字段保持原样；未激活时
// 原样返回 sig（默认关闭、逐字节不变）。sig 是值入参，本函数绝不修改调用方
// 的信号。
func ApplyDraftToSignal(d FeedbackDraft, sig RenderSignal) RenderSignal {
	if !d.Active {
		return sig
	}
	sig.StatusText = feedbackHint
	return sig
}
