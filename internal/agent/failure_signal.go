package agent

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// 本文件提供 R14 压缩 wire 所需的"隐式上下文"失败信号提取原语。
// 它们刻意是纯函数、默认关闭：不接入 Compact / CollectCompactionEvidence，
// 是否把结果合并进摘要完全由调用方决定，因此默认压缩行为逐字节不变。
//
// session 包里已有语义相近的 failureReason / hasImplicitFailureSignal，但按
// AGENTS.md 的分层规则 agent 不导入 session（见 subagent.go 的约定），故这里
// 以相同的判定口径本地实现一份，保持字节级行为一致而不引入跨层依赖。

// 失败信号类别，取值稳定，便于摘要与快照比对。
const (
	SignalKindIsError = "is_error" // 工具结果的显式 is_error 标记
	SignalKindExit    = "exit"     // 非零退出码 [exit N]
	SignalKindTimeout = "timeout"  // 超时 timed out
	SignalKindWord    = "word"     // 中英失败词
)

// signalTextRunes 限制单条信号 Text 的符文数，既控制体积又对 CJK 安全。
const signalTextRunes = 160

// failureSignalMarkers 与 session 的 failureMarkers 保持一致：匹配在单行上做
// 忽略大小写的子串匹配，因此失败被埋在进度输出之后也能被捕获。
var failureSignalMarkers = []string{
	"error", "failed", "panic", "traceback", "fatal", "错误", "失败", "异常", "崩溃",
}

// SignalInput 是失败信号提取所用的轻量消息视图。它刻意不依赖 llm / eval 的
// 具体消息类型，使本原语保持无调用方、零依赖（除标准库）。
//
// IsError 对应工具结果元数据中的显式 is_error 标记；调用方从工具结果数据里
// 读取该标记后填入。ToolName 在工具结果消息上填写，其余消息可留空。
type SignalInput struct {
	Role     string
	Content  string
	ToolName string
	IsError  bool
}

// MessageLike 是 SignalInput 的别名，保留与调用方"消息视图"命名习惯的一致。
// 两者是同一类型，函数签名可写作 []MessageLike 或 []SignalInput。
type MessageLike = SignalInput

// FailureSignal 是一条从消息中提取的失败信号。
type FailureSignal struct {
	// Text 是折叠为单行、按 rune 截断的失败文本；DeduplicateSignals 按它去重。
	Text string
	// Turn 是消息在输入切片中的序号（0 基）。
	Turn int
	// Tool 是工具名，来自 SignalInput.ToolName；非工具消息为空。
	Tool string
	// Kind 是信号类别，取值见 SignalKind* 常量。
	Kind string
}

// ExtractFailureSignals 从消息切片中提取失败信号。
//
// 每条形如"显式 is_error / 非零 [exit N] / timed out / 中英失败词"的消息至多
// 产生一条信号，类别按该优先级择一（与 session 的 failureReason 口径一致）。
// 结果按输入顺序稳定排列；本函数是纯函数，只读取入参，不修改 messages 或
// 其中的字符串，对 CJK / emoji 安全（截断只在 rune 边界上进行）。
func ExtractFailureSignals(messages []MessageLike) []FailureSignal {
	out := make([]FailureSignal, 0)
	for i, msg := range messages {
		text, kind := "", ""
		if msg.IsError {
			text = clipSignalText(msg.Content)
			if text == "" {
				// 只有 is_error 标记而没有输出文本时，给出稳定的占位说明。
				text = "is_error"
			}
			kind = SignalKindIsError
		} else {
			text, kind = failureSignalReason(msg.Content)
		}
		if kind == "" {
			continue
		}
		out = append(out, FailureSignal{
			Text: text,
			Turn: i,
			Tool: strings.TrimSpace(msg.ToolName),
			Kind: kind,
		})
	}
	return out
}

// failureSignalReason 返回 content 中最主要的失败信号文本与类别。
// 判定顺序与 session 的 failureReason 一致：非零退出码 -> 超时 -> 失败词。
// 找不到时返回 ("", "")。
func failureSignalReason(content string) (string, string) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", ""
	}

	// 逐行查找 [exit N] 以避免在 lower 与原文之间做字节偏移映射：
	// 折叠大小写可能改变非 ASCII 前缀的字节长度，按行整段处理天然对 CJK 安全。
	for _, line := range strings.Split(trimmed, "\n") {
		if line = strings.TrimSpace(line); line != "" && hasNonZeroExit(strings.ToLower(line)) {
			return clipSignalText(line), SignalKindExit
		}
	}
	if strings.Contains(strings.ToLower(trimmed), "timed out") {
		return clipSignalText(trimmed), SignalKindTimeout
	}
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if containsAny(strings.ToLower(line), failureSignalMarkers...) {
			return clipSignalText(line), SignalKindWord
		}
	}
	return "", ""
}

// hasNonZeroExit 报告 lower（应为已折叠为小写的单行文本）是否含有非零 [exit N]。
func hasNonZeroExit(lower string) bool {
	idx := strings.Index(lower, "[exit ")
	if idx < 0 {
		return false
	}
	rest := lower[idx+len("[exit "):]
	code, digits := 0, 0
	for _, r := range rest {
		if r < '0' || r > '9' {
			break
		}
		code = code*10 + int(r-'0')
		digits++
	}
	return digits > 0 && code != 0
}

// clipSignalText 把文本折叠为单行并按 rune 截断到 signalTextRunes 个符文。
// 全程不按字节切分，因此不会切开多字节 UTF-8 序列。
func clipSignalText(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= signalTextRunes {
		return text
	}
	return string([]rune(text)[:signalTextRunes]) + "..."
}

// DeduplicateSignals 按 Text 去重并保持原有顺序。它返回新切片，不修改入参。
func DeduplicateSignals(sigs []FailureSignal) []FailureSignal {
	if len(sigs) == 0 {
		return nil
	}
	out := make([]FailureSignal, 0, len(sigs))
	seen := make(map[string]struct{}, len(sigs))
	for _, sig := range sigs {
		if _, ok := seen[sig.Text]; ok {
			continue
		}
		seen[sig.Text] = struct{}{}
		out = append(out, sig)
	}
	return out
}

// FailureSignalSummary 返回稳定的中文摘要。
//
// 摘要只由类别计数、工具名与轮次范围构成，刻意不包含任何 Text 原文，
// 因此不会把工具输出中的敏感串（凭据、令牌等）泄漏到日志或报告里。
// 空输入返回固定短语"无失败信号"。
func FailureSignalSummary(sigs []FailureSignal) string {
	if len(sigs) == 0 {
		return "无失败信号"
	}

	counts := make(map[string]int, len(sigs))
	toolSeen := make(map[string]bool)
	var tools []string
	minTurn, maxTurn := sigs[0].Turn, sigs[0].Turn
	for _, sig := range sigs {
		counts[sig.Kind]++
		if sig.Tool != "" && !toolSeen[sig.Tool] {
			toolSeen[sig.Tool] = true
			tools = append(tools, sig.Tool)
		}
		if sig.Turn < minTurn {
			minTurn = sig.Turn
		}
		if sig.Turn > maxTurn {
			maxTurn = sig.Turn
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "失败信号共 %d 条", len(sigs))

	// 已知类别按固定顺序输出；未知类别按名字排序补齐，保证稳定且不丢信息。
	for _, kind := range []string{SignalKindIsError, SignalKindExit, SignalKindTimeout, SignalKindWord} {
		if c := counts[kind]; c > 0 {
			fmt.Fprintf(&b, "，%s %d 条", signalKindLabel(kind), c)
			delete(counts, kind)
		}
	}
	remaining := make([]string, 0, len(counts))
	for kind := range counts {
		remaining = append(remaining, kind)
	}
	sort.Strings(remaining)
	for _, kind := range remaining {
		fmt.Fprintf(&b, "，%s %d 条", signalKindLabel(kind), counts[kind])
	}

	if len(tools) > 0 {
		sort.Strings(tools)
		b.WriteString("；涉及工具 ")
		b.WriteString(strings.Join(tools, "、"))
	}
	fmt.Fprintf(&b, "；轮次 %d-%d", minTurn, maxTurn)
	return b.String()
}

// signalKindLabel 把类别值翻译成中文标签；未知值原样返回。
func signalKindLabel(kind string) string {
	switch kind {
	case SignalKindIsError:
		return "错误标记"
	case SignalKindExit:
		return "非零退出码"
	case SignalKindTimeout:
		return "超时"
	case SignalKindWord:
		return "失败词"
	default:
		return kind
	}
}
