package agent

import "sync"

// GuidanceState 记录失败指引的去重状态：它只记住最近一次注入所用的签名，
// 使同一个签名在连续失败期间不会被逐轮重复注入，避免长对话里堆积上百条雷同指引。
//
// 它是纯内存状态：不做任何 I/O、不读时钟、不构造消息；签名由调用方构造
// （如 "kind:count"）。所有方法都加锁，可安全地被并行的工具结果处理与轮次循环共享。
// 零值即可直接使用。该原语默认不接入运行循环，只有显式调用时才会生效。
type GuidanceState struct {
	mu       sync.Mutex
	lastSig  string // 最近一次注入时的签名；为空表示尚未注入过。
	injected int    // 自上次 Reset 以来 ShouldInject 返回 true 的次数。
}

// ShouldInject 判断 sig 是否需要注入。
// 当 sig 与 lastSig 相同时返回 false，且不改动注入计数；
// 否则记录该签名、把注入计数加一并返回 true，由调用方据此决定是否真的写入提示流。
//
// 零值状态下 lastSig 为空，因此任何非空 sig 的首次调用都会注入；
// 空 sig 在尚未注入过时会被视为与零值 lastSig 相同而静默，调用方应传入有意义的签名。
func (g *GuidanceState) ShouldInject(sig string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if sig == g.lastSig {
		return false
	}
	g.lastSig = sig
	g.injected++
	return true
}

// Reset 在工具成功时清空去重状态，使同一个签名能在下一段失败里重新注入。
// 注入计数一并归零，因此 Injected 反映的是当前失败区间内已注入的条数。
func (g *GuidanceState) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lastSig = ""
	g.injected = 0
}

// Injected 返回自上次 Reset（或创建）以来 ShouldInject 返回 true 的次数。
func (g *GuidanceState) Injected() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.injected
}
