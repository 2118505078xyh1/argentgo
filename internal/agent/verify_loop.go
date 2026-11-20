package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// verifyReflectionHint 刻意与 security.chain.go 的 finalitySentence 分开：
// 验证失败是一次有界、可恢复的提示，而非绝不能重试的刻意策略阻断。
//
// 前缀/后缀被拆开，使 HasDiagnosticContent 在判断 FAIL 是否携带可操作文本时，
// 能识别并忽略这个提示。组合后的值与原提示逐字节一致。
const (
	verifyReflectionHintPrefix = "Reflections left: "
	verifyReflectionHintSuffix = ". Do not re-run the same check unchanged."
	verifyReflectionHint       = verifyReflectionHintPrefix + "%d" + verifyReflectionHintSuffix
)

// VerifyLoopState 是每次运行用于编辑后验证反思的预算。零值即可使用：
// Max <= 0 表示 DefaultMaxVerifyReflections。
type VerifyLoopState struct {
	Reflections int
	Max         int
	Root        string // 工作区根目录；在反馈中启用 █ 源码锚定
	// LastOutcome 记录最近一次的验证结论（"PASS"、"FAIL" 或 "SKIP"）。
	// 它仅是附加/观察性的：没有任何循环决定读取它，因此设置它不会改变事件或控制消息。
	// omitempty 标签使从未运行过检查的状态编码保持不变。
	LastOutcome string `json:"last_outcome,omitempty"`
}

func (s VerifyLoopState) maxReflections() int {
	if s.Max > 0 {
		return s.Max
	}
	return DefaultMaxVerifyReflections
}

// CanReflect 报告是否还能注入一条 FAIL 反馈轮次。
func (s VerifyLoopState) CanReflect() bool {
	return s.Reflections < s.maxReflections()
}

// Record 消耗一次反思，报告是否有可用的反思。达到上限时它是空操作，
// 因此盲目调用的调用方也不会超出预算。
func (s *VerifyLoopState) Record() bool {
	if !s.CanReflect() {
		return false
	}
	s.Reflections++
	return true
}

// remaining 是在当前正在报告的这一次之后，仍可消耗的反思次数。它永不为负。
func (s VerifyLoopState) remaining() int {
	left := s.maxReflections() - s.Reflections
	if left < 0 {
		return 0
	}
	return left
}

// BuildVerifyFeedback 把一次 FAIL 渲染为下一条用户轮次。它复用现有的有界、脱敏的
// VerificationFeedback，并追加一条简短的、验证专用的防重试语句。
// 输出部分已经限制在 defaultVerifyMaxBytes 以内，因此整条消息保持有界。
func BuildVerifyFeedback(label, output string, state VerifyLoopState) string {
	feedback := VerificationFeedbackAt(label, false, output, defaultVerifyMaxBytes, state.Root)
	return feedback + "\n" + fmt.Sprintf(verifyReflectionHint, state.remaining())
}

// IsMutatingTool 报告某个工具名是否属于默认的变更型工具集合。
// 它对应 runState.mutating 的默认分支；使用自定义 Mutating/MutatingTools 选项的运行
// 应改用那个感知 Options 的方法。
func IsMutatingTool(name string) bool {
	for _, candidate := range defaultMutatingTools {
		if candidate == name {
			return true
		}
	}
	return false
}

// batchHasMutation 报告在本运行的策略（它可能覆盖默认的变更型工具集合）下，
// 批次中是否有任何调用会做修改。
func (s *runState) batchHasMutation(calls []llm.ToolCall) bool {
	for _, call := range calls {
		if s.mutating(call.Function.Name) {
			return true
		}
	}
	return false
}

// verifyChangedPaths 从批次已经发出的检查点中读取工作区路径。复用该事件可使 MutationPaths
// 钩子只调用一次，并使验证看到的正是会话所快照的路径。
func (s *runState) verifyChangedPaths(turn int) []string {
	events := s.eventsSnapshot()
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Kind == EventCheckpointReady && e.Turn == turn && e.Checkpoint != nil {
			return append([]string(nil), e.Checkpoint.Paths...)
		}
	}
	return nil
}

// verifyAfterBatch 在变更型批次之后运行注入的编辑后检查。
//
// 当还有反思额度时，FAIL 会通过 injectSteering 变成下一条用户轮次；额度用尽后，
// 再一次 FAIL 会以 StopVerifyReflections 结束运行。PASS 或无法运行的检查（ok == false）
// 不改动计数。PASS 刻意不重置任何东西：反思预算是按运行计算的。
//
// 只有在运行必须结束时才返回 (stopReason, true)。
func (s *runState) verifyAfterBatch(ctx context.Context, turn int, calls []llm.ToolCall) (string, bool) {
	if s.opts.Verify == nil || !s.batchHasMutation(calls) || !s.batchVerificationEligible {
		// 被整体拒绝的批次（参数被截断、BeforeTool 钩子失败或调用 id 无效）由 fillUnrun 回应，
		// 不运行任何工具。对它做验证会因为 FAIL 消耗一次反思，
		// 甚至可能在根本没有发生编辑的情况下以 StopVerifyReflections 结束运行。
		return "", false
	}
	label, passed, output, ok := s.opts.Verify(ctx, s.verifyChangedPaths(turn))
	if !ok {
		s.verifyState.LastOutcome = "SKIP"
		s.emit(Event{Kind: EventNotice, Turn: turn, Text: "verify: check could not run; skipping reflection"})
		return "", false
	}
	name := strings.ToUpper(strings.TrimSpace(label))
	if name == "" {
		name = "VERIFY"
	}
	verdict := "PASS"
	if !passed {
		verdict = "FAIL"
	}
	s.verifyState.LastOutcome = verdict
	s.emit(Event{Kind: EventNotice, Turn: turn, Text: "[verify: " + name + "] " + verdict})
	if passed {
		return "", false
	}
	if !s.verifyState.CanReflect() {
		s.emit(Event{Kind: EventNotice, Turn: turn, Text: fmt.Sprintf(
			"verification failed after %d reflection(s)", s.verifyState.Reflections,
		)})
		return StopVerifyReflections, true
	}
	s.verifyState.Record()
	s.injectSteering(BuildVerifyFeedback(label, output, s.verifyState))
	return "", false
}
