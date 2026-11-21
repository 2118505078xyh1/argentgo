package agent

import "github.com/2118505078xyh1/argentgo/internal/llm"

// 本文件提供"最终参数指纹校验"原语：在流式采集结束后，用模型最终回复中的
// 权威 ToolCalls 重算指纹，并把预跑缓存收敛到"最终参数确实匹配"的集合。
//
// 背景：流式采集到的参数字节可能≠最终调用字节（例如 responses 的 done 事件
// 会用权威结果覆盖累积值），因此以流式增量算出的指纹预跑的结果，必须以最终
// reply.ToolCalls 重算的指纹复核后才能复用。
//
// 这三个函数都是 stdlib-only、零 I/O、零调用方：它们不接入 loop、不执行工具、
// 不改动 SpecCache 结构，只提供可被接线层按需使用的纯逻辑与缓存操作。

// FinalFingerprints 从最终回复的 ToolCalls 中提取每个调用的名称与原始参数，
// 按调用顺序稳定计算并返回 SpeculativeFingerprint。
//
// 它只读取 reply，不修改 reply 及其中的任何 ToolCall；返回的切片是新建的，
// 与 reply.ToolCalls 一一对应（无工具调用时返回空切片）。
func FinalFingerprints(reply llm.Message) []string {
	fps := make([]string, 0, len(reply.ToolCalls))
	for _, call := range reply.ToolCalls {
		fps = append(fps, SpeculativeFingerprint(call.Function.Name, call.Function.Arguments))
	}
	return fps
}

// SpecCachePruneTo 把缓存收敛到 valid 列出的指纹集合：只保留这些条目，
// 其余一律从缓存中删除（作废）。valid 为 nil 或空时清空整个缓存。
//
// 它只改动 cache 本身（保留条目的结果与所属轮次不变，不改变 round），
// 不触碰 valid 或其它任何外部状态；nil 接收者为无操作。
func SpecCachePruneTo(cache *SpecCache, valid []string) {
	if cache == nil {
		return
	}
	keep := make(map[string]struct{}, len(valid))
	for _, fp := range valid {
		keep[fp] = struct{}{}
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()

	if len(keep) == 0 {
		cache.entries = nil
		return
	}
	for fp := range cache.entries {
		if _, ok := keep[fp]; !ok {
			delete(cache.entries, fp)
		}
	}
}

// CacheHitsFor 按 final 的顺序返回每个指纹在缓存中的命中结果。
//
// 命中时返回该指纹对应的预跑结果并在第二个切片相应位置置 true；未命中（或
// 条目已被整轮作废）时返回零值 SpecResult 与 false。两个返回切片与 final
// 等长且顺序一致，final 为 nil/空时返回空切片；nil 缓存视为全部未命中。
//
// 本函数只读取缓存（沿用一个 Get 语义：顺带惰性清除已作废条目），不修改
// final 或任何其它状态。
func CacheHitsFor(cache *SpecCache, final []string) ([]SpecResult, []bool) {
	results := make([]SpecResult, len(final))
	hits := make([]bool, len(final))
	for i, fp := range final {
		results[i], hits[i] = cache.Get(fp)
	}
	return results, hits
}
