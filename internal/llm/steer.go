package llm

import (
	"os"
	"strings"
)

// 本文件实现 opt-in 的供应商原生 live steering（流中重定向，TODO #13）。
//
// 协议取舍：Responses API 的 input 数组只在请求开头被接受，一条已经发出的 SSE
// 流无法合法地追加 input_text / input_image 消息。因此这里不改写任何线协议字节，
// 而是在 SSE 事件边界检查调用方排队的纠正文本，并按当前位置上报决策：
//
//   - accepted：捕获点位于两个完整事件之间，且没有未完成的 function_call；
//     上层可据此在下一个合法边界（下一轮请求）把文本作为用户消息注入。
//   - deferred：捕获点落在工具调用的原子段中间，此时注入会破坏参数/结果配对；
//     同样上报但标记延后，待工具段结束后由下一轮处理。
//
// 关闭时（默认）readStream 既不读取 Steer，也不分配任何状态，线协议字节与内存
// 行为与接入前逐字节一致。本文件不启动 goroutine，因此不存在泄漏路径。

const (
	steerAccepted = "accepted"
	steerDeferred = "deferred"
)

// SteerDecision 记录一次流中 steering 的捕获决策。
// 它由 readStream 经 Options.SteerHook 上报，供上层落 accepted/deferred 事件。
type SteerDecision struct {
	// Text 是调用方通过 Options.Steer 排队的纠正原文（已过滤纯空白项）。
	Text string
	// Status 为 "accepted" 或 "deferred"。
	Status string
	// Reason 说明决策依据（"event-boundary" 或 "tool-call-in-progress"）。
	Reason string
}

// liveSteerOn 报告是否开启 live steering。
// 原值首尾空白修剪后等于 "1" 才开启；默认关闭，且原值不变时结果恒定。
func liveSteerOn() bool {
	return strings.TrimSpace(os.Getenv("ARGENT_LIVE_STEER")) == "1"
}

// steerRecorder 在一次 readStream 调用内上报 steering 决策。
// 它只持有一个可选的 hook，无内部缓冲、无 goroutine。
type steerRecorder struct {
	hook func(SteerDecision)
}

// report 上报一条决策；hook 为 nil 时为空操作。
func (r *steerRecorder) report(text, status, reason string) {
	if r != nil && r.hook != nil {
		r.hook(SteerDecision{Text: text, Status: status, Reason: reason})
	}
}

// drainSteerTexts 过滤掉纯空白的纠正文本，语义对齐 agent 层 steering.drain：
// 空白项不值得作为用户指令注入，但调用方已把它们从队列中消费掉。
func drainSteerTexts(texts []string) []string {
	var out []string
	for _, text := range texts {
		if strings.TrimSpace(text) != "" {
			out = append(out, text)
		}
	}
	return out
}
