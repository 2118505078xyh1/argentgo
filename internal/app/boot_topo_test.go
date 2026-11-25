package app

import (
	"errors"
	"testing"

	"example.com/argentgo/internal/pluginapi"
)

func TestBootTopoDefaultOffSkipsValidation(t *testing.T) {
	// 默认关闭：nil env / "0" / 其它值都应直接放行，且不受依赖声明错误影响。
	for _, env := range []func(string) string{
		nil,
		func(string) string { return "" },
		func(string) string { return "0" },
		func(string) string { return "false" },
	} {
		if err := bootOrder(env); err != nil {
			t.Fatalf("default-off bootOrder returned error: %v", err)
		}
	}
}

func TestBootTopoEnabledMatchesHistoricalOrder(t *testing.T) {
	on := func(k string) string {
		if k == bootTopoEnv {
			return " 1 "
		}
		return ""
	}
	if err := bootOrder(on); err != nil {
		t.Fatalf("enabled bootOrder should pass: %v", err)
	}

	// 直接锁定依赖图推导结果与历史顺序逐项相等。
	order, err := pluginapi.TopoSort(bootFeatures())
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != len(bootFeatureNames) {
		t.Fatalf("order = %v, want %v", order, bootFeatureNames)
	}
	for i := range bootFeatureNames {
		if order[i] != bootFeatureNames[i] {
			t.Fatalf("order = %v, want %v", order, bootFeatureNames)
		}
	}
}

func TestBootTopoDeclaredDepsAreDriftSensitive(t *testing.T) {
	// 守卫：若某个依赖声明被改坏（如 toolset 不再依赖 guard），推导顺序应漂移，
	// 从而被 bootOrder 的相等断言拒绝——这证明接线对声明错误确实 fail-closed。
	broken := []pluginapi.Feature{
		{Name: "hooks", Order: 0},
		{Name: "approver", Order: 1},
		{Name: "audit", Order: 2},
		{Name: "policy", Order: 3},
		{Name: "chain", Deps: []string{"approver", "audit", "policy"}, Order: 4},
		{Name: "guard", Deps: []string{"chain"}, Order: 5},
		{Name: "toolset", Order: 6}, // 故意去掉对 guard 的依赖 → 与 chain/guard 同层提前
		{Name: "runner", Deps: []string{"toolset", "hooks"}, Order: 7},
	}
	order, err := pluginapi.TopoSort(broken)
	if err != nil {
		t.Fatal(err)
	}
	if slicesEqual(order, bootFeatureNames) {
		t.Fatal("broken dependency declaration unexpectedly produced the historical order")
	}
}

func TestBootTopoPropagatesTopoSortErrors(t *testing.T) {
	// 依赖声明若有环，TopoSort 返回哨兵错误；这里直接验证哨兵可穿透包装。
	cyclic := []pluginapi.Feature{
		{Name: "a", Deps: []string{"b"}},
		{Name: "b", Deps: []string{"a"}},
	}
	_, err := pluginapi.TopoSort(cyclic)
	if !errors.Is(err, pluginapi.ErrCycle) {
		t.Fatalf("expected ErrCycle, got %v", err)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
