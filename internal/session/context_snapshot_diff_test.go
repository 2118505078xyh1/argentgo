package session

import (
	"fmt"
	"reflect"
	"sort"
	"testing"
	"unicode/utf8"
)

// TestDiffContextSnapshotsClassifiesChanges 验证新增/删除/修改/未变四种情况被正确归类。
func TestDiffContextSnapshotsClassifiesChanges(t *testing.T) {
	before := map[string]string{
		"keep-same":   "v1",
		"keep-change": "v2",
		"drop":        "v3",
	}
	after := map[string]string{
		"keep-same":   "v1",
		"keep-change": "CHANGED",
		"add":         "v4",
	}

	got := DiffContextSnapshots(before, after)
	want := ContextDelta{
		Added:   []string{"add"},
		Removed: []string{"drop"},
		Changed: []string{"keep-change"},
	}
	// want 的 unchanged 无法在复合字面量里设置（字段不导出），这里单独校验。
	want.unchanged = 1

	if !reflect.DeepEqual(got, want) {
		t.Errorf("DiffContextSnapshots() = %#v, want %#v", got, want)
	}
	if got.Unchanged() != 1 {
		t.Errorf("Unchanged() = %d, want 1", got.Unchanged())
	}
}

// TestDiffContextSnapshotsSortsKeys 验证分类结果按 key 字典序排列，不受 map 遍历顺序影响。
func TestDiffContextSnapshotsSortsKeys(t *testing.T) {
	before := map[string]string{
		"delta": "1", "alpha": "1", "charlie": "1", "bravo": "1",
		"zulu-old": "1",
	}
	after := map[string]string{
		"delta": "2", "alpha": "2", "charlie": "2", "bravo": "2",
		"zulu-new": "1",
	}

	got := DiffContextSnapshots(before, after)
	if want := []string{"zulu-new"}; !reflect.DeepEqual(got.Added, want) {
		t.Errorf("Added = %v, want %v", got.Added, want)
	}
	if want := []string{"zulu-old"}; !reflect.DeepEqual(got.Removed, want) {
		t.Errorf("Removed = %v, want %v", got.Removed, want)
	}
	if want := []string{"alpha", "bravo", "charlie", "delta"}; !reflect.DeepEqual(got.Changed, want) {
		t.Errorf("Changed = %v, want %v", got.Changed, want)
	}
	for _, group := range []struct {
		name string
		keys []string
	}{
		{"Added", got.Added},
		{"Removed", got.Removed},
		{"Changed", got.Changed},
	} {
		if !sort.StringsAreSorted(group.keys) {
			t.Errorf("%s 未按字典序排列: %v", group.name, group.keys)
		}
	}
}

// TestDiffContextSnapshotsCJKKeys 验证中文 key 与值原样保留、稳定排序且 UTF-8 合法。
func TestDiffContextSnapshotsCJKKeys(t *testing.T) {
	before := map[string]string{
		"用户画像": "旧画像",
		"工具清单": "保持不变",
		"废弃来源": "待删除",
	}
	after := map[string]string{
		"用户画像": "新画像",
		"工具清单": "保持不变",
		"新增来源": "已加入",
	}

	got := DiffContextSnapshots(before, after)
	if want := []string{"新增来源"}; !reflect.DeepEqual(got.Added, want) {
		t.Errorf("Added = %v, want %v", got.Added, want)
	}
	if want := []string{"废弃来源"}; !reflect.DeepEqual(got.Removed, want) {
		t.Errorf("Removed = %v, want %v", got.Removed, want)
	}
	if want := []string{"用户画像"}; !reflect.DeepEqual(got.Changed, want) {
		t.Errorf("Changed = %v, want %v", got.Changed, want)
	}
	if got.Unchanged() != 1 {
		t.Errorf("Unchanged() = %d, want 1", got.Unchanged())
	}
	// 三个切片都只含完整字符串，不能出现被截断的半个汉字。
	for _, keys := range [][]string{got.Added, got.Removed, got.Changed} {
		for _, key := range keys {
			if !utf8.ValidString(key) {
				t.Errorf("key %q 不是完整 UTF-8", key)
			}
		}
	}
}

// TestDiffContextSnapshotsEmpty 覆盖 nil 与空 map 的各种组合。
func TestDiffContextSnapshotsEmpty(t *testing.T) {
	tests := []struct {
		name    string
		before  map[string]string
		after   map[string]string
		want    ContextDelta
		wantOld int
	}{
		{
			name: "both nil",
			want: ContextDelta{Added: []string{}, Removed: []string{}, Changed: []string{}},
		},
		{
			name:   "both empty",
			before: map[string]string{},
			after:  map[string]string{},
			want:   ContextDelta{Added: []string{}, Removed: []string{}, Changed: []string{}},
		},
		{
			name:  "all added",
			after: map[string]string{"a": "1", "b": "2"},
			want:  ContextDelta{Added: []string{"a", "b"}, Removed: []string{}, Changed: []string{}},
		},
		{
			name:   "all removed",
			before: map[string]string{"a": "1", "b": "2"},
			want:   ContextDelta{Added: []string{}, Removed: []string{"a", "b"}, Changed: []string{}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DiffContextSnapshots(tt.before, tt.after)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("DiffContextSnapshots() = %#v, want %#v", got, tt.want)
			}
			if got.Unchanged() != tt.wantOld {
				t.Errorf("Unchanged() = %d, want %d", got.Unchanged(), tt.wantOld)
			}
		})
	}
}

// TestDiffContextSnapshotsDoesNotMutateInputs 验证调用前后两个入参 map 完全不变。
func TestDiffContextSnapshotsDoesNotMutateInputs(t *testing.T) {
	before := map[string]string{"a": "1", "b": "2", "c": "3"}
	after := map[string]string{"b": "X", "c": "3", "d": "4"}
	beforeCopy := cloneStringMap(before)
	afterCopy := cloneStringMap(after)

	DiffContextSnapshots(before, after)

	if !reflect.DeepEqual(before, beforeCopy) {
		t.Errorf("before 被修改: got %#v, want %#v", before, beforeCopy)
	}
	if !reflect.DeepEqual(after, afterCopy) {
		t.Errorf("after 被修改: got %#v, want %#v", after, afterCopy)
	}
}

// TestDiffContextSnapshotsDeterministic 验证大量 key 下重复调用结果完全一致。
func TestDiffContextSnapshotsDeterministic(t *testing.T) {
	before := make(map[string]string, 64)
	after := make(map[string]string, 64)
	for i := 0; i < 64; i++ {
		key := fmt.Sprintf("key-%02d", i)
		before[key] = "value"
		after[key] = "value"
	}
	// 一半值被修改，一半被删除/新增，确保三个切片都非空时仍稳定。
	for i := 0; i < 32; i++ {
		after[fmt.Sprintf("key-%02d", i)] = "changed"
	}
	for i := 32; i < 48; i++ {
		delete(after, fmt.Sprintf("key-%02d", i))
	}
	for i := 48; i < 64; i++ {
		after[fmt.Sprintf("new-%02d", i)] = "value"
	}

	want := DiffContextSnapshots(before, after)
	for i := 0; i < 50; i++ {
		if got := DiffContextSnapshots(before, after); !reflect.DeepEqual(got, want) {
			t.Fatalf("第 %d 次调用结果不稳定:\ngot  %#v\nwant %#v", i, got, want)
		}
	}
	if len(want.Changed)+len(want.Removed)+len(want.Added) == 0 {
		t.Fatal("测试构造失败：未产生任何差异")
	}
}

// TestDiffContextSnapshotsEqualValuesNotChanged 验证值相同的 key 既不进 Changed 也不进任何其他切片。
func TestDiffContextSnapshotsEqualValuesNotChanged(t *testing.T) {
	same := map[string]string{"a": "1", "b": "", "c": "三"}
	got := DiffContextSnapshots(same, same)
	if len(got.Added) != 0 || len(got.Removed) != 0 || len(got.Changed) != 0 {
		t.Errorf("相同快照不应有差异: %#v", got)
	}
	if got.Unchanged() != 3 {
		t.Errorf("Unchanged() = %d, want 3", got.Unchanged())
	}
}

// TestDiffContextSnapshotsEmptyValueIsPresent 验证空字符串是合法值：存在但为空 ≠ 不存在。
func TestDiffContextSnapshotsEmptyValueIsPresent(t *testing.T) {
	tests := []struct {
		name    string
		before  map[string]string
		after   map[string]string
		wantAdd []string
		wantDel []string
		wantChg []string
	}{
		{"before empty value, after missing", map[string]string{"k": ""}, nil, nil, []string{"k"}, nil},
		{"before missing, after empty value", nil, map[string]string{"k": ""}, []string{"k"}, nil, nil},
		{"empty to non-empty", map[string]string{"k": ""}, map[string]string{"k": "v"}, nil, nil, []string{"k"}},
		{"non-empty to empty", map[string]string{"k": "v"}, map[string]string{"k": ""}, nil, nil, []string{"k"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DiffContextSnapshots(tt.before, tt.after)
			assertSameStrings(t, "Added", got.Added, tt.wantAdd)
			assertSameStrings(t, "Removed", got.Removed, tt.wantDel)
			assertSameStrings(t, "Changed", got.Changed, tt.wantChg)
			if got.Unchanged() != 0 {
				t.Errorf("Unchanged() = %d, want 0", got.Unchanged())
			}
		})
	}
}

// assertSameStrings 比较两个字符串切片；nil 与空切片视为等价。
func assertSameStrings(t *testing.T, name string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s = %v, want %v", name, got, want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s = %v, want %v", name, got, want)
			return
		}
	}
}

// cloneStringMap 返回 map 的独立副本，用于检测入参是否被原地修改。
func cloneStringMap(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
