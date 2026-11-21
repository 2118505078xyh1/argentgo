package provider_test

import (
	"reflect"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/provider"
)

// cloneStringSlice 复制切片并保留 nil 与空切片的区分，用于入参不变性断言。
func cloneStringSlice(s []string) []string {
	if s == nil {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	return out
}

func TestSnapshotTemplatesNames(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{
			name:  "nil 输入得到 nil Names",
			input: nil,
			want:  nil,
		},
		{
			name:  "空切片得到空 Names",
			input: []string{},
			want:  []string{},
		},
		{
			name:  "单项无需排序",
			input: []string{"anthropic"},
			want:  []string{"anthropic"},
		},
		{
			name:  "乱序 ASCII 按字节序排序",
			input: []string{"zhipu", "anthropic", "openai", "deepseek"},
			want:  []string{"anthropic", "deepseek", "openai", "zhipu"},
		},
		{
			name:  "ASCII 排在 CJK 之前且 CJK 按 UTF-8 字节序",
			input: []string{"温度", "a", "中文", "Z"},
			// 'Z'(0x5A) < 'a'(0x61) < "中"(E4 B8 AD) < "温"(E6 B8 A9)。
			want: []string{"Z", "a", "中文", "温度"},
		},
		{
			name:  "重复名称保留且相邻",
			input: []string{"b", "a", "b"},
			want:  []string{"a", "b", "b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := cloneStringSlice(tt.input)
			got := provider.SnapshotTemplates(tt.input)
			if !reflect.DeepEqual(got.Names, tt.want) {
				t.Fatalf("Names = %#v, want %#v", got.Names, tt.want)
			}
			if !reflect.DeepEqual(tt.input, original) {
				t.Fatalf("SnapshotTemplates 修改了入参: input=%#v, 原始=%#v", tt.input, original)
			}
		})
	}
}

func TestSnapshotTemplatesFingerprintKnownValues(t *testing.T) {
	// 固定字面量锁定「字节长度前缀 + 名称」编码格式与稳定排序。
	const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	tests := []struct {
		name  string
		input []string
		want  string
	}{
		{name: "nil", input: nil, want: emptyHash},
		{name: "空切片", input: []string{}, want: emptyHash},
		{name: "单项", input: []string{"anthropic"}, want: "ab25d8f01a2988c66946121ee5c5c819c753105a2c4ef2d6d0d12cb6436d3e75"},
		{name: "三项乱序", input: []string{"openai", "anthropic", "deepseek"}, want: "4095180a862a28ac4fbb856b6bd3a76c4afcfb2b5cdf9303ce10daef06cef2ff"},
		{name: "含 CJK", input: []string{"中文", "a"}, want: "0f25bbad99e89f90454e57563715b949c1e04ce9a3eada9ea587f8cc658191d5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := provider.SnapshotTemplates(tt.input)
			if got.Fingerprint != tt.want {
				t.Fatalf("Fingerprint = %q, want %q", got.Fingerprint, tt.want)
			}
		})
	}
}

func TestSnapshotTemplatesFingerprintStable(t *testing.T) {
	// 同一名称集合，任意入参顺序、任意调用次数，摘要都一致。
	orders := [][]string{
		{"anthropic", "openai", "deepseek"},
		{"deepseek", "anthropic", "openai"},
		{"openai", "deepseek", "anthropic"},
	}
	want := provider.SnapshotTemplates(orders[0]).Fingerprint
	for i, names := range orders {
		for call := 0; call < 20; call++ {
			if got := provider.SnapshotTemplates(names).Fingerprint; got != want {
				t.Fatalf("顺序 %d 第 %d 次调用 Fingerprint = %q, want %q", i, call, got, want)
			}
		}
	}
}

func TestSnapshotTemplatesFingerprintDistinguishesInputs(t *testing.T) {
	// 长度前缀消除拼接歧义：两组名称拼接出的裸字符串相同，摘要必须不同。
	a := provider.SnapshotTemplates([]string{"ab", "c"}).Fingerprint
	b := provider.SnapshotTemplates([]string{"a", "bc"}).Fingerprint
	if a == b {
		t.Fatalf("不同输入得到相同 Fingerprint: %q", a)
	}

	// 集合不同（增删、仅 CJK 变化）摘要都应不同。
	pairs := [][2][]string{
		{{"a"}, {"a", "b"}},
		{{"a", "b"}, {"a"}},
		{{"a"}, {"A"}},
		{{"中文"}, {"温度"}},
		{{"a", "b"}, {"a", "b", "b"}},
	}
	for _, pair := range pairs {
		fa := provider.SnapshotTemplates(pair[0]).Fingerprint
		fb := provider.SnapshotTemplates(pair[1]).Fingerprint
		if fa == fb {
			t.Fatalf("Fingerprint(%q) 与 Fingerprint(%q) 意外相同: %q", pair[0], pair[1], fa)
		}
	}
}

func TestSnapshotTemplatesDoesNotMutateInput(t *testing.T) {
	input := []string{"zhipu", "anthropic", "openai"}
	original := []string{"zhipu", "anthropic", "openai"}

	snap := provider.SnapshotTemplates(input)
	if !reflect.DeepEqual(input, original) {
		t.Fatalf("SnapshotTemplates 修改了入参: input=%#v", input)
	}

	// 修改快照的 Names 不影响入参，也不影响再次构造的快照（底层数组不共享）。
	snap.Names[0] = "mutated"
	snap.Names = append(snap.Names, "extra")
	if !reflect.DeepEqual(input, original) {
		t.Fatalf("修改快照影响了入参: input=%#v", input)
	}
	again := provider.SnapshotTemplates(input)
	if !reflect.DeepEqual(again.Names, []string{"anthropic", "openai", "zhipu"}) {
		t.Fatalf("再次构造的快照被污染: Names=%#v", again.Names)
	}
}

func TestDiffSnapshots(t *testing.T) {
	tests := []struct {
		name       string
		a          []string
		b          []string
		wantAdded  []string
		wantRemove []string
	}{
		{
			name:       "两侧均为空",
			a:          nil,
			b:          nil,
			wantAdded:  []string{},
			wantRemove: []string{},
		},
		{
			name:       "完全相同",
			a:          []string{"a", "b"},
			b:          []string{"a", "b"},
			wantAdded:  []string{},
			wantRemove: []string{},
		},
		{
			name:       "仅新增",
			a:          []string{"a"},
			b:          []string{"a", "b", "c"},
			wantAdded:  []string{"b", "c"},
			wantRemove: []string{},
		},
		{
			name:       "仅删除",
			a:          []string{"a", "b", "c"},
			b:          []string{"a"},
			wantAdded:  []string{},
			wantRemove: []string{"b", "c"},
		},
		{
			name:       "新增与删除同时存在且稳定排序",
			a:          []string{"z", "a", "gone"},
			b:          []string{"a", "new", "Z", "中文"},
			wantAdded:  []string{"Z", "new", "中文"},
			wantRemove: []string{"gone", "z"},
		},
		{
			name:       "a 为空全部为新增",
			a:          nil,
			b:          []string{"b", "a"},
			wantAdded:  []string{"a", "b"},
			wantRemove: []string{},
		},
		{
			name:       "b 为空全部为删除",
			a:          []string{"b", "a"},
			b:          nil,
			wantAdded:  []string{},
			wantRemove: []string{"a", "b"},
		},
		{
			name:       "重复名称按集合语义不重复报告",
			a:          []string{"a", "a", "b"},
			b:          []string{"a", "c", "c"},
			wantAdded:  []string{"c"},
			wantRemove: []string{"b"},
		},
		{
			name:       "CJK 名称增减",
			a:          []string{"中文", "a", "旧"},
			b:          []string{"中文", "a", "新"},
			wantAdded:  []string{"新"},
			wantRemove: []string{"旧"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := provider.SnapshotTemplates(tt.a)
			b := provider.SnapshotTemplates(tt.b)
			added, removed := provider.DiffSnapshots(a, b)
			if !reflect.DeepEqual(added, tt.wantAdded) {
				t.Fatalf("added = %#v, want %#v", added, tt.wantAdded)
			}
			if !reflect.DeepEqual(removed, tt.wantRemove) {
				t.Fatalf("removed = %#v, want %#v", removed, tt.wantRemove)
			}
		})
	}
}

func TestDiffSnapshotsIgnoresFingerprintAndOrder(t *testing.T) {
	// DiffSnapshots 只按 Names 比较：Fingerprint 字段以及手工构造的乱序 Names 都不影响结果。
	a := provider.TemplateSnapshot{Names: []string{"z", "a", "b"}, Fingerprint: "stale"}
	b := provider.TemplateSnapshot{Names: []string{"b", "c", "a"}, Fingerprint: "also-stale"}
	added, removed := provider.DiffSnapshots(a, b)
	if !reflect.DeepEqual(added, []string{"c"}) || !reflect.DeepEqual(removed, []string{"z"}) {
		t.Fatalf("added=%#v removed=%#v, want added=[c] removed=[z]", added, removed)
	}
}

func TestDiffSnapshotsStableAndDoesNotMutateInput(t *testing.T) {
	a := provider.SnapshotTemplates([]string{"中文", "z", "a", "gone"})
	b := provider.SnapshotTemplates([]string{"new", "a", "Z", "温度"})
	wantAdded := []string{"Z", "new", "温度"}
	wantRemoved := []string{"gone", "z", "中文"}

	origA := cloneStringSlice(a.Names)
	origB := cloneStringSlice(b.Names)
	for i := 0; i < 50; i++ {
		added, removed := provider.DiffSnapshots(a, b)
		if !reflect.DeepEqual(added, wantAdded) || !reflect.DeepEqual(removed, wantRemoved) {
			t.Fatalf("第 %d 次调用 added=%#v removed=%#v", i, added, removed)
		}
		if !reflect.DeepEqual(a.Names, origA) || !reflect.DeepEqual(b.Names, origB) {
			t.Fatalf("DiffSnapshots 修改了入参: a=%#v b=%#v", a.Names, b.Names)
		}
		// 修改返回值不影响任一快照。
		added[0] = "mutated"
		removed = append(removed, "extra")
	}
	if !reflect.DeepEqual(a.Names, origA) || !reflect.DeepEqual(b.Names, origB) {
		t.Fatalf("修改返回值影响了入参: a=%#v b=%#v", a.Names, b.Names)
	}
}
