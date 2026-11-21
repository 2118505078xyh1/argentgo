package config

import (
	"reflect"
	"sort"
	"testing"
)

// 顶层已知键与未知键：未知键应被报告，已知键不报告。
func TestFindUnknownKeysTopLevel(t *testing.T) {
	tree := map[string]any{
		"model":   "kept-model",
		"baseURL": "https://example.test/v1",
		"modle":   "typo",
	}
	known := []string{"model", "baseURL", "protocol", "locale"}
	want := []string{"modle"}
	if got := findUnknownKeys(tree, known); !reflect.DeepEqual(got, want) {
		t.Fatalf("findUnknownKeys = %v, want %v", got, want)
	}
}

// 别名拼写即使不在 known 中也不得被当作未知键。
func TestFindUnknownKeysExcludesKnownAliases(t *testing.T) {
	tree := map[string]any{
		"modelName":         "alias-of-model",
		"base_url":          "alias-of-baseURL",
		"verify_timeout_ms": 1000,
		"stop_token_ids":    []any{1, 2},
	}
	known := []string{
		"model", "baseURL", "verifyTimeoutMS", "stopTokenIds",
	}
	if got := findUnknownKeys(tree, known); len(got) != 0 {
		t.Fatalf("aliases were reported as unknown: %v", got)
	}
}

// 规范的嵌套别名路径也不得被当作未知键。
func TestFindUnknownKeysExcludesNestedAliases(t *testing.T) {
	tree := map[string]any{
		"context_window_info": map[string]any{
			"rope_scaling": map[string]any{
				"original_max": 4096,
			},
		},
	}
	known := []string{
		"contextWindowInfo",
		"contextWindowInfo.ropeScaling",
		"contextWindowInfo.ropeScaling.originalMax",
	}
	if got := findUnknownKeys(tree, known); len(got) != 0 {
		t.Fatalf("nested aliases were reported as unknown: %v", got)
	}
}

// 已知容器键内部的拼写错误应以点分路径报告。
func TestFindUnknownKeysNestedPath(t *testing.T) {
	tree := map[string]any{
		"contextWindowInfo": map[string]any{
			"ropeScaling": map[string]any{
				"factor":       4,
				"origionalMax": 4096, // 拼写错误
			},
		},
	}
	known := []string{
		"contextWindowInfo",
		"contextWindowInfo.ropeScaling",
		"contextWindowInfo.ropeScaling.factor",
		"contextWindowInfo.ropeScaling.originalMax",
	}
	want := []string{"contextWindowInfo.ropeScaling.origionalMax"}
	if got := findUnknownKeys(tree, known); !reflect.DeepEqual(got, want) {
		t.Fatalf("findUnknownKeys = %v, want %v", got, want)
	}
}

// 未知键的子树不再深入，只报告最外层的未知路径。
func TestFindUnknownKeysStopsAtUnknownSubtree(t *testing.T) {
	tree := map[string]any{
		"futureThing": map[string]any{
			"nested": map[string]any{"deep": true},
		},
	}
	known := []string{"model"}
	want := []string{"futureThing"}
	if got := findUnknownKeys(tree, known); !reflect.DeepEqual(got, want) {
		t.Fatalf("findUnknownKeys = %v, want %v", got, want)
	}
}

// 结果必须稳定排序并去重。
func TestFindUnknownKeysSortedAndDeduplicated(t *testing.T) {
	tree := map[string]any{
		"zeta":  1,
		"alpha": 2,
		"mid":   3,
	}
	// known 中含重复项，不应让输出出现重复。
	known := []string{"model", "model"}
	want := []string{"alpha", "mid", "zeta"}
	for i := 0; i < 5; i++ {
		got := findUnknownKeys(tree, known)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("findUnknownKeys = %v, want %v", got, want)
		}
		if !sort.StringsAreSorted(got) {
			t.Fatalf("结果未按字典序排序: %v", got)
		}
	}
}

// 含中文等 CJK 字符的键按原样处理，不依赖 ASCII 正则。
func TestFindUnknownKeysCJK(t *testing.T) {
	tree := map[string]any{
		"模型":    "gpt",
		"接口地址":  "https://example.test/v1",
		"最大轮数✗": 4, // 拼写/输入错误的 CJK 键
		"嵌套": map[string]any{
			"正常": 1,
			"错误": 2,
		},
	}
	known := []string{
		"模型", "接口地址",
		"嵌套", "嵌套.正常",
	}
	want := []string{"嵌套.错误", "最大轮数✗"}
	if got := findUnknownKeys(tree, known); !reflect.DeepEqual(got, want) {
		t.Fatalf("findUnknownKeys = %v, want %v", got, want)
	}
}

// 空树、nil 树与已知清单为空时的行为。
func TestFindUnknownKeysEmptyAndNil(t *testing.T) {
	if got := findUnknownKeys(nil, nil); len(got) != 0 {
		t.Fatalf("nil tree: got %v", got)
	}
	if got := findUnknownKeys(map[string]any{}, []string{"model"}); len(got) != 0 {
		t.Fatalf("empty tree: got %v", got)
	}
	if got := findUnknownKeys(map[string]any{"model": "m"}, nil); !reflect.DeepEqual(got, []string{"model"}) {
		t.Fatalf("empty known: got %v", got)
	}
}

// 纯函数不得修改传入的树或已知清单。
func TestFindUnknownKeysDoesNotMutateInputs(t *testing.T) {
	tree := map[string]any{
		"model": map[string]any{"typo": 1},
	}
	known := []string{"model"}
	treeBefore := map[string]any{"model": map[string]any{"typo": 1}}
	knownBefore := []string{"model"}

	_ = findUnknownKeys(tree, known)

	if !reflect.DeepEqual(tree, treeBefore) {
		t.Fatalf("tree 被修改: %#v", tree)
	}
	if !reflect.DeepEqual(known, knownBefore) {
		t.Fatalf("known 被修改: %#v", known)
	}
}
