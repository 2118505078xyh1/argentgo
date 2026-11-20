package agent

import "strings"

// 卡住检测源自 OpenHands 的 StuckDetector：它在一段有界的运行事件尾部中查找重复循环，
// 并给出一次性的纠正提示，而不是默默地消耗轮次预算。它只是对现有 Event 词汇的纯观察器——
// 没有定时器、没有 I/O，也不与循环的重复防护或错误追踪耦合。

const (
	// stuckWindow 是检查的最近事件数量。OpenHands 使用 20 个事件的窗口；
	// 相同的上限使检测保持局部且可预测。
	stuckWindow = 20
	// 各可识别模式的阈值。
	stuckRepeatCalls    = 3 // 连续相同的工具调用
	stuckErrorStreak    = 5 // 连续的错误步骤
	stuckAlternationLen = 4 // A/B/A/B... 形式的工具调用
	stuckMonologueLen   = 5 // 没有工具调用的助手文本步骤
)

// steeringNoticePrefix 对应 injectSteering 为每条移入对话的用户消息所发出的通知。
// 这是唯一能从事件流中识别出的用户消息，因此它标记了一个情节（episode）边界。
const steeringNoticePrefix = "steering: "

// 一次性提示文本。与其他循环通知保持一致，保持简洁并使用英文；
// 模型会把它们当作一条新的用户轮次来阅读。
const (
	stuckGuidanceRepeat = "Repeating the exact same call again will not work; change the tool, arguments, or approach before retrying."
	stuckGuidanceError  = "Several consecutive errors occurred; stop retrying and diagnose the underlying cause first."
	stuckGuidanceAlt    = "The recent calls keep alternating between two actions without progress; commit to one approach and verify the result."
	stuckGuidanceText   = "A long explanation without tool calls made no progress; take a concrete action or finish the answer."
)

// StuckDetector 监视事件中的重复循环。它只记录当前情节是否已提示过，
// 因此每个情节最多发出一次指引，直到一次健康的观察（或 Reset）重新武装它。
type StuckDetector struct {
	nudged bool
	// lastNudgedIndex 是提示触发时已看到的事件数。保留它是为了与 OpenHands 的
	// 最近提示事件去重机制保持一致，并便于调试检测出循环的确切位置。
	lastNudgedIndex int
}

// Reset 清除一次性状态，使下一次卡住的情节可以得到提示。
func (d *StuckDetector) Reset() {
	if d == nil {
		return
	}
	d.nudged = false
	d.lastNudgedIndex = 0
}

// Observe 检查最近的事件，并在某个情节中首次发现卡住模式时返回纠正指引。
// 即使一次性提示已经触发，stuck 仍会报告模式是否存在；调用方应以 guidance != "" 为门槛，
// 确保提示只投递一次。
func (d *StuckDetector) Observe(events []Event) (guidance string, stuck bool) {
	if d == nil {
		return "", false
	}
	window := events
	if len(window) > stuckWindow {
		window = window[len(window)-stuckWindow:]
	}
	if start := lastUserBoundary(window); start >= 0 {
		window = window[start+1:]
	}
	if len(window) == 0 {
		// 暂无可判断的内容：视为健康，使新情节处于待武装状态。
		d.nudged = false
		return "", false
	}

	guidance, stuck = detectStuck(window)
	if !stuck {
		// 健康的观察会结束当前情节并重新武装一次性提示。
		d.nudged = false
		return "", false
	}
	if d.nudged {
		return "", true
	}
	d.nudged = true
	d.lastNudgedIndex = len(events)
	return guidance, true
}

// lastUserBoundary 返回事件中最后一条可识别用户消息的索引，没有则返回 -1。
// 该位置之前的模式属于之前的情节，不得触发新的提示。
func lastUserBoundary(events []Event) int {
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Kind == EventNotice && strings.HasPrefix(e.Text, steeringNoticePrefix) {
			return i
		}
	}
	return -1
}

// stuckCall 是去掉易变字段后的一次工具调用身份：不含调用 id、轮次号、耗时或用量，
// 只保留决定"是否同一调用"的内容。参数经 NormalizeArgs 规范化，
// 因此同一 JSON 对象的两种写法（例如键顺序不同）算作同一个调用。
type stuckCall struct {
	name    string
	args    string
	isError bool
}

type stuckStepKind uint8

const (
	stepOther stuckStepKind = iota
	stepCall
	stepError
	stepText
)

// stuckStep 是把一个有意义的事件压缩为检测所需字段后的结果。
type stuckStep struct {
	kind    stuckStepKind
	name    string
	args    string
	isError bool
}

// detectStuck 返回第一个匹配模式的指引。模式顺序是刻意安排的：重复调用是最清晰的信号，
// 其次是交替调用，然后是单纯的错误连续，最后是无工具的独白。
func detectStuck(window []Event) (string, bool) {
	steps, calls := buildSteps(window)
	if hasRepeatCall(calls, stuckRepeatCalls) {
		return stuckGuidanceRepeat, true
	}
	if hasAlternation(calls, stuckAlternationLen) {
		return stuckGuidanceAlt, true
	}
	if hasErrorStreak(steps, stuckErrorStreak) {
		return stuckGuidanceError, true
	}
	if hasMonologue(window, stuckMonologueLen) {
		return stuckGuidanceText, true
	}
	return "", false
}

// buildSteps 在窗口内按调用 id 把 tool_start/tool_end 事件配对，
// 同时返回完整的有意义步骤序列，以及供重复模式使用的工具调用子序列。
func buildSteps(window []Event) ([]stuckStep, []stuckCall) {
	starts := make(map[string]stuckCall, 4)
	steps := make([]stuckStep, 0, len(window))
	calls := make([]stuckCall, 0, len(window)/2)
	for _, e := range window {
		switch e.Kind {
		case EventToolStart:
			// 规范化参数，使仅键顺序不同的差异不会掩盖重复；
			// 重复防护器使用相同的规范化方式。
			starts[e.ToolCallID] = stuckCall{name: e.ToolName, args: NormalizeArgs(e.Args)}
		case EventToolEnd:
			c := stuckCall{name: e.ToolName, isError: e.IsError}
			if start, ok := starts[e.ToolCallID]; ok {
				c = start
				c.isError = e.IsError
				delete(starts, e.ToolCallID)
			}
			steps = append(steps, stuckStep{kind: stepCall, name: c.name, args: c.args, isError: c.isError})
			calls = append(calls, c)
		case EventError:
			steps = append(steps, stuckStep{kind: stepError})
		case EventTextDelta:
			steps = append(steps, stuckStep{kind: stepText})
		}
	}
	return steps, calls
}

// sameCall 只比较稳定的身份字段，忽略 id 和计时。
func sameCall(a, b stuckCall) bool {
	return a.name == b.name && a.args == b.args && a.isError == b.isError
}

// hasRepeatCall 报告是否存在 minCalls 个或更多相邻且相同的调用。
func hasRepeatCall(calls []stuckCall, minCalls int) bool {
	if len(calls) < minCalls {
		return false
	}
	run := 1
	for i := 1; i < len(calls); i++ {
		if sameCall(calls[i], calls[i-1]) {
			run++
		} else {
			run = 1
		}
		if run >= minCalls {
			return true
		}
	}
	return false
}

// hasAlternation 报告调用序列的尾部是否至少在 minCalls 个调用中，
// 在两个不同的调用之间交替（A/B/A/B...）。
func hasAlternation(calls []stuckCall, minCalls int) bool {
	if len(calls) < minCalls {
		return false
	}
	run := 1
	for i := 1; i < len(calls); i++ {
		if i >= 2 && sameCall(calls[i], calls[i-2]) && !sameCall(calls[i], calls[i-1]) {
			run++
		} else {
			run = 2
		}
		if run >= minCalls {
			return true
		}
	}
	return false
}

// isErrorStep 报告某个步骤是否表示失败：提供方错误，或返回了 IsError 的工具调用。
func isErrorStep(s stuckStep) bool {
	return s.kind == stepError || (s.kind == stepCall && s.isError)
}

// hasErrorStreak 报告是否存在 minSteps 个或更多连续失败的步骤。
func hasErrorStreak(steps []stuckStep, minSteps int) bool {
	run := 0
	for _, s := range steps {
		if isErrorStep(s) {
			run++
			if run >= minSteps {
				return true
			}
		} else {
			run = 0
		}
	}
	return false
}

// hasMonologue 报告是否出现了 minSteps 个或更多文本增量且其间没有工具调用。
// 其他事件类型（轮次边界、用量、通知）是中性的，不会打断这一连续序列。
func hasMonologue(window []Event, minSteps int) bool {
	run := 0
	for _, e := range window {
		switch e.Kind {
		case EventTextDelta:
			if strings.TrimSpace(e.Text) != "" {
				run++
				if run >= minSteps {
					return true
				}
			}
		case EventToolStart, EventToolEnd:
			run = 0
		}
	}
	return false
}
