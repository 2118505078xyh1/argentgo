package llm

// CacheBoundary 描述一组提示块以及它们身上的缓存断点位置。
//
// Blocks 是入参 blocks 的副本，块顺序与内容都原样保留。
// Breakpoints 中的每个元素都是一个块索引 i，语义为“边界位于第 i 个块之前”：
// 因此断点 i 把索引 0..i-1 的块划为稳定的、可 byte-identical 复用提示缓存的
// 前缀，而索引 i 及其后的块（易变块）不进入该缓存前缀。
type CacheBoundary struct {
	Blocks      []string
	Breakpoints []int
}

// PlanCacheBoundaries 规划提示缓存断点，供 provider 在提示块序列上放置显式
// cache_control 断点。仅当 provider 支持显式断点（supported 为真）且首个易变块
// 的索引 volatileFrom 落在有效区间 [0, len(blocks)) 时，才在首个易变块之前
// 放一个断点；其余情况（provider 不支持、volatileFrom 为负或越界、没有块）一律
// 不设断点，稳定块本身始终原样保留。
//
// volatileFrom 表示首个易变块的索引：断点索引取该值，代表边界正好落在该块之前；
// 取 0 表示首块即易变，边界位于所有块之前。
//
// 本函数是纯函数：不修改入参（返回的 Blocks 是独立副本，Breakpoints 是新切片），
// 相同入参始终得到相同结果，且不依赖任何区域设置或外部状态。
func PlanCacheBoundaries(blocks []string, volatileFrom int, supported bool) CacheBoundary {
	out := CacheBoundary{
		// 复制而非直接引用，保证返回值与入参不共享底层数组。
		Blocks: append([]string(nil), blocks...),
	}
	if supported && volatileFrom >= 0 && volatileFrom < len(blocks) {
		out.Breakpoints = append(out.Breakpoints, volatileFrom)
	}
	return out
}
