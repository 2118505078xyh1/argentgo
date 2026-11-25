package agent

import (
	"fmt"
	"hash/fnv"
	"strconv"

	"example.com/argentgo/internal/llm"
)

// 无进展检测是对基于事件的 StuckDetector 和 dispatch 重复防护的纯逻辑、无状态补充。
// 它只回答一个狭窄的问题：最近的工具调用是否一直是同一个调用返回同一个结果，
// 也就是说模型是否在原地打转、没有学到任何新东西？
//
// 这里的判断独立于定时器、I/O 和循环，因此可以单独单元测试，
// 并可以通过一个选择性启用的开关接入，而不改变未配置时的运行行为。

// DefaultNoProgressThreshold 是 DetectNoProgress 检查的尾部长度。
// 三次匹配与事件检测器的重复阈值保持一致，并保持保守：两次相同调用是正常的读取-验证模式。
const DefaultNoProgressThreshold = 3

// ToolCallSignature 是一次工具调用的稳定身份，加上对其产生结果的紧凑摘要。
// 易变字段（调用 id、轮次、耗时、用量）刻意不包含在内：仅在这些字段上不同的两次调用，
// 对循环检测而言就是同一个调用。
//
// Args 必须是规范化后的参数摘要；调用方可以使用 NormalizeArgs，
// 或使用会自动应用它的 SignatureFromToolCall / SignaturesFromEvents。Result 是输出的哈希；
// 空的 Result 表示"未记录结果"，当窗口中每个调用都共享它时，仍算作稳定（不变化）的值。
// 两次结果不同的调用属于进展，绝不是循环。
type ToolCallSignature struct {
	Name   string
	Args   string
	Result string
}

// NoProgressConfig 启用无进展检测的选择性提前停止形式。
// 当 Options.NoProgress 为 nil 时，循环从不调用检测器，因此现有运行行为不变。
type NoProgressConfig struct {
	// Threshold 是触发停止所需的相同尾部调用数量。零表示使用 DefaultNoProgressThreshold。
	Threshold int
}

// DetectNoProgress 报告末尾 DefaultNoProgressThreshold 个调用是否都是同一调用且结果相同。
// 这是无需配置的纯形式；需要不同窗口的调用方使用 DetectNoProgressWithThreshold。
func DetectNoProgress(calls []ToolCallSignature) (stuck bool, reason string) {
	return DetectNoProgressWithThreshold(calls, DefaultNoProgressThreshold)
}

// DetectNoProgressWithThreshold 是可配置阈值形式。threshold 是必须全部匹配的尾部调用数量；
// 小于 1 的值回退到 DefaultNoProgressThreshold，
// 这样配置错误的调用方也不会因空历史或单次调用历史而误判运行。
func DetectNoProgressWithThreshold(calls []ToolCallSignature, threshold int) (stuck bool, reason string) {
	if threshold < 1 {
		threshold = DefaultNoProgressThreshold
	}
	if len(calls) < threshold {
		return false, ""
	}
	window := calls[len(calls)-threshold:]
	first := window[0]
	// 空的工具名是畸形输入，不能作为循环的证据。
	if first.Name == "" {
		return false, ""
	}
	for _, c := range window[1:] {
		if c.Name != first.Name || c.Args != first.Args || c.Result != first.Result {
			return false, ""
		}
	}
	return true, fmt.Sprintf(
		"no progress: %q was called %d times in a row with identical arguments and no new result",
		first.Name, threshold)
}

// SignatureFromToolCall 在结果产生之前投影一次已派发的调用。
// 参数会被规范化，因此 JSON 键顺序和空白无法掩盖重复。
func SignatureFromToolCall(call llm.ToolCall) ToolCallSignature {
	return ToolCallSignature{Name: call.Function.Name, Args: NormalizeArgs(call.Function.Arguments)}
}

// ToolCallSignatures 按索引把已派发的调用与其结果配对。较短的 results 切片会使尾部的调用
// 没有结果摘要；nil 的 results 切片会产生无结果的签名，适用于执行前检查。
func ToolCallSignatures(calls []llm.ToolCall, results []callResult) []ToolCallSignature {
	out := make([]ToolCallSignature, 0, len(calls))
	for i, call := range calls {
		sig := SignatureFromToolCall(call)
		if i < len(results) {
			sig.Result = summarizeResult(results[i].output)
		}
		out = append(out, sig)
	}
	return out
}

// SignaturesFromEvents 从一次运行记录的 tool_end 事件构建签名。
// 每个 tool_end 都带有 Args 和 Output，因此无需与 tool_start 配对，未匹配的开始事件会被自然忽略。
func SignaturesFromEvents(events []Event) []ToolCallSignature {
	out := make([]ToolCallSignature, 0, len(events))
	for _, e := range events {
		if e.Kind != EventToolEnd {
			continue
		}
		out = append(out, ToolCallSignature{
			Name:   e.ToolName,
			Args:   NormalizeArgs(e.Args),
			Result: summarizeResult(e.Output),
		})
	}
	return out
}

// summarizeResult 把输出哈希为紧凑、稳定的标记。哈希使签名即使面对庞大的工具结果
// 也能保持很小，这与重复防护器的输出窗口类似。
func summarizeResult(output string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(output))
	return strconv.FormatUint(h.Sum64(), 16)
}
