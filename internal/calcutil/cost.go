// Package calcutil 提供与会话状态、模型协议无关的纯计算原语。
// 它位于依赖图的最底层，只依赖标准库，供 session、agent 等上层包复用，
// 以避免同一段算术逻辑在多处复刻而产生语义漂移。
package calcutil

import (
	"math"
	"sort"
)

// 本文件提供一个纯粹的"子代理成本回灌"原语：子代理被取消时，它已经产生的那部分成本
// 需要回灌到父 assistant message，避免漏账。ReconcileChildCost 只做算术与规范化，
// 不触碰会话文件、不读写任何状态，也不接线到实际的 subagent 流程。

// CostContribution 描述一个子代理对父消息的成本贡献。
// ChildID 标识子代理（用于稳定排序与审计），CostUSD 是已产生的美元成本，
// Tokens 是配套的 token 计数：仅随 breakdown 透传，不参与求和与有效性判定。
type CostContribution struct {
	ChildID string
	CostUSD float64
	Tokens  int
}

// ReconcileChildCost 把若干子代理的部分成本累加到父消息成本上，并给出规范化后的明细。
//
// 行为：
//   - total = 规范化的 parentCost + 所有有效贡献的 CostUSD 之和；
//   - 无效贡献（CostUSD 为负、NaN 或 ±Inf）被直接丢弃，既不进 total 也不进 breakdown；
//   - parentCost 自身若非法（负、NaN、±Inf）按 0 处理，保证 total 始终有限且非负；
//   - breakdown 按 ChildID 升序稳定排序：ChildID 相同的贡献保持其在输入中的相对顺序；
//   - 求和按排序后的 breakdown 顺序进行，因此给定输入总是得到逐位相同的结果（确定性）；
//   - 入参绝不被修改：过滤与排序都发生在新建的切片上。
func ReconcileChildCost(parentCost float64, children []CostContribution) (total float64, breakdown []CostContribution) {
	if !validCostUSD(parentCost) {
		parentCost = 0
	}

	breakdown = make([]CostContribution, 0, len(children))
	for _, child := range children {
		if !validCostUSD(child.CostUSD) {
			continue
		}
		breakdown = append(breakdown, child)
	}

	// 稳定排序：同名 ChildID 的贡献保留输入次序，避免重复子代理的明细顺序抖动。
	sort.SliceStable(breakdown, func(i, j int) bool {
		return breakdown[i].ChildID < breakdown[j].ChildID
	})

	// 按排序后的顺序求和，使同一输入得到逐位一致的结果。
	total = parentCost
	for _, child := range breakdown {
		total += child.CostUSD
	}
	return total, breakdown
}

// validCostUSD 判定一个成本值是否可用于记账：必须是有限的非负数。
// 负数没有记账意义，NaN/±Inf 会污染累加结果，故一并拒绝。
func validCostUSD(v float64) bool {
	return v >= 0 && !math.IsNaN(v) && !math.IsInf(v, 0)
}
