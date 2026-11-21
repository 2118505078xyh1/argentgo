package session

import (
	"math"
	"reflect"
	"testing"
)

// costClose 用相对/绝对混合容差比较浮点成本，避免二进制浮点表示误差造成假失败。
func costClose(got, want float64) bool {
	if math.IsNaN(got) || math.IsInf(got, 0) {
		return false
	}
	diff := math.Abs(got - want)
	return diff <= 1e-9*math.Max(1, math.Max(math.Abs(got), math.Abs(want)))
}

func TestReconcileChildCostAccumulatesMultipleChildren(t *testing.T) {
	children := []CostContribution{
		{ChildID: "a", CostUSD: 0.5, Tokens: 100},
		{ChildID: "b", CostUSD: 1.25, Tokens: 200},
		{ChildID: "c", CostUSD: 0.125, Tokens: 50},
	}
	total, breakdown := ReconcileChildCost(2.0, children)

	if !costClose(total, 3.875) {
		t.Fatalf("total = %v, want 3.875 (parent 2 + 0.5 + 1.25 + 0.125)", total)
	}
	want := []CostContribution{
		{ChildID: "a", CostUSD: 0.5, Tokens: 100},
		{ChildID: "b", CostUSD: 1.25, Tokens: 200},
		{ChildID: "c", CostUSD: 0.125, Tokens: 50},
	}
	if !reflect.DeepEqual(breakdown, want) {
		t.Fatalf("breakdown mismatch:\n got %+v\nwant %+v", breakdown, want)
	}
}

func TestReconcileChildCostStableSortByChildID(t *testing.T) {
	// 输入刻意乱序，且 "b" 出现两次以检验稳定排序的同键相对次序。
	children := []CostContribution{
		{ChildID: "b", CostUSD: 0.2, Tokens: 2},
		{ChildID: "a", CostUSD: 0.1, Tokens: 1},
		{ChildID: "b", CostUSD: 0.3, Tokens: 3},
		{ChildID: "c", CostUSD: 0.4, Tokens: 4},
	}
	_, breakdown := ReconcileChildCost(0, children)

	want := []CostContribution{
		{ChildID: "a", CostUSD: 0.1, Tokens: 1},
		{ChildID: "b", CostUSD: 0.2, Tokens: 2}, // 输入中先出现的 b 仍在前
		{ChildID: "b", CostUSD: 0.3, Tokens: 3},
		{ChildID: "c", CostUSD: 0.4, Tokens: 4},
	}
	if !reflect.DeepEqual(breakdown, want) {
		t.Fatalf("stable sort mismatch:\n got %+v\nwant %+v", breakdown, want)
	}
}

func TestReconcileChildCostFiltersInvalidContributions(t *testing.T) {
	children := []CostContribution{
		{ChildID: "neg", CostUSD: -1.5, Tokens: 999},
		{ChildID: "nan", CostUSD: math.NaN(), Tokens: 999},
		{ChildID: "posinf", CostUSD: math.Inf(1), Tokens: 999},
		{ChildID: "neginf", CostUSD: math.Inf(-1), Tokens: 999},
		{ChildID: "zero", CostUSD: 0, Tokens: 7}, // 0 合法，必须保留
		{ChildID: "ok", CostUSD: 0.75, Tokens: 8},
	}
	total, breakdown := ReconcileChildCost(1, children)

	if !costClose(total, 1.75) {
		t.Fatalf("total = %v, want 1.75 (invalid contributions must be dropped)", total)
	}
	want := []CostContribution{
		{ChildID: "ok", CostUSD: 0.75, Tokens: 8},
		{ChildID: "zero", CostUSD: 0, Tokens: 7},
	}
	if !reflect.DeepEqual(breakdown, want) {
		t.Fatalf("breakdown mismatch:\n got %+v\nwant %+v", breakdown, want)
	}
}

func TestReconcileChildCostSanitizesInvalidParentCost(t *testing.T) {
	cases := []struct {
		name   string
		parent float64
	}{
		{"negative", -3},
		{"nan", math.NaN()},
		{"posinf", math.Inf(1)},
		{"neginf", math.Inf(-1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			total, breakdown := ReconcileChildCost(tc.parent, []CostContribution{
				{ChildID: "c", CostUSD: 0.25, Tokens: 1},
			})
			if !costClose(total, 0.25) {
				t.Fatalf("total = %v, want 0.25 (invalid parent treated as 0)", total)
			}
			if len(breakdown) != 1 || breakdown[0].ChildID != "c" {
				t.Fatalf("breakdown = %+v, want single 'c' contribution", breakdown)
			}
		})
	}
}

func TestReconcileChildCostEmptyChildren(t *testing.T) {
	// nil 与空切片都只返回父成本与空明细。
	for _, children := range [][]CostContribution{nil, {}} {
		total, breakdown := ReconcileChildCost(4.2, children)
		if !costClose(total, 4.2) {
			t.Fatalf("total = %v, want 4.2", total)
		}
		if len(breakdown) != 0 {
			t.Fatalf("breakdown = %+v, want empty", breakdown)
		}
	}

	// 全为无效贡献时同样得到空明细。
	_, breakdown := ReconcileChildCost(1, []CostContribution{{ChildID: "bad", CostUSD: math.NaN()}})
	if len(breakdown) != 0 {
		t.Fatalf("breakdown = %+v, want empty", breakdown)
	}
}

func TestReconcileChildCostDoesNotMutateInput(t *testing.T) {
	children := []CostContribution{
		{ChildID: "z", CostUSD: 0.3, Tokens: 3},
		{ChildID: "a", CostUSD: 0.1, Tokens: 1},
		{ChildID: "m", CostUSD: -1, Tokens: 99}, // 会被过滤
	}
	original := append([]CostContribution(nil), children...)

	_, breakdown := ReconcileChildCost(0, children)

	if !reflect.DeepEqual(children, original) {
		t.Fatalf("input was mutated:\n got %+v\nwant %+v", children, original)
	}
	// breakdown 必须是新切片：改动它不能影响入参。
	breakdown[0].CostUSD = 12345
	breakdown[0].ChildID = "changed"
	if !reflect.DeepEqual(children, original) {
		t.Fatalf("breakdown aliases input:\n got %+v\nwant %+v", children, original)
	}
}

func TestReconcileChildCostFloatingPrecisionAndDeterminism(t *testing.T) {
	// 0.1 + 0.2 这类值存在二进制表示误差，使用容差比较。
	total, _ := ReconcileChildCost(0, []CostContribution{
		{ChildID: "a", CostUSD: 0.1},
		{ChildID: "b", CostUSD: 0.2},
	})
	if !costClose(total, 0.3) {
		t.Fatalf("total = %.17g, want ~0.3", total)
	}

	// ChildID 唯一时，输入排列不应影响求和顺序，得到逐位相同的 total。
	first := []CostContribution{
		{ChildID: "a", CostUSD: 0.1},
		{ChildID: "b", CostUSD: 0.2},
		{ChildID: "c", CostUSD: 0.3},
		{ChildID: "d", CostUSD: 0.4},
	}
	second := []CostContribution{first[3], first[1], first[0], first[2]}
	t1, b1 := ReconcileChildCost(0.05, first)
	t2, b2 := ReconcileChildCost(0.05, second)
	if t1 != t2 {
		t.Fatalf("total depends on input order: %v vs %v", t1, t2)
	}
	if !reflect.DeepEqual(b1, b2) {
		t.Fatalf("breakdown depends on input order:\n %+v\n %+v", b1, b2)
	}
}
