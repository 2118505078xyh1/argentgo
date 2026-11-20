package tui

import (
	"reflect"
	"testing"
)

func TestResolveBindingSingleAlias(t *testing.T) {
	aliases := map[string][]string{
		"submit": {"enter"},
	}
	action, ok := resolveBinding("enter", aliases)
	if !ok || action != "submit" {
		t.Fatalf("resolveBinding(enter) = (%q, %v), want (submit, true)", action, ok)
	}
}

func TestResolveBindingMultipleAliases(t *testing.T) {
	// 一个动作可绑定多个别名，不同动作各自维护别名列表。
	aliases := map[string][]string{
		"down": {"down", "ctrl+j", "j"},
		"copy": {"c", "y"},
	}
	cases := []struct {
		key  string
		want string
	}{
		{"down", "down"},
		{"ctrl+j", "down"},
		{"j", "down"},
		{"c", "copy"},
		{"y", "copy"},
	}
	for _, tc := range cases {
		action, ok := resolveBinding(tc.key, aliases)
		if !ok || action != tc.want {
			t.Errorf("resolveBinding(%q) = (%q, %v), want (%q, true)", tc.key, action, ok, tc.want)
		}
	}
}

func TestResolveBindingCaseInsensitive(t *testing.T) {
	// 大小写折叠同时作用于按键串与别名定义。
	aliases := map[string][]string{
		"down": {"Ctrl+J", "J"},
	}
	for _, key := range []string{"ctrl+j", "CTRL+J", "Ctrl+J", "j", "J"} {
		action, ok := resolveBinding(key, aliases)
		if !ok || action != "down" {
			t.Errorf("resolveBinding(%q) = (%q, %v), want (down, true)", key, action, ok)
		}
	}
}

func TestResolveBindingCJK(t *testing.T) {
	// CJK 别名按原样比较，无大小写之别；与 ASCII 别名可共存于同一动作。
	aliases := map[string][]string{
		"copy": {"复制", "c"},
	}
	for _, key := range []string{"复制", "c", "C"} {
		action, ok := resolveBinding(key, aliases)
		if !ok || action != "copy" {
			t.Errorf("resolveBinding(%q) = (%q, %v), want (copy, true)", key, action, ok)
		}
	}
	if action, ok := resolveBinding("粘贴", aliases); ok || action != "" {
		t.Fatalf("an unrelated CJK key must miss: (%q, %v)", action, ok)
	}
}

func TestResolveBindingMiss(t *testing.T) {
	aliases := map[string][]string{"down": {"j"}}
	if action, ok := resolveBinding("k", aliases); ok || action != "" {
		t.Fatalf("resolveBinding(k) = (%q, %v), want (\"\", false)", action, ok)
	}
}

func TestResolveBindingEmptyTable(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		aliases map[string][]string
	}{
		{"nil 表", "j", nil},
		{"空表", "j", map[string][]string{}},
		{"空按键", "", map[string][]string{"down": {"j"}}},
	}
	for _, tc := range cases {
		action, ok := resolveBinding(tc.key, tc.aliases)
		if ok || action != "" {
			t.Errorf("%s: resolveBinding(%q) = (%q, %v), want (\"\", false)", tc.name, tc.key, action, ok)
		}
	}
}

func TestResolveBindingStableOnConflict(t *testing.T) {
	// 两个动作声明同一别名时，结果应恒为字典序较小的动作，
	// 不随 map 遍历顺序或调用次数波动。
	aliases := map[string][]string{
		"zeta":  {"shared"},
		"alpha": {"shared"},
	}
	for i := 0; i < 32; i++ {
		action, ok := resolveBinding("shared", aliases)
		if !ok || action != "alpha" {
			t.Fatalf("iteration %d: resolveBinding(shared) = (%q, %v), want (alpha, true)", i, action, ok)
		}
	}
}

func TestResolveBindingDuplicateAliases(t *testing.T) {
	// 同一动作的别名列表里出现重复/大小写变体时仍能正确命中。
	aliases := map[string][]string{"down": {"j", "J", "j"}}
	action, ok := resolveBinding("j", aliases)
	if !ok || action != "down" {
		t.Fatalf("resolveBinding(j) = (%q, %v), want (down, true)", action, ok)
	}
}

func TestAddAliasDeduplicates(t *testing.T) {
	aliases := make(map[string][]string)
	if added := addAlias(aliases, "copy", "c"); !added {
		t.Fatal("first add should report added")
	}
	// 大小写不同的重复别名不应再次写入。
	if added := addAlias(aliases, "copy", "C"); added {
		t.Fatal("case-variant duplicate should be skipped")
	}
	if added := addAlias(aliases, "copy", "c"); added {
		t.Fatal("exact duplicate should be skipped")
	}
	if added := addAlias(aliases, "copy", "y"); !added {
		t.Fatal("a distinct alias should be added")
	}
	want := []string{"c", "y"}
	if got := aliases["copy"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("aliases[copy] = %v, want %v", got, want)
	}
	// nil 表与空别名是安全的空操作。
	if addAlias(nil, "copy", "z") || addAlias(aliases, "copy", "") {
		t.Fatal("nil map and empty alias must be no-ops")
	}
}

func TestMergeAliases(t *testing.T) {
	dst := map[string][]string{"copy": {"c"}}
	src := map[string][]string{
		"copy": {"C", "y"}, // C 与已有 c 重复，应跳过；y 应加入
		"down": {"j"},
	}
	mergeAliases(dst, src)

	if got, want := dst["copy"], []string{"c", "y"}; !reflect.DeepEqual(got, want) {
		t.Errorf("dst[copy] = %v, want %v", got, want)
	}
	if got, want := dst["down"], []string{"j"}; !reflect.DeepEqual(got, want) {
		t.Errorf("dst[down] = %v, want %v", got, want)
	}

	// 空源表不得修改目标表。
	before := map[string][]string{"copy": {"c"}}
	mergeAliases(before, nil)
	if got, want := before, (map[string][]string{"copy": {"c"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("empty src modified dst: %v, want %v", got, want)
	}
}
