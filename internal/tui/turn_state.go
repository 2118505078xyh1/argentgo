package tui

import (
	"errors"
	"fmt"
	"time"
)

// TurnPhase 描述一个回合在生命周期中所处的阶段。
//
// 它借鉴 cline 的 TurnState 思想：用单一权威阶段取代散落在按钮、
// 加载态里的布尔标志，使异步事件只能沿合法路径推进回合。四个阶段
// 构成一条典型链路：idle → thinking → tool-running → thinking → answering → idle。
type TurnPhase uint8

const (
	// TurnIdle 表示没有进行中的回合；它可能来自初始状态、回合结束
	// 或用户中止。
	TurnIdle TurnPhase = iota
	// TurnThinking 表示模型正在推理（含流式思考/推理内容）。
	TurnThinking
	// TurnToolRunning 表示模型已请求工具，工具正在执行。
	TurnToolRunning
	// TurnAnswering 表示模型正在输出最终答复。
	TurnAnswering
)

var turnPhaseNames = [...]string{
	"idle", "thinking", "tool-running", "answering",
}

// String 返回阶段的稳定小写名称；未知阶段返回 "unknown"。
func (p TurnPhase) String() string {
	if int(p) < len(turnPhaseNames) {
		return turnPhaseNames[p]
	}
	return "unknown"
}

// TurnState 是一个回合的权威状态。它有三个字段：
//
//   - Phase：当前阶段。
//   - AnchorTs：当前阶段的锚点时间，即最近一次成功推进的阶段事件
//     所携带的时刻。携带更早时刻的异步事件被视为上一回合的陈旧
//     事件并被拒绝，这正是 cline 用锚点淘汰过期回调的做法。
//   - Seq：单调递增的序号，用于在时钟粒度较粗、时间戳相等时区分
//     新旧事件并去重（重复或乱序事件一律拒绝）。
//
// 零值是合法的初始状态：idle、零时间锚点、序号 0。本类型只承载
// 数据，所有转换都是纯函数，不修改接收者，也不触碰任何渲染状态。
type TurnState struct {
	Phase    TurnPhase
	AnchorTs time.Time
	Seq      uint64
}

// TurnEvent 描述一次阶段推进请求。At 是触发该阶段的时刻（将成为
// 新的锚点），Seq 是调用方分配的、在整个回合生命周期内单调递增的
// 序号。
type TurnEvent struct {
	Phase TurnPhase
	At    time.Time
	Seq   uint64
}

// 转换失败时的哨兵错误，调用方可用 errors.Is 匹配。
var (
	// ErrStaleTurnSeq 表示事件序号没有严格大于当前序号（重复/乱序）。
	ErrStaleTurnSeq = errors.New("stale turn seq")
	// ErrStaleTurnAnchor 表示事件时刻早于当前锚点（上一回合的陈旧事件）。
	ErrStaleTurnAnchor = errors.New("stale turn anchor")
	// ErrIllegalTurnTransition 表示阶段组合不合法。
	ErrIllegalTurnTransition = errors.New("illegal turn transition")
)

// CanTransition 报告阶段 from 能否合法推进到 to。这里只描述由模型
// 循环驱动的自然推进；用户发起的取消/中止走 Abort，不属于此矩阵。
// 规则如下：
//
//   - idle 只能进入 thinking（开启新回合）；
//   - thinking 可进入 tool-running 或 answering（工具调用 / 直接作答）；
//   - tool-running 可进入 thinking（工具结果回灌）、answering（工具后
//     作答）或再次进入 tool-running（连续工具，刷新锚点）；
//   - answering 可进入 idle（作答完成）或再次进入 answering（流式
//     续块刷新锚点）。
//
// 其余组合非法；thinking 拒绝自转换，因为重复的 thinking 事件应作为
// 重复事件由 seq 去重，而不是一次新的阶段推进。任何未知阶段值一律
// 返回 false。
func CanTransition(from, to TurnPhase) bool {
	switch from {
	case TurnIdle:
		return to == TurnThinking
	case TurnThinking:
		return to == TurnToolRunning || to == TurnAnswering
	case TurnToolRunning:
		return to == TurnThinking || to == TurnAnswering || to == TurnToolRunning
	case TurnAnswering:
		return to == TurnIdle || to == TurnAnswering
	}
	return false
}

// fresh 检查事件是否足够"新"：序号必须严格递增，时刻不得早于当前
// 锚点。检查顺序固定为 seq → anchor，便于调用方稳定地判断拒绝原因。
func (s TurnState) fresh(e TurnEvent) error {
	if e.Seq <= s.Seq {
		return fmt.Errorf("%w: event %d, current %d", ErrStaleTurnSeq, e.Seq, s.Seq)
	}
	if e.At.Before(s.AnchorTs) {
		return fmt.Errorf("%w: event %v before anchor %v", ErrStaleTurnAnchor, e.At, s.AnchorTs)
	}
	return nil
}

// Apply 把一次阶段事件应用到当前状态，返回新的权威状态。
// 它是纯函数：不修改接收者。以下任一情况拒绝并返回包装后的哨兵错误
// （可用 errors.Is 判断），原状态原样返回：
//
//   - 序号非严格递增 → ErrStaleTurnSeq；
//   - 事件时刻早于当前锚点（陈旧事件）→ ErrStaleTurnAnchor；
//   - !CanTransition(s.Phase, e.Phase) → ErrIllegalTurnTransition。
//
// 接受时，新状态的 AnchorTs 取 e.At、Seq 取 e.Seq，从而刷新锚点：
// 之后携带更旧时刻或更旧序号的事件都会被拒绝。
func (s TurnState) Apply(e TurnEvent) (TurnState, error) {
	if err := s.fresh(e); err != nil {
		return s, err
	}
	if !CanTransition(s.Phase, e.Phase) {
		return s, fmt.Errorf("%w: %s -> %s", ErrIllegalTurnTransition, s.Phase, e.Phase)
	}
	return TurnState{Phase: e.Phase, AnchorTs: e.At, Seq: e.Seq}, nil
}

// Abort 把任意活动阶段重置为 idle（用户取消/中止）。它与 Apply 走
// 相同的陈旧性检查，但不受 CanTransition 的阶段矩阵限制——因为
// "活动阶段 → idle"本质上是一次外部重置，而非模型驱动的自然推进。
// 对 idle 调用 Abort 视为非法。
func (s TurnState) Abort(at time.Time, seq uint64) (TurnState, error) {
	if s.Phase == TurnIdle {
		return s, fmt.Errorf("%w: cannot abort from %s", ErrIllegalTurnTransition, s.Phase)
	}
	if err := s.fresh(TurnEvent{At: at, Seq: seq}); err != nil {
		return s, err
	}
	return TurnState{Phase: TurnIdle, AnchorTs: at, Seq: seq}, nil
}
