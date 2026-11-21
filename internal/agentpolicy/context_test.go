package agentpolicy

import (
	"strings"
	"testing"
)

// TestSourceIDWireValues 锁定来源标识的稳定字符串取值，防止缓存键与日志格式漂移。
func TestSourceIDWireValues(t *testing.T) {
	cases := map[SourceID]string{
		SourceSystemStatic:      "system.static",
		SourceSystemDynamic:     "system.dynamic",
		SourceToolsSpecs:        "tools.specs",
		SourceHistoryUser:       "history.user",
		SourceHistoryTool:       "history.tool",
		SourceSteer:             "steer",
		SourceReminder:          "reminder",
		SourceCompactionSummary: "compaction.summary",
	}
	for id, want := range cases {
		if string(id) != want {
			t.Errorf("SourceID = %q, want %q", id, want)
		}
	}
}

// TestEpochDeterministic 验证 Epoch 的确定性：重复调用一致，且 Stable 来源重排不改变结果。
func TestEpochDeterministic(t *testing.T) {
	srcs := []Source{
		{ID: SourceSystemStatic, Stable: true, Digest: "static-v1"},
		{ID: SourceToolsSpecs, Stable: true, Digest: "tools-v1"},
		{ID: SourceSystemDynamic, Stable: false, Digest: "dynamic-v1"},
	}
	first := NewContextState(srcs).Epoch()
	if len(first) != 64 {
		t.Fatalf("Epoch length = %d, want 64 (sha256 hex)", len(first))
	}
	for i := 0; i < 5; i++ {
		if got := NewContextState(srcs).Epoch(); got != first {
			t.Fatalf("Epoch call %d = %q, want %q", i, got, first)
		}
	}
	// 稳定段顺序颠倒、易变段位置改变都不应改变 Epoch。
	reordered := []Source{srcs[1], srcs[2], srcs[0]}
	if got := NewContextState(reordered).Epoch(); got != first {
		t.Fatalf("reordered Epoch = %q, want %q", got, first)
	}
}

// TestEpochOnlyStableSourcesMatter 验证只有 Stable 来源影响 Epoch：
// 易变段的变化（改值/增/删）都不改变 Epoch，稳定段的变化则必定改变。
func TestEpochOnlyStableSourcesMatter(t *testing.T) {
	base := []Source{
		{ID: SourceSystemStatic, Stable: true, Digest: "s1"},
		{ID: SourceSteer, Stable: false, Digest: "v1"},
	}
	baseEpoch := NewContextState(base).Epoch()

	cases := []struct {
		name     string
		srcs     []Source
		wantSame bool // 是否与 baseEpoch 相同
	}{
		{
			name:     "volatile digest changed",
			srcs:     []Source{base[0], {ID: SourceSteer, Stable: false, Digest: "v2"}},
			wantSame: true,
		},
		{
			name:     "volatile added",
			srcs:     []Source{base[0], base[1], {ID: SourceReminder, Stable: false, Digest: "v3"}},
			wantSame: true,
		},
		{
			name:     "volatile removed",
			srcs:     []Source{base[0]},
			wantSame: true,
		},
		{
			name:     "stable digest changed",
			srcs:     []Source{{ID: SourceSystemStatic, Stable: true, Digest: "s2"}, base[1]},
			wantSame: false,
		},
		{
			name:     "stable added",
			srcs:     []Source{base[0], base[1], {ID: SourceToolsSpecs, Stable: true, Digest: "t1"}},
			wantSame: false,
		},
		{
			name:     "stable removed",
			srcs:     []Source{base[1]},
			wantSame: false,
		},
		{
			name:     "empty state differs from non-empty",
			srcs:     nil,
			wantSame: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NewContextState(tc.srcs).Epoch()
			if same := got == baseEpoch; same != tc.wantSame {
				t.Fatalf("Epoch same = %v, want %v (got %q, base %q)", same, tc.wantSame, got, baseEpoch)
			}
		})
	}
}

// TestEpochCJKAndLengthBoundaries 验证 CJK 多字节内容与长度边界：
// 编码按字节计长，任意切分都不会产生拼接碰撞。
func TestEpochCJKAndLengthBoundaries(t *testing.T) {
	// 同一 ID 下多个 Digest，专门考验长度前缀消除拼接歧义。
	sameID := func(digests ...string) string {
		srcs := make([]Source, 0, len(digests))
		for _, d := range digests {
			srcs = append(srcs, Source{ID: SourceSystemStatic, Stable: true, Digest: d})
		}
		return NewContextState(srcs).Epoch()
	}

	cjk := sameID("你好世界", "工具规格🔥")
	if len(cjk) != 64 || cjk != sameID("你好世界", "工具规格🔥") {
		t.Fatalf("CJK Epoch unstable: %q", cjk)
	}

	// 朴素拼接会把这两组都变成 "abc"，长度前缀必须区分它们。
	if sameID("ab", "c") == sameID("a", "bc") {
		t.Fatal("concatenation ambiguity: [ab,c] and [a,bc] produced equal Epoch")
	}
	if sameID("界", "abc") == sameID("界a", "bc") {
		t.Fatal("CJK byte-length ambiguity produced equal Epoch")
	}

	// 长串边界以及差一个字符的差异都必须被捕获。
	long := strings.Repeat("界", 10000)
	if sameID(long) == sameID(long+"界") {
		t.Fatal("long digest and its extension produced equal Epoch")
	}
}

// TestEpochDuplicateSourcesStable 验证重复来源不会破坏确定性：
// 重复项颠倒顺序仍得到相同 Epoch。
func TestEpochDuplicateSourcesStable(t *testing.T) {
	srcs := []Source{
		{ID: SourceSystemStatic, Stable: true, Digest: "x"},
		{ID: SourceSystemStatic, Stable: true, Digest: "x"},
		{ID: SourceToolsSpecs, Stable: true, Digest: "y"},
	}
	first := NewContextState(srcs).Epoch()
	for i := 0; i < 5; i++ {
		if got := NewContextState(srcs).Epoch(); got != first {
			t.Fatalf("duplicate Epoch call %d = %q, want %q", i, got, first)
		}
	}
	reordered := []Source{srcs[2], srcs[1], srcs[0]}
	if got := NewContextState(reordered).Epoch(); got != first {
		t.Fatalf("reordered duplicate Epoch = %q, want %q", got, first)
	}
}

// TestDiffIgnoresVolatile 锁定 Diff 的差异语义：忽略 volatile，按 LCS 对齐稳定段，
// 区分 changed / added / removed，且顺序敏感。
func TestDiffIgnoresVolatile(t *testing.T) {
	cases := []struct {
		name string
		prev []Source
		next []Source
		want []SourceDelta
	}{
		{
			name: "volatile only changes",
			prev: []Source{
				{ID: SourceSystemStatic, Stable: true, Digest: "s"},
				{ID: SourceSteer, Stable: false, Digest: "v1"},
			},
			next: []Source{
				{ID: SourceSystemStatic, Stable: true, Digest: "s"},
				{ID: SourceSteer, Stable: false, Digest: "v2"},
				{ID: SourceReminder, Stable: false, Digest: "v3"},
			},
			want: nil,
		},
		{
			name: "identical stable segments",
			prev: []Source{
				{ID: SourceSystemStatic, Stable: true, Digest: "s"},
				{ID: SourceToolsSpecs, Stable: true, Digest: "t"},
			},
			next: []Source{
				{ID: SourceSystemStatic, Stable: true, Digest: "s"},
				{ID: SourceToolsSpecs, Stable: true, Digest: "t"},
				{ID: SourceSteer, Stable: false, Digest: "v"},
			},
			want: nil,
		},
		{
			name: "stable digest changed",
			prev: []Source{{ID: SourceSystemStatic, Stable: true, Digest: "s1"}},
			next: []Source{{ID: SourceSystemStatic, Stable: true, Digest: "s2"}},
			want: []SourceDelta{{ID: SourceSystemStatic, Kind: DeltaChanged, Before: "s1", After: "s2"}},
		},
		{
			name: "stable added",
			prev: []Source{{ID: SourceSystemStatic, Stable: true, Digest: "s"}},
			next: []Source{
				{ID: SourceSystemStatic, Stable: true, Digest: "s"},
				{ID: SourceToolsSpecs, Stable: true, Digest: "t"},
			},
			want: []SourceDelta{{ID: SourceToolsSpecs, Kind: DeltaAdded, After: "t"}},
		},
		{
			name: "stable removed",
			prev: []Source{
				{ID: SourceSystemStatic, Stable: true, Digest: "s"},
				{ID: SourceToolsSpecs, Stable: true, Digest: "t"},
			},
			next: []Source{{ID: SourceSystemStatic, Stable: true, Digest: "s"}},
			want: []SourceDelta{{ID: SourceToolsSpecs, Kind: DeltaRemoved, Before: "t"}},
		},
		{
			name: "reorder is removal plus addition",
			prev: []Source{
				{ID: SourceSystemStatic, Stable: true, Digest: "s"},
				{ID: SourceToolsSpecs, Stable: true, Digest: "t"},
			},
			next: []Source{
				{ID: SourceToolsSpecs, Stable: true, Digest: "t"},
				{ID: SourceSystemStatic, Stable: true, Digest: "s"},
			},
			want: []SourceDelta{
				{ID: SourceSystemStatic, Kind: DeltaRemoved, Before: "s"},
				{ID: SourceSystemStatic, Kind: DeltaAdded, After: "s"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Diff(NewContextState(tc.prev), NewContextState(tc.next))
			if len(got) != len(tc.want) {
				t.Fatalf("Diff = %#v, want %#v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("Diff[%d] = %#v, want %#v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestAdmitAtBoundary 验证边界接纳：零值与关闭均为 no-op，开放才追加来源。
func TestAdmitAtBoundary(t *testing.T) {
	base := NewContextState([]Source{{ID: SourceSystemStatic, Stable: true, Digest: "s"}})
	incoming := Source{ID: SourceSteer, Stable: false, Digest: "v"}

	cases := []struct {
		name      string
		boundary  Boundary
		src       Source
		wantLen   int
		wantSteer bool
	}{
		{"zero boundary noop", BoundaryNone, incoming, 1, false},
		{"closed boundary noop", BoundaryClosed, incoming, 1, false},
		{"unknown boundary noop", Boundary("other"), incoming, 1, false},
		{"open admits", BoundaryOpen, incoming, 2, true},
		{"open with empty source noop", BoundaryOpen, Source{}, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AdmitAtBoundary(base, tc.boundary, tc.src)
			if got.Len() != tc.wantLen {
				t.Fatalf("AdmitAtBoundary Len = %d, want %d", got.Len(), tc.wantLen)
			}
			hasSteer := false
			for _, src := range got.Sources() {
				if src.ID == SourceSteer {
					hasSteer = true
				}
			}
			if hasSteer != tc.wantSteer {
				t.Fatalf("hasSteer = %v, want %v", hasSteer, tc.wantSteer)
			}
			// 纯度：入参状态不得被修改。
			if base.Len() != 1 || base.Sources()[0].Digest != "s" {
				t.Fatalf("prev mutated: %#v", base.Sources())
			}
		})
	}
}

// TestPurityNoInputMutation 验证构造、读取、Diff、接纳都不修改入参或共享底层切片。
func TestPurityNoInputMutation(t *testing.T) {
	srcs := []Source{{ID: SourceSystemStatic, Stable: true, Digest: "s"}}
	state := NewContextState(srcs)
	srcs[0].Digest = "changed"
	if state.Sources()[0].Digest != "s" {
		t.Fatal("NewContextState aliased its input slice")
	}

	out := state.Sources()
	out[0].Digest = "mutated"
	if state.Sources()[0].Digest != "s" {
		t.Fatal("Sources leaked the internal slice")
	}

	prev := NewContextState([]Source{{ID: SourceSystemStatic, Stable: true, Digest: "a"}})
	next := NewContextState([]Source{{ID: SourceSystemStatic, Stable: true, Digest: "b"}})
	Diff(prev, next)
	if prev.Sources()[0].Digest != "a" || next.Sources()[0].Digest != "b" {
		t.Fatalf("Diff mutated inputs: prev=%#v next=%#v", prev.Sources(), next.Sources())
	}

	admitted := AdmitAtBoundary(state, BoundaryOpen, Source{ID: SourceSteer, Digest: "v"})
	admittedSources := admitted.Sources()
	admittedSources[0].Digest = "mutated"
	if state.Sources()[0].Digest != "s" {
		t.Fatal("AdmitAtBoundary shared its backing slice with prev")
	}
}

// TestZeroValueState 锁定零值行为：空状态合法、Epoch 确定、Diff 仅对新增报告。
func TestZeroValueState(t *testing.T) {
	var zero ContextState
	if zero.Len() != 0 {
		t.Fatalf("zero Len = %d, want 0", zero.Len())
	}
	if epoch := zero.Epoch(); len(epoch) != 64 || epoch != NewContextState(nil).Epoch() {
		t.Fatalf("zero Epoch unstable: %q", epoch)
	}
	added := NewContextState([]Source{{ID: SourceSystemStatic, Stable: true, Digest: "s"}})
	got := Diff(zero, added)
	want := []SourceDelta{{ID: SourceSystemStatic, Kind: DeltaAdded, After: "s"}}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("Diff(zero, added) = %#v, want %#v", got, want)
	}
	// 零值边界对零值状态也是 no-op。
	if admitted := AdmitAtBoundary(zero, BoundaryNone, Source{ID: SourceSteer}); admitted.Len() != 0 {
		t.Fatalf("zero boundary admitted %#v", admitted.Sources())
	}
}

// TestEpochDuplicateDigestNotDeduplicated 记录一个设计决定：重复来源按出现次数参与哈希，
// 因此新增一条完全相同的稳定段会改变 Epoch（重复不会被视为去重）。
func TestEpochDuplicateDigestNotDeduplicated(t *testing.T) {
	one := NewContextState([]Source{{ID: SourceSystemStatic, Stable: true, Digest: "x"}}).Epoch()
	two := NewContextState([]Source{
		{ID: SourceSystemStatic, Stable: true, Digest: "x"},
		{ID: SourceSystemStatic, Stable: true, Digest: "x"},
	}).Epoch()
	if one == two {
		t.Fatal("duplicate stable source was deduplicated in Epoch")
	}
}
