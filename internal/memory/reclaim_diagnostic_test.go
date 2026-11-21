package memory

import (
	"reflect"
	"strings"
	"testing"
)

// 各淘汰条件及其组合：Age 超限、被取代、无用任一命中即淘汰；
// Bytes 仅为元数据，不参与判定。
func TestPlanReclaimConditions(t *testing.T) {
	const maxAge = 10
	cands := []ReclaimCandidate{
		{Key: "fresh", Age: 1},                                 // 保留：均不命中
		{Key: "old", Age: 11},                                  // 淘汰：Age 超限
		{Key: "superseded", Age: 2, Superseded: true},          // 淘汰：被取代
		{Key: "useless", Age: 2, Useless: true},                // 淘汰：无用
		{Key: "both", Age: 2, Superseded: true, Useless: true}, // 淘汰：两个标记
		{Key: "old_superseded", Age: 99, Superseded: true},     // 淘汰：年龄+标记
		{Key: "large_bytes", Age: 1, Bytes: 1 << 30},           // 保留：Bytes 不影响
	}

	evict, keep := PlanReclaim(cands, maxAge)

	wantEvict := []string{"both", "old", "old_superseded", "superseded", "useless"}
	wantKeep := []string{"fresh", "large_bytes"}
	if !reflect.DeepEqual(evict, wantEvict) {
		t.Errorf("evict = %q, want %q", evict, wantEvict)
	}
	if !reflect.DeepEqual(keep, wantKeep) {
		t.Errorf("keep = %q, want %q", keep, wantKeep)
	}
}

// maxAge 边界：Age == maxAge 保留，Age == maxAge+1 淘汰；
// 同时覆盖零值、负值 maxAge 与 Age 为负的情形。
func TestPlanReclaimMaxAgeBoundary(t *testing.T) {
	tests := []struct {
		name   string
		maxAge int
		cands  []ReclaimCandidate
		wantEv []string
		wantKp []string
	}{
		{
			name:   "equal to maxAge is kept, one above is evicted",
			maxAge: 5,
			cands: []ReclaimCandidate{
				{Key: "four", Age: 4},
				{Key: "five", Age: 5},
				{Key: "six", Age: 6},
			},
			wantEv: []string{"six"},
			wantKp: []string{"five", "four"},
		},
		{
			name:   "zero maxAge keeps only age zero",
			maxAge: 0,
			cands: []ReclaimCandidate{
				{Key: "a0", Age: 0},
				{Key: "a1", Age: 1},
			},
			wantEv: []string{"a1"},
			wantKp: []string{"a0"},
		},
		{
			name:   "negative maxAge evicts age zero",
			maxAge: -1,
			cands: []ReclaimCandidate{
				{Key: "neg", Age: -2},
				{Key: "zero", Age: 0},
			},
			wantEv: []string{"zero"},
			wantKp: []string{"neg"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			evict, keep := PlanReclaim(tc.cands, tc.maxAge)
			if !reflect.DeepEqual(evict, tc.wantEv) {
				t.Errorf("evict = %q, want %q", evict, tc.wantEv)
			}
			if !reflect.DeepEqual(keep, tc.wantKp) {
				t.Errorf("keep = %q, want %q", keep, tc.wantKp)
			}
		})
	}
}

// CJK 键按 UTF-8 原样保留并参与排序，不因多字节字符被破坏。
func TestPlanReclaimCJKKeys(t *testing.T) {
	cands := []ReclaimCandidate{
		{Key: "项目/设计", Age: 20},
		{Key: "密钥", Age: 1, Useless: true},
		{Key: "凭证.txt", Age: 1},
		{Key: "alpha", Age: 1},
	}

	evict, keep := PlanReclaim(cands, 10)

	wantEvict := []string{"密钥", "项目/设计"}
	wantKeep := []string{"alpha", "凭证.txt"}
	if !reflect.DeepEqual(evict, wantEvict) {
		t.Errorf("evict = %q, want %q", evict, wantEvict)
	}
	if !reflect.DeepEqual(keep, wantKeep) {
		t.Errorf("keep = %q, want %q", keep, wantKeep)
	}
}

// 输出按 Key 升序排列且排序稳定：相同 Key 保持输入中的相对顺序。
func TestPlanReclaimStableOrder(t *testing.T) {
	cands := []ReclaimCandidate{
		{Key: "b", Age: 1, Useless: true},
		{Key: "a", Age: 1, Useless: true},
		{Key: "b", Age: 2, Useless: true}, // 与首个 b 同 Key，须排在其前
		{Key: "d", Age: 1},
		{Key: "c", Age: 1},
		{Key: "d", Age: 2}, // 与首个 d 同 Key，须排在其前
	}

	evict, keep := PlanReclaim(cands, 10)

	if want := []string{"a", "b", "b"}; !reflect.DeepEqual(evict, want) {
		t.Errorf("evict = %q, want %q", evict, want)
	}
	if want := []string{"c", "d", "d"}; !reflect.DeepEqual(keep, want) {
		t.Errorf("keep = %q, want %q", keep, want)
	}
}

// 纯函数：不修改入参，且返回切片与入参不共享后备数组。
func TestPlanReclaimIsPure(t *testing.T) {
	cands := []ReclaimCandidate{
		{Key: "z", Age: 99},
		{Key: "a", Age: 1},
	}
	before := append([]ReclaimCandidate(nil), cands...)

	evict, keep := PlanReclaim(cands, 10)

	if !reflect.DeepEqual(cands, before) {
		t.Fatalf("input mutated: got %#v, want %#v", cands, before)
	}
	evict[0] = "CHANGED"
	keep[0] = "CHANGED"
	if cands[0].Key != "z" || cands[1].Key != "a" {
		t.Fatalf("output aliases input: %#v", cands)
	}
}

// 空输入：nil 与空切片均返回两个 nil 名单。
func TestPlanReclaimEmpty(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cands []ReclaimCandidate
	}{
		{name: "nil", cands: nil},
		{name: "empty slice", cands: []ReclaimCandidate{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evict, keep := PlanReclaim(tc.cands, 10)
			if evict != nil || keep != nil {
				t.Fatalf("evict=%q keep=%q, want both nil", evict, keep)
			}
		})
	}
}

// 确定性：相同输入重复调用恒得相同输出。
func TestPlanReclaimDeterministic(t *testing.T) {
	cands := []ReclaimCandidate{
		{Key: "密钥", Age: 99},
		{Key: "beta", Age: 1},
		{Key: "alpha", Age: 1, Superseded: true},
	}
	firstEv, firstKp := PlanReclaim(cands, 10)
	for i := 0; i < 20; i++ {
		ev, kp := PlanReclaim(cands, 10)
		if !reflect.DeepEqual(ev, firstEv) || !reflect.DeepEqual(kp, firstKp) {
			t.Fatalf("run %d differs: evict=%q keep=%q", i, ev, kp)
		}
	}
}

// ReclaimSummary 内容稳定：计数与键都出现，顺序与传入无关，重复调用一致。
func TestReclaimSummaryStable(t *testing.T) {
	first := ReclaimSummary([]string{"密钥", "alpha"}, []string{"beta"})
	want := "回收计划：淘汰 2 项 [alpha、密钥]；保留 1 项 [beta]"
	if first != want {
		t.Errorf("summary = %q, want %q", first, want)
	}

	// 传入顺序不同，摘要仍相同。
	if got := ReclaimSummary([]string{"alpha", "密钥"}, []string{"beta"}); got != first {
		t.Errorf("order-dependent summary: %q vs %q", got, first)
	}

	// 重复调用确定性。
	for i := 0; i < 20; i++ {
		if got := ReclaimSummary([]string{"密钥", "alpha"}, []string{"beta"}); got != first {
			t.Fatalf("run %d differs: %q", i, got)
		}
	}

	// 不修改入参。
	ev := []string{"密钥", "alpha"}
	kp := []string{"beta"}
	_ = ReclaimSummary(ev, kp)
	if ev[0] != "密钥" || ev[1] != "alpha" || kp[0] != "beta" {
		t.Fatalf("input mutated: ev=%q kp=%q", ev, kp)
	}

	// 摘要涵盖计数与所有键文本。
	for _, bit := range []string{"淘汰 2 项", "保留 1 项", "alpha", "密钥", "beta"} {
		if !strings.Contains(first, bit) {
			t.Errorf("summary %q missing %q", first, bit)
		}
	}
}

// ReclaimSummary 空名单：nil 与空切片得到完全相同的稳定摘要。
func TestReclaimSummaryEmpty(t *testing.T) {
	want := "回收计划：淘汰 0 项 []；保留 0 项 []"
	for _, tc := range []struct {
		name        string
		evict, keep []string
	}{
		{name: "nil", evict: nil, keep: nil},
		{name: "empty slices", evict: []string{}, keep: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReclaimSummary(tc.evict, tc.keep); got != want {
				t.Errorf("summary = %q, want %q", got, want)
			}
		})
	}
}

// 端到端衔接：PlanReclaim 的输出交给 ReclaimSummary，CJK 与计数一致。
func TestPlanReclaimThenSummary(t *testing.T) {
	cands := []ReclaimCandidate{
		{Key: "项目/旧稿", Age: 50},
		{Key: "密钥", Age: 1, Useless: true},
		{Key: "README", Age: 1},
	}
	evict, keep := PlanReclaim(cands, 10)
	got := ReclaimSummary(evict, keep)
	want := "回收计划：淘汰 2 项 [密钥、项目/旧稿]；保留 1 项 [README]"
	if got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}
