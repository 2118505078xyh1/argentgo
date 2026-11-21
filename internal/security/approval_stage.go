package security

import (
	"sort"
	"strings"
)

// 本文件提供"分阶段审批"的纯函数状态机原语：把一次审批请求拆成三个阶段，
// 用一次前进操作（AdvanceStage）在阶段之间迁移，并把要记住的作用域整理成
// 可展示的摘要（ScopeSummary）。
//
// 这是全新且自包含的原语：只依赖标准库，无副作用，刻意不接入 Chain.Check，
// 也不改变既有"策略 → 审批 → 审计"安全链的任何行为。所有函数都是纯函数：
// 只读入参、返回新值，不修改调用方持有的切片或结构体。

// ApprovalStage 是分阶段审批当前所处的阶段。
//
// 三个阶段的含义：
//   - stageOnce：初始阶段，等待用户在 once/always/reject 之间做选择；
//   - stageConfirmScope：选择 always 后的二次确认阶段，先展示将记住的作用域，
//     只有 Confirmed 为真才真正提交；
//   - stageFeedback：选择 reject 后的反馈收集阶段，收集要喂回模型的文本。
type ApprovalStage int

const (
	// stageOnce 是初始阶段：等待用户在 once/always/reject 之间选择。
	stageOnce ApprovalStage = iota
	// stageConfirmScope 是"始终允许"的二次确认阶段，提交前必须先展示作用域。
	stageConfirmScope
	// stageFeedback 是"拒绝"后的反馈收集阶段。
	stageFeedback
)

// 用户在审批界面上可以做出的选择。Choice 用字符串而非枚举，便于直接承载
// UI/协议传来的值；这些常量列出被状态机识别的取值，未识别的取值一律"停留"
// 在当前阶段，绝不意外完成。
const (
	// ChoiceOnce 表示"仅此一次"：立即完成，不进入后续阶段。
	ChoiceOnce = "once"
	// ChoiceAlways 表示"始终允许"：进入二次确认阶段。
	ChoiceAlways = "always"
	// ChoiceReject 表示"拒绝"：进入反馈收集阶段。
	ChoiceReject = "reject"
	// ChoiceEsc 表示"回退"：回到上一阶段，而非关闭审批。
	ChoiceEsc = "esc"
)

// ApprovalDecision 是一次前进操作携带的决策输入。
//
// Choice 是用户选择（见 Choice* 常量）；Scope 是"始终允许"时打算记住的作用域，
// 供 ScopeSummary 展示，状态机本身只读取、不修改它；Feedback 是拒绝时收集、
// 要喂回模型的文本；Confirmed 是二次确认/反馈提交通用的"确认提交"信号。
type ApprovalDecision struct {
	Choice    string
	Scope     []string
	Feedback  string
	Confirmed bool
}

// AdvanceStage 根据决策把当前阶段推进到下一阶段。
//
// 返回 nextStage 与 completed：completed 为真表示整条审批已完成，调用方可以
// 结束流程；为假表示仍需继续与用户交互，此时 nextStage 是应展示的阶段。
//
// 迁移规则：
//   - ChoiceEsc 优先级最高，回退到上一阶段且永不视为完成（Esc 是"返回"不是
//     "关闭"）；根阶段 stageOnce 没有上一阶段，回退即原地停留；
//   - stageOnce：once 直接完成；always 进入 stageConfirmScope；reject 进入
//     stageFeedback；其他取值原地停留；
//   - stageConfirmScope：只有 Confirmed 为真才完成提交，否则停留等待确认；
//   - stageFeedback：只有 Confirmed 为真才完成提交（此时 Feedback 即为要喂回
//     模型的文本），否则停留继续收集；
//   - 未知阶段一律原地停留且未完成，保持失败即安全。
//
// 本函数是纯函数：不修改 decision（包括其 Scope 切片），可安全并发调用。
func AdvanceStage(stage ApprovalStage, decision ApprovalDecision) (nextStage ApprovalStage, completed bool) {
	// Esc 先于一切阶段判断：它是回退，不是完成，也不是关闭。
	if decision.Choice == ChoiceEsc {
		return previousStage(stage), false
	}

	switch stage {
	case stageOnce:
		switch decision.Choice {
		case ChoiceOnce:
			return stage, true
		case ChoiceAlways:
			return stageConfirmScope, false
		case ChoiceReject:
			return stageFeedback, false
		default:
			// 未识别的选择：停留，绝不意外完成。
			return stage, false
		}
	case stageConfirmScope, stageFeedback:
		// 二次确认与反馈收集都以 Confirmed 作为唯一的提交信号。
		if decision.Confirmed {
			return stage, true
		}
		return stage, false
	default:
		// 未知阶段：原地停留且未完成。
		return stage, false
	}
}

// previousStage 返回 Esc 回退后的上一阶段。
//
// 只有两个子阶段有"上一阶段"：它们都退回根阶段 stageOnce。根阶段本身没有
// 更前的阶段，回退即原地停留；未知阶段同样原样返回，避免跳到意外状态。
func previousStage(stage ApprovalStage) ApprovalStage {
	switch stage {
	case stageConfirmScope, stageFeedback:
		return stageOnce
	default:
		return stage
	}
}

// ScopeSummary 把要记住的作用域整理成用于"始终允许"二次确认展示的摘要。
//
// 它去掉空项与重复项，按字符串字节序稳定排序，再用顿号连接。合法 UTF-8 下
// 字节序等价于 Unicode 码点序，因此对 CJK 作用域同样稳定、确定；全程只做
// 整串比较与写入，不会在字节中间切断 UTF-8 序列。空集合返回空字符串。
//
// 本函数是纯函数：内部复制切片后再排序，不修改传入的 scope。
func ScopeSummary(scope []string) string {
	if len(scope) == 0 {
		return ""
	}

	// 先复制再去重，保证不触碰调用方持有的底层数组。
	items := make([]string, 0, len(scope))
	seen := make(map[string]struct{}, len(scope))
	for _, s := range scope {
		if s == "" {
			continue
		}
		if _, exists := seen[s]; exists {
			continue
		}
		seen[s] = struct{}{}
		items = append(items, s)
	}
	if len(items) == 0 {
		return ""
	}

	sort.Strings(items)

	var b strings.Builder
	for i, s := range items {
		if i > 0 {
			b.WriteString("、")
		}
		b.WriteString(s)
	}
	return b.String()
}
