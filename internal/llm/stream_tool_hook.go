package llm

import "sync"

// 本文件提供一个"流式工具增量采集器":它把 ToolDeltaHook 的回调形态
// 与 ToolDeltaAccumulator 的累积能力组合起来,供调用方在把 hook 安装到
// context 后,于流结束时一次性取出全部完整调用。
//
// 与既有接缝一致,它不接触任何协议、连接或 I/O,也没有内置调用方,
// 因此不改变任何 wire 行为,也不影响 Chatter 的众多 fake 实现。

// StreamToolCollector 把 ToolDeltaHook 与 ToolDeltaAccumulator 组合成
// 一个可直接消费的采集器。
//
// 零值即可用于 CompleteAll/Reset(内部累积器按需惰性建图);要获得可安装到
// WithToolDeltaHook 的回调,请用 NewStreamToolCollector。采集器自带的锁与
// 返回的 hook 共用,使"收片"与 CompleteAll/Reset 彼此互斥——这样取出操作
// 是原子的,不会在快照与移除之间被新分片撕裂;内部再用 ToolDeltaAccumulator
// 完成按 Index 的参数归并,沿用 MergeToolDelta 的既有语义。
type StreamToolCollector struct {
	mu  sync.Mutex
	acc ToolDeltaAccumulator
}

// NewStreamToolCollector 返回一个采集器和一个可直接传给 WithToolDeltaHook
// 的回调。回调把每片增量并入采集器内部的累积器;它与采集器共享同一把锁,
// 因此可在多个 goroutine 间安全使用。
func NewStreamToolCollector() (*StreamToolCollector, ToolDeltaHook) {
	c := &StreamToolCollector{}
	hook := func(d ToolCallDelta) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.acc.OnDelta(d)
	}
	return c, hook
}

// CompleteAll 取出并移除当前全部调用,按 Index 升序返回。
//
// 整个过程在同一把锁内完成:先借 Snapshot 拿到有序的 Index 列表,再逐个
// Complete。由于 hook 也走这把锁,"快照→取出"之间不会有新分片插入,
// 返回的就是此刻的全部完整调用,且这些槽位随后不再保留在采集器中。
// 返回值是新切片,元素为值拷贝;空采集器返回长度为 0 的切片。
func (c *StreamToolCollector) CompleteAll() []ToolCallDelta {
	c.mu.Lock()
	defer c.mu.Unlock()

	snapshot := c.acc.Snapshot()
	out := make([]ToolCallDelta, 0, len(snapshot))
	for _, call := range snapshot {
		completed, ok := c.acc.Complete(call.Index)
		if ok {
			out = append(out, completed)
		}
	}
	return out
}

// Reset 丢弃当前累积的全部调用,把采集器恢复到空状态。
//
// 与 CompleteAll 共用同一把锁,可与 hook 回调并发调用而不会撕裂;
// Reset 之后可继续通过原 hook 收片。
func (c *StreamToolCollector) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, call := range c.acc.Snapshot() {
		c.acc.Complete(call.Index)
	}
}
