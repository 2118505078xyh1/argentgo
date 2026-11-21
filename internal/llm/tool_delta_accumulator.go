package llm

import (
	"sort"
	"sync"
)

// 本文件为 ToolDeltaHook 的消费者提供一个并发安全的增量累积器。
// 它复用 tool_delta.go 中的纯函数 MergeToolDelta 完成参数拼接，
// 自身不接触任何协议、连接或 I/O，也没有内置调用方，因此既不改变
// 任何 wire 行为，也不影响 Chatter 的众多 fake 实现。

// ToolDeltaAccumulator 按 Index 汇总来自 hook 的工具调用增量。
//
// 零值即可直接使用：内部 map 在首次 OnDelta 时惰性创建。所有导出方法
// 都由同一把互斥锁保护，可在多个 goroutine 间安全共享；读方法 Snapshot
// 与写方法 OnDelta/Complete 互斥，但多个 Snapshot 之间会串行（刻意求简，
// 累积器不是热路径，且调用方原本也没有读密集场景）。
type ToolDeltaAccumulator struct {
	mu    sync.Mutex
	calls map[int]ToolCallDelta
}

// OnDelta 把一片增量并入该 Index 对应的调用。
//
// 首次见到某个 Index 时，先用该 Index 作为种子构造空累积值，再交给
// MergeToolDelta：这样即使首片的 Index 非 0，合并后保留的 acc.Index
// 仍是正确的槽位，而参数、ID、Name 沿用 MergeToolDelta 的既有规则
// （参数顺序拼接、ID/Name 取首个非空值）。
func (a *ToolDeltaAccumulator) OnDelta(d ToolCallDelta) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.calls == nil {
		a.calls = make(map[int]ToolCallDelta)
	}
	acc, ok := a.calls[d.Index]
	if !ok {
		acc = ToolCallDelta{Index: d.Index}
	}
	a.calls[d.Index] = MergeToolDelta(acc, d)
}

// Snapshot 返回当前所有调用的副本，按 Index 升序排列。
//
// 返回值是新分配的切片，元素为值拷贝；调用方任意修改它都不会影响
// 累积器的内部状态。空累积器返回长度为 0 的切片。
func (a *ToolDeltaAccumulator) Snapshot() []ToolCallDelta {
	a.mu.Lock()
	defer a.mu.Unlock()

	out := make([]ToolCallDelta, 0, len(a.calls))
	for _, call := range a.calls {
		out = append(out, call)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Index < out[j].Index
	})
	return out
}

// Complete 取出并移除指定 Index 的完整调用。
//
// 找到时返回累积值与 true，之后该槽位不再出现在 Snapshot 中；
// 未找到时返回零值与 false，且不改变内部状态。
func (a *ToolDeltaAccumulator) Complete(index int) (ToolCallDelta, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	call, ok := a.calls[index]
	if !ok {
		return ToolCallDelta{}, false
	}
	delete(a.calls, index)
	return call, true
}
