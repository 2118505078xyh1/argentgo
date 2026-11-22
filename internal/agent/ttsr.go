package agent

import (
	"errors"
	"fmt"
	"regexp"
)

// 本文件实现「TTSR（Token-Triggered Steering Rule）流式规则注入」的判定纯核心：
// 平时规则以正则形式蛰伏、零成本；token 流逐段喂入 Feed，命中即返回裁决，
// 由上层决定中止当前流、注入系统提醒并从同点重试（重试时调用 Reset）。
//
// 本文件刻意不接线：不感知流式循环、事件、注册表或 llm 协议，也不引入任何依赖
// （仅用标准库 regexp/errors/fmt）。所有状态都封装在 TTSRWatcher 实例中，
// 无全局变量、无 goroutine，窗口按 rune 截断，CJK 安全。

// TTSRScope 是规则作用的 token 流作用域。一条规则只对同 scope 的 delta 生效。
type TTSRScope string

const (
	TTSRScopeText     TTSRScope = "text"     // 正文流
	TTSRScopeThinking TTSRScope = "thinking" // 思考流
	TTSRScopeTool     TTSRScope = "tool"     // 工具调用/结果流
)

// TTSRAction 是命中规则后建议上层采取的动作，按干预强度从弱到强排列。
type TTSRAction string

const (
	TTSRWarn   TTSRAction = "warn"   // 仅记录/提醒，不改变流
	TTSRInject TTSRAction = "inject" // 注入系统提醒后继续
	TTSRRetry  TTSRAction = "retry"  // 中止当前流，从同点重试
)

// TTSRRule 是一条流式规则的静态声明。Pattern 为 Go regexp 表达式；
// Reminder 是命中时携带、供上层注入的系统提醒文本。
type TTSRRule struct {
	Name     string
	Scope    TTSRScope
	Pattern  string
	Action   TTSRAction
	Reminder string
}

// TTSRVerdict 是一次命中的裁决结果。Matched 为命中的子串（有界截断），
// Reminder 直接透出规则声明的提醒文本，便于上层注入而无需回查规则表。
type TTSRVerdict struct {
	RuleName string
	Action   TTSRAction
	Reminder string
	Matched  string
}

// CompiledTTSRRule 是一条已编译规则的运行时表示：Rule 为原始声明，
// Re 为其 Pattern 编译后的正则。
type CompiledTTSRRule struct {
	Rule TTSRRule
	Re   *regexp.Regexp
}

// compiledTTSRRule 是 CompiledTTSRRule 的包内别名。
// 它让 CompileTTSRRules 保留未导出的签名类型，同时与 NewTTSRWatcher 的
// 导出参数类型 []CompiledTTSRRule 完全同一，二者可直接拼接接线。
type compiledTTSRRule = CompiledTTSRRule

// 默认值与上限。参数 <= 0 时取默认值；Matched 上限按 rune 计，避免超长子串。
const (
	defaultTTSRWindowRunes   = 2000
	defaultTTSRCooldownBytes = 4000
	ttsrMatchedMaxRunes      = 64
)

// CompileTTSRRules 编译每条规则的正则。非法正则的规则被跳过而不是让整体失败，
// 所有失败通过 errors.Join 汇总在返回的 error 中；合法规则保持原始顺序返回。
// 全部合法时 error 为 nil；输入为空时返回空切片与 nil。
func CompileTTSRRules(rules []TTSRRule) ([]compiledTTSRRule, error) {
	out := make([]compiledTTSRRule, 0, len(rules))
	var errs []error
	for i, r := range rules {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			errs = append(errs, fmt.Errorf("ttsr 规则 %q（第 %d 条）正则非法 %q: %w", r.Name, i, r.Pattern, err))
			continue
		}
		out = append(out, CompiledTTSRRule{Rule: r, Re: re})
	}
	return out, errors.Join(errs...)
}

// TTSRWatcher 是流式规则判定的有状态观察器。每个 scope 独立维护一个有界滚动窗口；
// 每条命中的规则在 cooldownBytes 个字节内不再报，避免同一段内容每来一个 token 都触发。
// 它可被并发不安全地复用：单实例供单一流串行调用，重试时由上层调用 Reset 清空状态。
type TTSRWatcher struct {
	rules         []CompiledTTSRRule
	windowRunes   int
	cooldownBytes int

	windows  map[TTSRScope][]rune // scope -> 最近 windowRunes 个 rune
	cooldown map[string]int       // 规则名 -> 剩余冷却字节数
}

// NewTTSRWatcher 用已编译规则构造观察器。windowRunes <= 0 取 2000，
// cooldownBytes <= 0 取 4000。规则名应唯一；同名规则共享冷却计数。
func NewTTSRWatcher(rules []CompiledTTSRRule, windowRunes, cooldownBytes int) *TTSRWatcher {
	if windowRunes <= 0 {
		windowRunes = defaultTTSRWindowRunes
	}
	if cooldownBytes <= 0 {
		cooldownBytes = defaultTTSRCooldownBytes
	}
	return &TTSRWatcher{
		rules:         rules,
		windowRunes:   windowRunes,
		cooldownBytes: cooldownBytes,
		windows:       make(map[TTSRScope][]rune),
		cooldown:      make(map[string]int),
	}
}

// Feed 把一段 token 增量喂入指定 scope，返回最强动作的命中裁决。
// 行为顺序：先按本 scope 的 delta 字节数推进冷却，再把 delta 累积到有界窗口，
// 最后在该 scope 的规则上跑正则。未命中返回零值与 false；命中时该规则进入冷却。
// delta 为空时不产生任何状态变化，直接返回未命中。
func (w *TTSRWatcher) Feed(scope TTSRScope, delta string) (TTSRVerdict, bool) {
	if w == nil || delta == "" {
		return TTSRVerdict{}, false
	}
	n := len(delta) // 冷却按字节计

	// 冷却按 delta 字节数递减，只消耗命中规则所属 scope 的 token。
	for _, c := range w.rules {
		left, ok := w.cooldown[c.Rule.Name]
		if !ok || c.Rule.Scope != scope {
			continue
		}
		if left > n {
			w.cooldown[c.Rule.Name] = left - n
		} else {
			delete(w.cooldown, c.Rule.Name)
		}
	}

	// 累积窗口并按 rune 截断，保证 CJK 不被切断在 UTF-8 字节中间。
	win := append(w.windows[scope], []rune(delta)...)
	if len(win) > w.windowRunes {
		win = win[len(win)-w.windowRunes:]
	}
	w.windows[scope] = win
	window := string(win)

	// 同一 delta 多条命中取最强动作；强度相同取编译顺序靠前者，保证确定性。
	var best TTSRVerdict
	bestRank := 0
	found := false
	for _, c := range w.rules {
		if c.Rule.Scope != scope {
			continue
		}
		if _, cooling := w.cooldown[c.Rule.Name]; cooling {
			continue
		}
		loc := c.Re.FindStringIndex(window)
		if loc == nil {
			continue
		}
		rank := ttsrActionRank(c.Rule.Action)
		if found && rank <= bestRank {
			continue
		}
		best = TTSRVerdict{
			RuleName: c.Rule.Name,
			Action:   c.Rule.Action,
			Reminder: c.Rule.Reminder,
			Matched:  boundTTSRMatched(window[loc[0]:loc[1]]),
		}
		bestRank = rank
		found = true
	}

	if found {
		// 只有实际上报的规则进入冷却；其余同时命中但未被选中的规则留给后续 delta。
		w.cooldown[best.RuleName] = w.cooldownBytes
		return best, true
	}
	return TTSRVerdict{}, false
}

// Reset 清空所有 scope 的窗口与冷却。上层在从同点重试时调用，
// 使规则可以针对重试后的流重新判定。
func (w *TTSRWatcher) Reset() {
	if w == nil {
		return
	}
	w.windows = make(map[TTSRScope][]rune)
	w.cooldown = make(map[string]int)
}

// ttsrActionRank 把动作映射为强度，用于同一 delta 多命中的优先级比较：
// retry(3) > inject(2) > warn(1) > 未知动作(0)。
func ttsrActionRank(a TTSRAction) int {
	switch a {
	case TTSRWarn:
		return 1
	case TTSRInject:
		return 2
	case TTSRRetry:
		return 3
	default:
		return 0
	}
}

// boundTTSRMatched 按 rune 把命中子串截到上限，避免超长匹配撑大裁决结果。
func boundTTSRMatched(s string) string {
	r := []rune(s)
	if len(r) > ttsrMatchedMaxRunes {
		return string(r[:ttsrMatchedMaxRunes])
	}
	return s
}
