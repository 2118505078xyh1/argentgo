package agent

import "strings"

// ReasoningOp 是前端必须对其展示的推理文本执行的改写操作。
// Replace 置位时，前端用 Text 替换整个显示的文本块；否则 Text 是追加到末尾的片段。
// 在一个值中同时承载两种形式，使流可以把增量片段与重新发送完整文本块的权威结束帧混合起来。
type ReasoningOp struct {
	Replace bool
	Text    string
}

// MergeReasoningDelta 把一个增量推理片段折叠进目前已累积的文本。
// 它返回新的累积文本，以及前端是否必须替换（而非追加）以避免把文本块渲染两次。
//
// 有些提供方发送的是累积快照（"让"、"让我"、"让我思考"），而非严格的增量。
// 一个已经以目前所见全部内容开头的片段就是这样的快照：较长者胜出，前端执行替换。
// 完全相同的重复是空操作。严格新增的片段会被追加。
//
// 该操作是面向字节的（strings.HasPrefix / 拼接），因此多字节 rune 永远不会被切断：
// CJK 和 emoji 文本保持完整。
func MergeReasoningDelta(current, delta string) (next string, replace bool) {
	if delta == "" {
		return current, false
	}
	if current == "" {
		return delta, false
	}
	if strings.HasPrefix(delta, current) {
		if len(delta) == len(current) {
			return current, false // 完全重复：无需处理
		}
		return delta, true // 累积快照：替换，不要重复
	}
	return current + delta, false
}

// ReasoningEndOverride 应用终结推理帧。在结束时重新发送完整文本块的提供方，
// 必须覆盖增量帧产生的任何内容：结束帧是权威的，绝不会被追加。空的结束帧不改动累积文本，
// 完全相同的结束帧是空操作，因此幂等的提供方不会触发多余的重绘。
func ReasoningEndOverride(current, final string) (next string, replace bool) {
	if final == "" {
		return current, false
	}
	if final == current {
		return current, false
	}
	return final, true
}

// ReasoningAggregator 是对两个纯合并辅助函数的有状态包装。
// 它让拥有单次请求推理流的调用方按顺序输入片段，并把每个返回的 ReasoningOp 转换为前端事件。
// 该类型是纯内存状态：无 I/O、无事件、无依赖，因此何时以及如何发出完全由调用方掌控。
// 零值即可使用。
type ReasoningAggregator struct {
	text string
}

// Add 折叠一个增量片段，并返回要发出的操作。
func (a *ReasoningAggregator) Add(delta string) ReasoningOp {
	next, replace := MergeReasoningDelta(a.text, delta)
	a.text = next
	if replace {
		return ReasoningOp{Replace: true, Text: next}
	}
	return ReasoningOp{Text: delta}
}

// Finish 应用权威的结束帧，并返回要发出的操作。
func (a *ReasoningAggregator) Finish(final string) ReasoningOp {
	next, replace := ReasoningEndOverride(a.text, final)
	a.text = next
	if replace {
		return ReasoningOp{Replace: true, Text: next}
	}
	return ReasoningOp{}
}

// Text 返回目前为止累积的全部内容（完整的显示文本块）。
func (a *ReasoningAggregator) Text() string {
	return a.text
}
