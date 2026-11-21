package tui

// RenderSignal 是"权威状态 → 渲染信号"的投影结果：它只描述界面此刻
// 应当呈现的三件事，不持有任何 Model 状态，也不触发任何副作用。
//
//   - ShowSpinner：是否展示转动指示器（模型推理/流式输出/工具执行时为真）。
//   - AcceptInput：输入框是否接受用户文本输入；回合进行中或等待审批时为假。
//   - StatusText：状态栏文案。这里刻意写成稳定的中文常量而不读取 locale，
//     因为本类型目前是零调用方的纯原语；文案日后可整批迁入 locale 表。
//
// 零值 RenderSignal 表示"没有任何信号"，是 MergeSignals 中的"缺失"哨兵。
type RenderSignal struct {
	ShowSpinner bool
	AcceptInput bool
	StatusText  string
}

// 投影使用的稳定文案。集中为常量便于日后整批迁往 localeStrings，
// 无需改动 ProjectRender 的控制流。
const (
	statusReady       = "就绪"
	statusWelcome     = "请输入消息开始对话"
	statusThinking    = "思考中…"
	statusToolRunning = "工具执行中…"
	statusAnswering   = "正在答复…"
	statusApproval    = "等待确认…"
)

// ProjectRender 把权威回合状态投影为渲染信号。它是纯函数：不修改 st、
// 不读取时钟、不触碰 Model，因此同一输入永远得到同一输出。
//
// 阶段映射（st.Phase）：
//
//   - TurnIdle（含"作答完成后回到空闲"的情形）：可输入、无 spinner。有
//     历史消息（itemCount > 0）时文案为"就绪"，否则给出首次引导文案。
//   - TurnThinking / TurnAnswering（流式输出）：spinner、不可输入。
//   - TurnToolRunning（等待工具）：spinner、不可输入。
//   - 未知阶段：保守当作活动中（spinner、不可输入），避免把非法阶段误判
//     为空闲而放开输入。
//
// hasPendingApproval 优先于阶段：有待确认项时显示审批提示并屏蔽文本输
// 入（用户应通过审批弹窗回应），且不再转 spinner——此时系统在等用户，
// 而非在做功。itemCount 只影响空闲态的文案选择。
func ProjectRender(st TurnState, hasPendingApproval bool, itemCount int) RenderSignal {
	if hasPendingApproval {
		return RenderSignal{StatusText: statusApproval}
	}
	switch st.Phase {
	case TurnIdle:
		if itemCount > 0 {
			return RenderSignal{AcceptInput: true, StatusText: statusReady}
		}
		return RenderSignal{AcceptInput: true, StatusText: statusWelcome}
	case TurnThinking:
		return RenderSignal{ShowSpinner: true, StatusText: statusThinking}
	case TurnToolRunning:
		return RenderSignal{ShowSpinner: true, StatusText: statusToolRunning}
	case TurnAnswering:
		return RenderSignal{ShowSpinner: true, StatusText: statusAnswering}
	default:
		return RenderSignal{ShowSpinner: true, StatusText: statusThinking}
	}
}

// MergeSignals 用 fallback 填补 primary 中"缺失"的字段，实现"新状态缺
// 失时回退旧 working 布尔"的过渡策略。
//
// "非空"的判定遵循各字段零值：bool 的 true、以及字符串非 "" 视为有值。
// 因此 primary 的 true 与非空文案优先，其余字段取 fallback。注意 bool 的
// false 无法表达"显式关闭"——它会被当作缺失而回退；需要显式关闭的调用
// 方应传入字段完整的 primary（例如直接来自 ProjectRender 的结果）。
//
// 纯函数：两个入参都不修改；零值 primary 退化为 fallback，零值 fallback
// 退化为 primary。
func MergeSignals(primary, fallback RenderSignal) RenderSignal {
	out := primary
	if !out.ShowSpinner {
		out.ShowSpinner = fallback.ShowSpinner
	}
	if !out.AcceptInput {
		out.AcceptInput = fallback.AcceptInput
	}
	if out.StatusText == "" {
		out.StatusText = fallback.StatusText
	}
	return out
}
