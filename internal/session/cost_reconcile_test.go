package session

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/calcutil"
)

// TestReconcileChildCostForwardsToCalcutil 是转发层的精简 smoke 测试。
// session.ReconcileChildCost 是 calcutil.ReconcileChildCost 的别名转发；
// 后者的完整行为（过滤/稳定排序/确定性求和/CJK）由 calcutil/cost_test.go
// 的 8 个表驱动测试覆盖，这里只确认转发没有偏离，避免逐字复制那 8 个测试。
func TestReconcileChildCostForwardsToCalcutil(t *testing.T) {
	children := []CostContribution{
		{ChildID: "b", CostUSD: 1.25, Tokens: 200},
		{ChildID: "a", CostUSD: 0.5, Tokens: 100},
	}
	gotTotal, gotBreakdown := ReconcileChildCost(2.0, children)
	wantTotal, wantBreakdown := calcutil.ReconcileChildCost(2.0, children)
	if gotTotal != wantTotal || len(gotBreakdown) != len(wantBreakdown) {
		t.Fatalf("forward mismatch: total %v/%v, breakdown %+v/%+v",
			gotTotal, wantTotal, gotBreakdown, wantBreakdown)
	}
	for i := range gotBreakdown {
		if gotBreakdown[i] != wantBreakdown[i] {
			t.Fatalf("breakdown[%d] mismatch: %+v != %+v", i, gotBreakdown[i], wantBreakdown[i])
		}
	}
}
