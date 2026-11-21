package server

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// Phase 是后端权威的"当前一轮正在做什么"。它只描述状态本身，不含任何
// UI 文案；前端据此决定绘制流式文本、工具指示器还是审批提示。
//
// 零值 PhaseIdle 表示尚未开始一轮，因此 JSON omitempty 会把它省略。
type Phase uint8

const (
	PhaseIdle             Phase = iota // 空闲：尚未开始一轮，或刚越过轮次边界
	PhaseStreaming                     // 正在流式输出模型增量
	PhaseAwaitingTool                  // 正在执行工具调用
	PhaseAwaitingApproval              // 正在等待用户审批
	PhaseFinished                      // 已终结：收到唯一的终止事件
)

// String 返回稳定的英文标识，用于日志与调试；未知值统一报 "unknown"。
func (p Phase) String() string {
	switch p {
	case PhaseIdle:
		return "idle"
	case PhaseStreaming:
		return "streaming"
	case PhaseAwaitingTool:
		return "awaiting_tool"
	case PhaseAwaitingApproval:
		return "awaiting_approval"
	case PhaseFinished:
		return "finished"
	default:
		return "unknown"
	}
}

// State 是 TurnState 的只读投影：
//   - Phase 驱动 UI 绘制；
//   - AnchorMS 标记当前轮的起点（Unix 毫秒），用于淘汰上一轮的迟到事件；
//   - Seq 与 EventHub 的单调序号对齐，用于淘汰乱序或重复事件。
//
// 所有字段都带 omitempty，因此零值编码为 `{}`，新增字段不会改变旧
// 客户端看到的字节序列。State 是可比较的，适合放进 atomic.Value 做 CAS。
type State struct {
	Phase    Phase  `json:"phase,omitempty"`
	AnchorMS int64  `json:"anchor_ms,omitempty"`
	Seq      uint64 `json:"seq,omitempty"`
}

// 状态投影识别的事件 kind。前半段与 agent.EventKind 的字符串一致，
// 故意用字面量而非 import agent，使本文件只依赖标准库。
// 审批两个是调用方从 notice 文本规范化后的附加 kind：agent 用
// "approval:request/resolved " 前缀的 notice 表达审批，Reduce 只看 kind，
// 因此调用方必须先识别前缀再映射到这里。
const (
	EvTurnStart       = "turn_start"
	EvTurnEnd         = "turn_end"
	EvToolStart       = "tool_start"
	EvToolEnd         = "tool_end"
	EvTextDelta       = "text_delta"
	EvReasoningDelta  = "reasoning_delta"
	EvUsage           = "usage"
	EvError           = "error"
	EvNotice          = "notice"
	EvCheckpointReady = "checkpoint_ready"
	EvPartial         = "partial"
	EvSubagentStart   = "subagent_start"
	EvSubagentEnd     = "subagent_end"
	EvRetry           = "retry"
	EvBudgetReminder  = "budget_reminder"
	EvStop            = "stop"

	EvApprovalRequest  = "approval_request"
	EvApprovalResolved = "approval_resolved"
)

// ErrIllegalTransition 表示事件在 seq/anchor 都合法的前提下，却与当前
// phase 矛盾（例如已终结后又收到事件，或无法识别的 kind）。
// 陈旧事件不算非法：它们被静默淘汰，不返回错误。
var ErrIllegalTransition = errors.New("turn state: illegal phase transition")

// isTerminal 报告 kind 是否为唯一的运行终止事件。
// 与 agent.Event.IsTerminal 对齐：只有 stop 终结整次运行，
// turn_end 只是多轮之间的边界。
func isTerminalKind(kind string) bool {
	return kind == EvStop
}

// Reduce 把单个事件折叠进 prev，返回投影后的新状态。
//
// 淘汰规则（都不视作错误，直接原样返回 prev）：
//  1. seq 单调：seq <= prev.Seq 表示重复或乱序事件，丢弃；
//  2. anchor：带时间戳的非终止事件若其 startedMS 早于 prev.AnchorMS，
//     说明它属于上一轮，丢弃。startedMS 为 0 表示事件没有起点时间戳，
//     不参与锚点比较。终止事件享有必达语义，永不被锚点淘汰，
//     保证慢订阅者即使错过进度事件也一定能把状态推进到 finished。
//
// 转换规则：只有 stop 进入 PhaseFinished；turn_end 只回到空闲边界，
// 等待下一次 turn_start；工具与审批事件在 streaming/awaiting 之间切换；
// 其余进度事件保持或进入 streaming。已终结的状态拒绝一切后续事件。
// turn 仅用于错误详情，不进入投影（State 有意不保存轮号）。
//
// startedMS 用 uint64 传入并与 int64 的 AnchorMS 互转：Unix 毫秒恒为正，
// 该转换在实际取值范围内无损。
func Reduce(prev State, kind string, turn int, startedMS, seq uint64) (State, error) {
	terminal := isTerminalKind(kind)

	// 规则 1：seq 必须严格递增。
	if seq <= prev.Seq {
		return prev, nil
	}

	// 规则 2：锚点淘汰。终止事件享有必达语义，跳过该检查。
	anchor := int64(startedMS)
	if !terminal && startedMS != 0 && anchor < prev.AnchorMS {
		return prev, nil
	}

	// 已终结的状态拒绝一切后续事件，包括重复的终止事件。
	if prev.Phase == PhaseFinished {
		return prev, fmt.Errorf("%w: phase finished, turn %d, kind %q", ErrIllegalTransition, turn, kind)
	}

	next := prev
	next.Seq = seq

	switch kind {
	case EvStop:
		// 唯一的终止事件：必达且终结整次运行。
		next.Phase = PhaseFinished
	case EvTurnStart:
		// 新一轮开始：进入流式并记录该轮锚点。
		next.Phase = PhaseStreaming
		if startedMS != 0 {
			next.AnchorMS = anchor
		}
	case EvTurnEnd:
		// 多轮边界，不是终结；回到空闲等待下一次 turn_start。
		next.Phase = PhaseIdle
	case EvToolStart:
		next.Phase = PhaseAwaitingTool
	case EvToolEnd:
		next.Phase = PhaseStreaming
	case EvApprovalRequest:
		next.Phase = PhaseAwaitingApproval
	case EvApprovalResolved:
		// 审批结束，恢复流式（工具随后会发 tool_start）。
		next.Phase = PhaseStreaming
	case EvTextDelta, EvReasoningDelta, EvUsage, EvError, EvNotice,
		EvCheckpointReady, EvPartial, EvSubagentStart, EvSubagentEnd,
		EvRetry, EvBudgetReminder:
		// 进度事件：在空闲时进入流式，否则保持当前 phase
		// （例如等待工具/审批时不被 usage 事件打断）。
		if next.Phase == PhaseIdle {
			next.Phase = PhaseStreaming
		}
	default:
		return prev, fmt.Errorf("%w: unknown kind %q at turn %d", ErrIllegalTransition, kind, turn)
	}

	return next, nil
}

// Store 以 atomic.Value 保存单个 State，为并发的 Apply 与 Load 提供
// 无锁读、CAS 写的线程安全投影。零值即可使用：首次 Load 会惰性写入
// 零 State，避免 CompareAndSwap 对 nil 接口失败。
type Store struct {
	once    sync.Once    // 惰性写入零 State，使零值 Store 可用
	current atomic.Value // 始终保存 State 值类型，不存指针
}

// init 保证 atomic.Value 在首次读写前已保存一个 State；
// 之后所有写入都保持同一具体类型，符合 atomic.Value 的约束。
func (s *Store) init() {
	s.once.Do(func() { s.current.Store(State{}) })
}

// Load 返回当前状态快照。它可与 Apply 并发调用。
func (s *Store) Load() State {
	s.init()
	return s.current.Load().(State)
}

// Apply 在最新状态上做 Reduce，并用 CAS 原子替换；发生竞争时重试，
// 保证并发调用不会丢失更新。陈旧事件（Reduce 原样返回）不会写入。
// 非法转换返回 ErrIllegalTransition，且状态保持不变。
func (s *Store) Apply(kind string, turn int, startedMS, seq uint64) (State, error) {
	for {
		prev := s.Load()
		next, err := Reduce(prev, kind, turn, startedMS, seq)
		if err != nil {
			return prev, err
		}
		if next == prev { // 陈旧事件：无需写入
			return prev, nil
		}
		if s.current.CompareAndSwap(prev, next) {
			return next, nil
		}
	}
}
