package app

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// 本文件提供"按提示自动决策性能旋钮"的纯函数 judge。它是零成本的：
// 不调用模型、不做任何 I/O、不读全局状态，只依据传入的提示文本与两个布尔标志
// 推导出一组性能建议。默认不接入 Execute；caller 可以自行决定是否在某条路径上
// 使用这些建议。它刻意只存在于 app 包（唯一允许导入一切的组合根），
// 从而不向无依赖的核心包增加任何新约束。
//
// 安全边界（务必遵守）：
// DecidePrompt 的输入与输出都只涉及"性能"维度。PromptDecision 里没有、
// 也绝不会加入 Approve / Policy / SandboxMode / MutatingTools 等安全字段。
// 换句话说，本 judge 的判定结果不能被用来批准工具、放宽策略、切换沙箱模式
// 或启用修改类工具；mutating 入参只当作"任务复杂度"的信号参与轮次/思考档位
// 的建议，绝不参与任何授权决策。所有安全决策必须继续走 security 包的既有链路。

// Thinking 档位建议。它们是给上层选择推理强度的提示，不是安全开关。
const (
	thinkingOff  = "off"  // 简单任务：无需额外推理
	thinkingLow  = "low"  // 一般任务：轻度推理
	thinkingHigh = "high" // 复杂任务：充分推理
)

// 轮次上限建议。数值为保守的启发式默认值，上层可自行再收紧，但不应自行放宽到
// 超过用户/配置显式声明的上限。
const (
	turnsSimple  = 6  // 简单任务
	turnsMedium  = 12 // 一般任务
	turnsComplex = 20 // 复杂任务
)

// 文本长度（按 Unicode 码点计，避免 CJK 被字节数高估）与复杂度计分阈值。
const (
	promptMediumRunes = 200  // 达到此长度视为"中等长度"
	promptLongRunes   = 1000 // 达到此长度视为"长提示"
	scoreMedium       = 2    // 达到此分数进入中等档
	scoreComplex      = 4    // 达到此分数进入复杂档
)

// PromptDecision 是一次提示的性能旋钮建议。
//
// 它只包含三个性能字段：Thinking（思考档位建议）、UseSecondary（是否倾向使用
// 次级/轻量模型）、MaxTurns（轮次上限建议）。刻意不包含任何安全字段——
// Approve、Policy、SandboxMode、MutatingTools 均不在此结构中，也不会通过未来的
// 改动加入：本 judge 无权影响授权、策略、沙箱或修改类工具的可用性。
type PromptDecision struct {
	Thinking     string
	UseSecondary bool
	MaxTurns     int
}

// DecidePrompt 仅依据提示文本特征与两个标志推导性能建议，是纯函数。
//
// 复杂度信号：
//   - 长度：先剥离非用户文本，再按码点计数，长/中等长度分别加分；
//   - hasCode：调用方已识别出的"含代码块"信号，加分（代码任务通常需要更多推理）；
//   - 多步骤标记：正文中的"步骤/首先/然后/step 1/1."等，加分；
//   - mutating：调用方给出的"任务涉及修改"信号，仅作为复杂度加分，
//     不参与任何安全授权。
//
// 防注入：分析前会用 stripPromptNonUserText 剥离代码块、<system-reminder> 等注入块、
// @-引用与通用 HTML 标签，因此附件或注入文本里的长度与"步骤"字样无法操纵判定。
//
// 计分：长提示 +4（或中等 +2）、hasCode +2、多步骤 +2、mutating +2；
// >=4 为复杂档、>=2 为中等档、其余为简单档。分数与档位映射是确定性的，
// 相同输入总是返回相同结果。
func DecidePrompt(prompt string, hasCode bool, mutating bool) PromptDecision {
	text := stripPromptNonUserText(prompt)

	score := 0
	length := utf8.RuneCountInString(text)
	switch {
	case length >= promptLongRunes:
		score += 4
	case length >= promptMediumRunes:
		score += 2
	}

	if hasCode {
		score += 2
	}
	if hasMultiStepMarkers(text) {
		score += 2
	}
	if mutating {
		score += 2
	}

	switch {
	case score >= scoreComplex:
		return PromptDecision{Thinking: thinkingHigh, UseSecondary: false, MaxTurns: turnsComplex}
	case score >= scoreMedium:
		return PromptDecision{Thinking: thinkingLow, UseSecondary: false, MaxTurns: turnsMedium}
	default:
		return PromptDecision{Thinking: thinkingOff, UseSecondary: true, MaxTurns: turnsSimple}
	}
}

// hasMultiStepMarkers 报告正文中是否出现"多步骤"标记。它是纯启发式，仅用于
// 性能旋钮，误报（把普通词当作步骤）最多让建议偏保守，不会影响安全。
func hasMultiStepMarkers(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range multiStepMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return numberedStepLine.MatchString(text)
}

// multiStepMarkers 是多步骤任务的典型措辞。英文标记统一按小写保存，匹配前对
// 正文做 strings.ToLower；中文标记不受大小写影响。
var multiStepMarkers = []string{
	"步骤", "首先", "然后", "接着", "最后", "第一步", "第二步", "分两步", "分三步",
	"step 1", "step 2", "step 3", "firstly", "secondly", "finally", "after that", "then,",
}

// 下列正则与 internal/agent 中 stripNonUserText 使用的模式同源。
// RE2 不支持反向引用，故每种注入容器由各自编译的模式匹配（见 injection block 构建）。
var (
	judgeFencedBacktick = regexp.MustCompile("(?s)```.*?```")
	judgeFencedTilde    = regexp.MustCompile("(?s)~~~.*?~~~")
	judgeInlineCode     = regexp.MustCompile("`[^`\\n]*`")
	judgeGenericHTMLTag = regexp.MustCompile(`(?s)<[A-Za-z/][^>]*>`)
	judgeAtReference    = regexp.MustCompile(`(?m)@[A-Za-z0-9_./:\\-]+[^\n]*`)
	judgeAtBracketRef   = regexp.MustCompile(`(?m)@<[^>\n]*>[^\n]*`)
	judgeAtQuotedRef    = regexp.MustCompile(`(?m)@"[^"\n]*"[^\n]*`)
	numberedStepLine    = regexp.MustCompile(`(?m)^\s*\d+[.)、]`)
)

// judgeInjectionTags 是框架用来注入非用户本人文本的包装标签。整个元素（含主体）
// 都会被丢弃，这样附件中形似步骤或代码块的内容就无法影响判定。
var judgeInjectionTags = []string{
	"system-reminder", "system_reminder", "project-memory", "file",
	"attachment", "attachments", "document", "context", "resource",
	"resources", "mcp", "tool_result", "tool-result", "function_results",
	"injected", "environment",
}

var judgeInjectionPatterns = func() []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(judgeInjectionTags)*2)
	for _, name := range judgeInjectionTags {
		out = append(out,
			regexp.MustCompile(`(?is)<`+name+`\b[^>]*>.*?</`+name+`\s*>`),
			regexp.MustCompile(`(?is)<`+name+`\b[^>]*/>`),
		)
	}
	return out
}()

// stripPromptNonUserText 从提示中剥离非用户正文内容。顺序与 agent 包一致：
// 代码块与注入块先于 @-引用处理，通用标签最后，确保标签周围的用户原文得以保留。
func stripPromptNonUserText(prompt string) string {
	s := judgeFencedBacktick.ReplaceAllString(prompt, "")
	s = judgeFencedTilde.ReplaceAllString(s, "")
	s = judgeInlineCode.ReplaceAllString(s, "")
	for _, re := range judgeInjectionPatterns {
		s = re.ReplaceAllString(s, "")
	}
	s = judgeAtReference.ReplaceAllString(s, "")
	s = judgeAtBracketRef.ReplaceAllString(s, "")
	s = judgeAtQuotedRef.ReplaceAllString(s, "")
	s = judgeGenericHTMLTag.ReplaceAllString(s, "")
	return s
}
