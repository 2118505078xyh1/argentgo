package eval

import (
	"fmt"
	"unicode/utf8"
)

// MessageLike 是上下文压力诊断所用的轻量消息输入：只携带
// 估算所需的角色、文本内容和"是否为工具结果"标记。
// 它刻意不依赖 agent 或 llm 的具体消息类型，使诊断保持无调用方、零依赖。
type MessageLike struct {
	Role       string
	Content    string
	ToolResult bool
}

// ContextPressure 是一次对话上下文的压力快照，由 MeasurePressure 产生。
// 所有字段都是只读诊断量：调用方应将其视为值。
type ContextPressure struct {
	// Messages 是传入的消息条数。
	Messages int
	// EstTokens 是所有消息 Content 经注入的 est 估算后的 token 总数。
	EstTokens int
	// ToolResultBytes 是被标记为工具结果的消息 Content 的字节总数（按字节计，非 rune 数）。
	ToolResultBytes int
	// OldestTokenAge 是最老消息的"序号年龄"：它之后又追加了多少条消息。
	// 对 N 条消息，最老消息位于序号 0，当前位于序号 N-1，因此年龄为 N-1；
	// 空对话为 0。它是上下文停留时长的代理量。
	OldestTokenAge int
	// Verdict 是按固定阈值分出的压力等级："healthy"/"warning"/"critical"。
	Verdict string
}

// 压力等级字符串。不导出以保持诊断原语的封闭性；值本身稳定，便于快照与日志比对。
const (
	verdictHealthy  = "healthy"
	verdictWarning  = "warning"
	verdictCritical = "critical"
)

// Verdict 阈值。三个信号各自独立映射到 0(健康)/1(警告)/2(危急)，
// 最终等级取三者最大值。阈值是包内常量，规则稳定，便于测试锁定边界。
const (
	pressureWarningTokens     = 4000
	pressureCriticalTokens    = 8000
	pressureWarningToolBytes  = 20000
	pressureCriticalToolBytes = 50000
	pressureWarningMessages   = 20
	pressureCriticalMessages  = 50
)

// MeasurePressure 测量一组消息对上下文窗口造成的压力。它是纯函数：
// 只读入参、不修改 messages 或其中的字符串，相同的 messages 与 est 总产生相同结果。
// est 为每条消息的 Content 估算 token 数；为 nil 时退化为按 Unicode rune 计数，
// 因此 CJK 与 emoji 不会被按字节高估。ToolResultBytes 始终是字节数，与 est 无关。
func MeasurePressure(messages []MessageLike, est func(string) int) ContextPressure {
	if est == nil {
		est = defaultTokenEstimate
	}
	p := ContextPressure{Messages: len(messages)}
	if len(messages) > 0 {
		p.OldestTokenAge = len(messages) - 1
	}
	for _, m := range messages {
		p.EstTokens += est(m.Content)
		if m.ToolResult {
			p.ToolResultBytes += len(m.Content)
		}
	}
	p.Verdict = p.verdict()
	return p
}

// defaultTokenEstimate 在 est 为 nil 时使用：按 rune 计数，
// 既不会切开多字节 UTF-8 序列，也不会把 CJK/emoji 当成多个字节 token。
func defaultTokenEstimate(s string) int {
	return utf8.RuneCountInString(s)
}

// verdict 根据 EstTokens、ToolResultBytes 与 Messages 三个信号给出等级；
// OldestTokenAge 与 Messages 近似冗余，仅作诊断报告，不单独参与分级。
func (p ContextPressure) verdict() string {
	level := max(
		levelFor(p.EstTokens, pressureWarningTokens, pressureCriticalTokens),
		levelFor(p.ToolResultBytes, pressureWarningToolBytes, pressureCriticalToolBytes),
		levelFor(p.Messages, pressureWarningMessages, pressureCriticalMessages),
	)
	switch level {
	case 2:
		return verdictCritical
	case 1:
		return verdictWarning
	default:
		return verdictHealthy
	}
}

// levelFor 把单个信号值映射到 0/1/2：达到 critical 为 2，达到 warning 为 1，否则为 0。
func levelFor(value, warning, critical int) int {
	switch {
	case value >= critical:
		return 2
	case value >= warning:
		return 1
	default:
		return 0
	}
}

// Summary 返回稳定的中文摘要，等级标签经过翻译，未知等级原样保留。
// 格式固定，可直接用于报告或快照比对。
func (p ContextPressure) Summary() string {
	return fmt.Sprintf("上下文压力[%s]：消息 %d 条，估算 token %d，工具结果 %d 字节，最老消息年龄 %d",
		verdictLabel(p.Verdict), p.Messages, p.EstTokens, p.ToolResultBytes, p.OldestTokenAge)
}

// verdictLabel 把等级值翻译成中文标签；未知值原样返回，保证摘要永不丢失信息。
func verdictLabel(v string) string {
	switch v {
	case verdictHealthy:
		return "健康"
	case verdictWarning:
		return "警告"
	case verdictCritical:
		return "危急"
	default:
		return v
	}
}
