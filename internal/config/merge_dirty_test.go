package config

import (
	"reflect"
	"testing"
)

// 顶层脏字段从 changed 覆盖，其它顶层字段保留 base 的值。
func TestMergeDirtyFieldsTopLevel(t *testing.T) {
	base := map[string]any{"model": "old", "maxTurns": 5, "provider": "x"}
	changed := map[string]any{"model": "new", "maxTurns": 99, "provider": "y"}

	got := mergeDirtyFields(base, changed, []string{"model"})
	want := map[string]any{"model": "new", "maxTurns": 5, "provider": "x"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeDirtyFields() = %#v, want %#v", got, want)
	}
}

// 嵌套点分键只覆盖叶子字段：兄弟键、祖先的其它键以及 changed 中额外的键
// 都不应被合并进来。
func TestMergeDirtyFieldsNested(t *testing.T) {
	base := map[string]any{
		"provider": "x",
		"providers": map[string]any{
			"x": map[string]any{
				"baseURL": "old-url",
				"apiKey":  "keep-secret",
				"models":  []any{"a", "b"},
			},
		},
	}
	changed := map[string]any{
		"providers": map[string]any{
			"x": map[string]any{
				"baseURL": "new-url",
				"apiKey":  "changed-secret",
				"extra":   true,
			},
		},
	}

	got := mergeDirtyFields(base, changed, []string{"providers.x.baseURL"})
	want := map[string]any{
		"provider": "x",
		"providers": map[string]any{
			"x": map[string]any{
				"baseURL": "new-url",
				"apiKey":  "keep-secret",
				"models":  []any{"a", "b"},
			},
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeDirtyFields() = %#v, want %#v", got, want)
	}
}

// 多个脏键同时生效：顶层键与嵌套键可以混用，未被标记的字段保持原值。
func TestMergeDirtyFieldsMultiple(t *testing.T) {
	base := map[string]any{
		"a": 1, "b": 2, "c": 3,
		"nested": map[string]any{"p": 1, "q": 2, "r": 3},
	}
	changed := map[string]any{
		"a": 10, "b": 20, "c": 30,
		"nested": map[string]any{"p": 9, "q": 8, "r": 7},
	}

	got := mergeDirtyFields(base, changed, []string{"a", "nested.q", "c"})
	want := map[string]any{
		"a": 10, "b": 2, "c": 30,
		"nested": map[string]any{"p": 1, "q": 8, "r": 3},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeDirtyFields() = %#v, want %#v", got, want)
	}
}

// 空脏键列表返回 base 的拷贝：内容相等，但不是同一个 map，修改返回值
// 不会回写到 base。
func TestMergeDirtyFieldsEmptyDirty(t *testing.T) {
	base := map[string]any{"a": 1, "nested": map[string]any{"b": 2}}
	changed := map[string]any{"a": 99}

	for _, dirty := range [][]string{nil, []string{}} {
		got := mergeDirtyFields(base, changed, dirty)
		want := map[string]any{"a": 1, "nested": map[string]any{"b": 2}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("dirty=%v: mergeDirtyFields() = %#v, want %#v", dirty, got, want)
		}

		got["a"] = 100
		got["nested"].(map[string]any)["b"] = 200
		if base["a"] != 1 || base["nested"].(map[string]any)["b"] != 2 {
			t.Fatalf("dirty=%v: 修改返回值改动了 base：%#v", dirty, base)
		}
	}
}

// CJK 键按字面量分段匹配：点分路径中的中文键与含 CJK 的叶子值都能正确处理。
func TestMergeDirtyFieldsCJKKeys(t *testing.T) {
	base := map[string]any{
		"模型": "旧模型",
		"提供商": map[string]any{
			"甲": map[string]any{
				"地址": "old-url",
				"密钥": "keep-secret",
			},
		},
	}
	changed := map[string]any{
		"模型": "新模型",
		"提供商": map[string]any{
			"甲": map[string]any{
				"地址": "新地址",
				"密钥": "changed-secret",
			},
		},
	}

	got := mergeDirtyFields(base, changed, []string{"模型", "提供商.甲.地址"})
	want := map[string]any{
		"模型": "新模型",
		"提供商": map[string]any{
			"甲": map[string]any{
				"地址": "新地址",
				"密钥": "keep-secret",
			},
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeDirtyFields() = %#v, want %#v", got, want)
	}
}

// 函数不得修改入参：调用前后 base/changed 保持一致；返回值经过深层改写后
// 也不能反向影响入参（说明返回值是深拷贝而非共享引用）。
func TestMergeDirtyFieldsDoesNotMutateInputs(t *testing.T) {
	base := map[string]any{
		"a": 1,
		"nested": map[string]any{
			"b":    2,
			"list": []any{"x", "y"},
		},
	}
	changed := map[string]any{
		"a": 9,
		"nested": map[string]any{
			"b":    8,
			"list": []any{"p", "q"},
		},
	}
	baseBefore := cloneTree(base)
	changedBefore := cloneTree(changed)

	got := mergeDirtyFields(base, changed, []string{"a", "nested.b", "nested.list"})

	if !reflect.DeepEqual(base, baseBefore) {
		t.Fatalf("调用修改了 base：%#v, want %#v", base, baseBefore)
	}
	if !reflect.DeepEqual(changed, changedBefore) {
		t.Fatalf("调用修改了 changed：%#v, want %#v", changed, changedBefore)
	}

	// 深层改写返回值：标量、嵌套 map 与切片元素全部改动。
	got["a"] = 100
	nested := got["nested"].(map[string]any)
	nested["b"] = 200
	nested["list"].([]any)[0] = "z"

	if !reflect.DeepEqual(base, baseBefore) {
		t.Fatalf("改写返回值后 base 被改动：%#v, want %#v", base, baseBefore)
	}
	if !reflect.DeepEqual(changed, changedBefore) {
		t.Fatalf("改写返回值后 changed 被改动：%#v, want %#v", changed, changedBefore)
	}
}

// changed 中不存在的脏键（包括空路径与无法导航的路径）被跳过，保留 base 原值；
// 显式 nil 值算作存在，会被写入结果。
func TestMergeDirtyFieldsMissingAndNull(t *testing.T) {
	base := map[string]any{
		"keep":   "v",
		"nullme": "old",
		"scalar": "not-an-object",
	}
	changed := map[string]any{"nullme": nil}

	got := mergeDirtyFields(base, changed, []string{
		"", "absent", "nullme", "deep.x", "scalar.child",
	})
	want := map[string]any{
		"keep":   "v",
		"nullme": nil,
		"scalar": "not-an-object",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeDirtyFields() = %#v, want %#v", got, want)
	}
}

// base 为 nil 或空 map 时仍返回可用（非 nil）的 map，且脏字段能正确写入。
func TestMergeDirtyFieldsNilBase(t *testing.T) {
	changed := map[string]any{
		"a":      1,
		"nested": map[string]any{"b": 2},
	}

	got := mergeDirtyFields(nil, changed, []string{"nested.b"})
	want := map[string]any{"nested": map[string]any{"b": 2}}
	if got == nil {
		t.Fatal("mergeDirtyFields() 返回 nil，期望非 nil 的 map")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeDirtyFields() = %#v, want %#v", got, want)
	}
}
