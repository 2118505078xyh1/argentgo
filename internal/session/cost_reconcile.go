package session

import "example.com/argentgo/internal/calcutil"

// 本文件保留 session 包既有的成本回灌 API（类型别名 + 薄转发），
// 实际的纯算术实现已下沉到零依赖的 calcutil 包，作为单一真源。
// 这样 session 的调用方无需改动，agent 等其他包也能复用同一段算术而不依赖 session。

// CostContribution 描述一个子代理对父消息的成本贡献。
// 它是 calcutil.CostContribution 的别名，字段与底层原语完全一致。
type CostContribution = calcutil.CostContribution

// ReconcileChildCost 把若干子代理的部分成本累加到父消息成本上，并给出规范化后的明细。
// 实现转发到 calcutil.ReconcileChildCost，行为（过滤、稳定排序、确定性求和）不变。
func ReconcileChildCost(parentCost float64, children []CostContribution) (total float64, breakdown []CostContribution) {
	return calcutil.ReconcileChildCost(parentCost, children)
}
