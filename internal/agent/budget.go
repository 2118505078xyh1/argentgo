package agent

import (
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/tokenest"
)

// 停止原因，记录在 Result.Stopped 和 Event.Reason 中。它们是稳定的
// 字符串，因此前端和测试可以直接比较。
const (
	// StopComplete 表示模型已作答，没有请求更多工具。
	StopComplete = "complete"
	// StopMaxTurns 表示轮次预算已耗尽。
	StopMaxTurns = "max_turns"
	// StopMaxPromptTokens 表示某个请求将超过 MaxPromptTokens。
	StopMaxPromptTokens = "max_prompt_tokens"
	// StopMaxTotalTokens 表示本次运行的 prompt+completion 总量达到了上限。
	StopMaxTotalTokens = "max_total_tokens"
	// StopDeclaredTurnBudget 表示用户在提示中声明的单轮上限（见 ParseTurnBudget）
	// 已被触及。它需要经由 Budget 字段选择性启用。
	StopDeclaredTurnBudget = "declared_turn_budget"
	// StopMaxCost 表示估算花费达到了 MaxCostUSD。
	StopMaxCost = "max_cost"
	// StopCancelled 表示上下文已被取消。
	StopCancelled = "cancelled"
	// StopProviderError 表示提供方调用失败；Run 也会返回一个错误。
	StopProviderError = "provider_error"
	// StopStuck 保留用于被"卡住检测"中止的运行。检测默认只做提示而不停止，
	// 因此目前还没有停止逻辑会用到它。
	StopStuck = "stuck"
)

// Price 表示提供方对某模型每百万 token 的收费。
type Price struct {
	// Input 是每百万提示 token 的美元价格。
	Input float64
	// Output 是每百万补全 token 的美元价格。
	Output float64
}

// Cost 估算一次用量快照的花费。缓存的提示 token 按普通提示 token
// 计费；不建模提供方的缓存折扣。
func (p Price) Cost(u llm.Usage) float64 {
	return float64(u.PromptTokens)/1_000_000*p.Input +
		float64(u.CompletionTokens)/1_000_000*p.Output
}

// PriceFor 在价格表中查找模型，找不到时回退到 "" 条目。
func PriceFor(prices map[string]Price, model string) (Price, bool) {
	if len(prices) == 0 {
		return Price{}, false
	}
	if p, ok := prices[model]; ok {
		return p, true
	}
	p, ok := prices[""]
	return p, ok
}

// Budget 限制一次运行。字段值为零表示不限制。
type Budget struct {
	// MaxTurns 限制模型请求的数量。
	MaxTurns int
	// MaxPromptTokens 限制单次请求的提示大小（以 token 计）。
	// 会先执行裁剪；当裁剪无能为力时，这是硬性停止线。
	MaxPromptTokens int
	// MaxTotalTokens 限制累计的 prompt+completion token 数。
	MaxTotalTokens int
	// MaxCostUSD 使用 Options.Prices 限制估算花费。
	MaxCostUSD float64
	// DeclaredTurnTokens 是用户在提示中声明的单轮输出预算（用 ParseTurnBudget 解析）。
	// 零表示未声明。它是一个额外的累计 token 上限，绝不会放宽其他上限。
	DeclaredTurnTokens int64
}

// Breach 返回第一个被突破的限制，以稳定的原因字符串和人类可读的明细行表示。
// 当运行可以继续时 ok 为 false。
//
// lastPromptTokens 是最近一次请求的提示大小（由提供方报告或估算得出）；
// 当没有匹配到价格时 costKnown 为 false。
func (b Budget) Breach(used llm.Usage, lastPromptTokens int, spentUSD float64, costKnown bool) (reason, detail string, ok bool) {
	if b.DeclaredTurnTokens > 0 {
		total := int64(used.PromptTokens) + int64(used.CompletionTokens)
		if total >= b.DeclaredTurnTokens {
			return StopDeclaredTurnBudget,
				fmt.Sprintf("%s: %d tokens used (declared limit %d)", StopDeclaredTurnBudget, total, b.DeclaredTurnTokens),
				true
		}
	}
	if b.MaxTotalTokens > 0 {
		total := used.PromptTokens + used.CompletionTokens
		if total >= b.MaxTotalTokens {
			return StopMaxTotalTokens,
				fmt.Sprintf("%s: %d tokens used (limit %d)", StopMaxTotalTokens, total, b.MaxTotalTokens),
				true
		}
	}
	if b.MaxCostUSD > 0 && costKnown && spentUSD >= b.MaxCostUSD {
		return StopMaxCost,
			fmt.Sprintf("%s: $%.4f spent (limit $%.4f)", StopMaxCost, spentUSD, b.MaxCostUSD),
			true
	}
	if b.MaxPromptTokens > 0 && lastPromptTokens >= b.MaxPromptTokens {
		return StopMaxPromptTokens,
			fmt.Sprintf("%s: prompt %d tokens (limit %d)", StopMaxPromptTokens, lastPromptTokens, b.MaxPromptTokens),
			true
	}
	return "", "", false
}

// 上下文感知的压缩阈值。优先使用模型上下文窗口的百分比作为触发条件；
// 对于未公布上下文窗口的配置，则回退到 token 常量。
const (
	defaultCompactionTokens = 32000
	// reservedOutputFraction 为摘要后提示所对应的回复预留窗口的 1/20；
	// reservedOutputTokens 为该预留量设置上限。
	reservedOutputFraction = 20
	reservedOutputTokens   = 8000
	// 估算膨胀系数用整数表示为 1.3 倍：ceil(n*13/10)。
	estimateInflationNumerator   = 13
	estimateInflationDenominator = 10
)

// CompactionThreshold 返回自动压缩的估算 token 触发阈值。
//
// percent 为正时优先：触发阈值为 floor(contextWindow*percent/100)
// 减去大小为 min(contextWindow/20, 8000) 的预留输出缓冲，且至少为 1。
// 否则按原样使用为正的 configuredTokens，编译时的默认值作为最后兜底。
// 非正的 percent 值（-1/0）继承全局阈值，与按 profile 配置的语义一致。
func CompactionThreshold(contextWindow, configuredTokens, percent int) int {
	if percent > 0 {
		reserve := contextWindow / reservedOutputFraction
		if reserve > reservedOutputTokens {
			reserve = reservedOutputTokens
		}
		// 使用 int64 相乘可避免在 /100 之前对大窗口发生溢出。
		threshold := int(int64(contextWindow)*int64(percent)/100) - reserve
		if threshold < 1 {
			return 1
		}
		return threshold
	}
	if configuredTokens > 0 {
		return configuredTokens
	}
	return defaultCompactionTokens
}

// InflateEstimate 应用在把估算提示大小与压缩阈值比较之前使用的 1.3 倍安全系数。
// 仅使用整数运算并向上取整（ceil），因此 n=1 变为 2，n=100 变为 130。
func InflateEstimate(n int) int {
	if n <= 0 {
		return 0
	}
	return int((int64(n)*estimateInflationNumerator + estimateInflationDenominator - 1) / estimateInflationDenominator)
}

// sliceForChild 从父运行的预算派生子运行的预算：每项限制减半，
// 这样被委派者就无法花光父级的全部额度。
func (b Budget) sliceForChild() Budget {
	out := Budget{}
	if b.MaxTurns > 0 {
		out.MaxTurns = max(1, b.MaxTurns/2)
	}
	if b.MaxPromptTokens > 0 {
		out.MaxPromptTokens = max(1, b.MaxPromptTokens/2)
	}
	if b.MaxTotalTokens > 0 {
		out.MaxTotalTokens = max(1, b.MaxTotalTokens/2)
	}
	if b.MaxCostUSD > 0 {
		out.MaxCostUSD = b.MaxCostUSD / 2
	}
	if b.DeclaredTurnTokens > 0 {
		out.DeclaredTurnTokens = max(int64(1), b.DeclaredTurnTokens/2)
	}
	return out
}

// 对用户声明的轮次预算施加的限制。超出范围的声明会被收紧而非拒绝，
// 这样一次笔误就不会使该功能失效。
const (
	turnBudgetMinTokens = int64(1_000)
	turnBudgetMaxTokens = int64(100_000_000)
)

// 在查找声明之前，用于移除所有非用户本人文本内容的模式。RE2 不支持反向引用，
// 因此每种注入容器都由各自编译的模式来匹配（见下方构建）。
var (
	fencedCodeBacktick = regexp.MustCompile("(?s)```.*?```")
	fencedCodeTilde    = regexp.MustCompile("(?s)~~~.*?~~~")
	inlineCode         = regexp.MustCompile("`[^`\\n]*`")
	genericHTMLTag     = regexp.MustCompile(`(?s)<[A-Za-z/][^>]*>`)
	atReference        = regexp.MustCompile(`(?m)@[A-Za-z0-9_./:\\-]+[^\n]*`)
	atBracketReference = regexp.MustCompile(`(?m)@<[^>\n]*>[^\n]*`)
	atQuotedReference  = regexp.MustCompile(`(?m)@"[^"\n]*"[^\n]*`)

	// plusBudget 匹配 "+500k" / "+2m" 简写（k = 千，m = 百万）。
	// 正号使得正文里单独的 "500k" 可以安全地忽略，
	// 而后缀的词边界会拒绝 "500miles" 这类内容。
	plusBudget = regexp.MustCompile(`\+\s*(\d+(?:\.\d+)?)\s*([kKmM])\b`)
	// verbBudget 匹配 "use 2m tokens" 这类短语。必须同时出现动词和名词
	// "tokens"，这样普通数量就不会被当成声明。
	verbBudget = regexp.MustCompile(`(?i)\b(?:use|spend|budget|limit|cap|with|set|allow|allocate|reserve)\s+(\d+(?:\.\d+)?)\s*([kKmM])\s+(?:(?:output|total|context)\s+)?tokens?\b`)
)

// injectionBlockTags 是框架用来注入非用户本人文本的包装标签。整个元素（包括其主体）
// 都会被丢弃，这样附件中形似预算的数字就无法设置上限。
var injectionBlockTags = []string{
	"system-reminder", "system_reminder", "project-memory", "file",
	"attachment", "attachments", "document", "context", "resource",
	"resources", "mcp", "tool_result", "tool-result", "function_results",
	"injected", "environment",
}

var injectionBlockPatterns = func() []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(injectionBlockTags)*2)
	for _, name := range injectionBlockTags {
		out = append(out,
			regexp.MustCompile(`(?is)<`+name+`\b[^>]*>.*?</`+name+`\s*>`),
			regexp.MustCompile(`(?is)<`+name+`\b[^>]*/>`),
		)
	}
	return out
}()

// ParseTurnBudget 提取用户为当前轮次声明的输出预算。可识别两种写法，
// 均不区分大小写且支持小数："+500k"（也支持 "+2m"、"+1.5m"）和
// "use 2m tokens"（带一组有限的预算动词，以及可选的
// "output"/"total"/"context" 限定词）。用户文本中最靠左的声明优先。
//
// 只检查用户本人的文本。代码段、<system-reminder> 及其他注入的 XML 块、
// @file/MCP 引用和其余 HTML 标签都会先被剥离，因此附件中的数字无法设置预算。
// 结果会被收紧到 [1k, 100m]；没有声明时 ok 为 false。
func ParseTurnBudget(prompt string) (tokens int64, ok bool) {
	text := stripNonUserText(prompt)

	// 找出两种可识别形式中最靠左的那个。
	best := len(text) + 1
	var number, suffix string
	if loc := plusBudget.FindStringSubmatchIndex(text); loc != nil {
		best, number, suffix = loc[0], text[loc[2]:loc[3]], text[loc[4]:loc[5]]
	}
	if loc := verbBudget.FindStringSubmatchIndex(text); loc != nil && loc[0] < best {
		best, number, suffix = loc[0], text[loc[2]:loc[3]], text[loc[4]:loc[5]]
	}
	if best > len(text) {
		return 0, false
	}

	tokens = parseTokenCount(number, suffix)
	if tokens < turnBudgetMinTokens {
		tokens = turnBudgetMinTokens
	} else if tokens > turnBudgetMaxTokens {
		tokens = turnBudgetMaxTokens
	}
	return tokens, true
}

// stripNonUserText 从提示中移除非正文内容。顺序很重要：
// 代码块和注入块先于 @-引用处理，通用标签最后处理，
// 这样标签周围的用户原文才能保留下来。
func stripNonUserText(prompt string) string {
	s := fencedCodeBacktick.ReplaceAllString(prompt, "")
	s = fencedCodeTilde.ReplaceAllString(s, "")
	s = inlineCode.ReplaceAllString(s, "")
	for _, re := range injectionBlockPatterns {
		s = re.ReplaceAllString(s, "")
	}
	s = atReference.ReplaceAllString(s, "")
	s = atBracketReference.ReplaceAllString(s, "")
	s = atQuotedReference.ReplaceAllString(s, "")
	s = genericHTMLTag.ReplaceAllString(s, "")
	return s
}

// parseTokenCount 把 "1.5" + "m" 转换为 1_500_000。整数运算避免浮点误差；
// 超大输入会在 ParseTurnBudget 收紧之前先行饱和，
// 因此再荒谬的数字也不会使 int64 溢出。
func parseTokenCount(number, suffix string) int64 {
	multiplier := int64(1)
	switch suffix {
	case "k", "K":
		multiplier = 1_000
	case "m", "M":
		multiplier = 1_000_000
	}

	const saturation = int64(1 << 62)
	parts := strings.SplitN(number, ".", 2)
	var total int64
	for _, c := range parts[0] {
		d := int64(c - '0')
		if total > (saturation-d)/10 {
			return saturation
		}
		total = total*10 + d
	}
	if total > saturation/multiplier {
		return saturation
	}
	total *= multiplier

	if len(parts) == 2 {
		frac := parts[1]
		if len(frac) > 6 {
			frac = frac[:6]
		}
		var f int64
		for _, c := range frac {
			f = f*10 + int64(c-'0')
		}
		denom := int64(1)
		for range frac {
			denom *= 10
		}
		add := f * multiplier / denom
		if total > saturation-add {
			return saturation
		}
		total += add
	}
	return total
}

// estimateBatchMin 是使用并行批量估算的最小消息数。低于它时走原有的
// EstimateMessages，保证既有小会话的预算判定逐字节不变。
const estimateBatchMin = 16

// EstimateMessagesBatch 在批量场景（消息数 >= estimateBatchMin）用
// internal/tokenest 的单缓冲 + 并行计数估算整段对话；小批量委托给
// EstimateMessages。框架开销（角色、工具调用、多模态 part）与
// EstimateMessage 保持一致，只是正文扫描走批量路径。
//
// 启发式与 tokenize 在 ASCII/宽 rune 上对齐、对数字与标点略微高估；
// 高估对预算是安全方向（见 tokenize 包注释），不会造成上下文溢出。
func EstimateMessagesBatch(messages []llm.Message) int {
	if len(messages) < estimateBatchMin {
		return EstimateMessages(messages)
	}
	items := make([]tokenest.Item, 0, len(messages)*2)
	for _, m := range messages {
		items = append(items,
			tokenest.Item{Kind: m.Role, Text: m.Content},
			tokenest.Item{Kind: m.Role, Text: m.Reasoning},
		)
		for _, call := range m.ToolCalls {
			items = append(items,
				tokenest.Item{Kind: "tool_call", Text: call.Function.Name},
				tokenest.Item{Kind: "tool_call", Text: call.Function.Arguments},
			)
		}
	}
	total, _ := tokenest.EstimateItems(items)
	// 加回与 EstimateMessage 相同的框架开销：每条消息 messageOverheadTokens，
	// 每个工具调用 2，以及多模态 part 的估算。全部走饱和加法避免溢出。
	for _, m := range messages {
		total = satAddInt(total, messageOverheadTokens)
		total = satAddInt(total, estimateMessageContentParts(m))
		for range m.ToolCalls {
			total = satAddInt(total, 2)
		}
	}
	return total
}

func satAddInt(a, b int) int {
	if b > 0 && a > math.MaxInt-b {
		return math.MaxInt
	}
	if b < 0 && a < math.MinInt-b {
		return math.MinInt
	}
	return a + b
}
