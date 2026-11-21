package config

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestFingerprintConfigKnownDigests(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"empty nil", nil, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"empty slice", []byte{}, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"abc", []byte("abc"), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
		{"hello", []byte("hello"), "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FingerprintConfig(tt.data)
			if got != tt.want {
				t.Fatalf("FingerprintConfig(%q) = %q, want %q", tt.data, got, tt.want)
			}
			if len(got) != 64 {
				t.Errorf("指纹长度 = %d, want 64", len(got))
			}
			if strings.ToLower(got) != got {
				t.Errorf("指纹应为小写十六进制: %q", got)
			}
		})
	}
}

func TestFingerprintConfigStable(t *testing.T) {
	inputs := [][]byte{
		nil,
		[]byte("abc"),
		[]byte(`{"model":"gpt-4","locale":"zh-CN"}`),
		[]byte("模型一"),
	}
	for _, data := range inputs {
		first := FingerprintConfig(data)
		for i := 0; i < 20; i++ {
			if again := FingerprintConfig(data); again != first {
				t.Fatalf("相同输入第 %d 次指纹漂移: %q -> %q", i+2, first, again)
			}
		}
	}
}

func TestFingerprintConfigDifferentInputs(t *testing.T) {
	tests := []struct {
		name  string
		left  []byte
		right []byte
	}{
		{"空与非空", nil, []byte("x")},
		{"一字之差", []byte("abc"), []byte("abd")},
		{"字节编码不同", []byte("模型一"), []byte("模型二")},
		{"合法与非法 JSON", []byte(`{"a":1}`), []byte(`{"a":1`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if FingerprintConfig(tt.left) == FingerprintConfig(tt.right) {
				t.Fatalf("不同输入得到相同指纹: %q / %q", tt.left, tt.right)
			}
		})
	}
}

func TestDiffConfigMaps(t *testing.T) {
	tests := []struct {
		name   string
		before map[string]any
		after  map[string]any
		want   ConfigDiff
	}{
		{
			name:   "完全一致",
			before: map[string]any{"model": "gpt", "temp": 0.5},
			after:  map[string]any{"model": "gpt", "temp": 0.5},
			want:   ConfigDiff{},
		},
		{
			name:   "两个 nil 映射",
			before: nil,
			after:  nil,
			want:   ConfigDiff{},
		},
		{
			name:   "仅新增",
			before: map[string]any{"model": "gpt"},
			after:  map[string]any{"model": "gpt", "locale": "zh-CN"},
			want:   ConfigDiff{Changed: true, AddedKeys: []string{"locale"}},
		},
		{
			name:   "仅删除",
			before: map[string]any{"model": "gpt", "locale": "zh-CN"},
			after:  map[string]any{"model": "gpt"},
			want:   ConfigDiff{Changed: true, RemovedKeys: []string{"locale"}},
		},
		{
			name:   "仅修改",
			before: map[string]any{"model": "gpt"},
			after:  map[string]any{"model": "claude"},
			want:   ConfigDiff{Changed: true, ModifiedKeys: []string{"model"}},
		},
		{
			name:   "三种差异同时出现并稳定排序",
			before: map[string]any{"zeta": 1, "model": "a", "old": true, "keep": "same"},
			after:  map[string]any{"zeta": 2, "model": "b", "fresh": true, "keep": "same"},
			want: ConfigDiff{
				Changed:      true,
				AddedKeys:    []string{"fresh"},
				RemovedKeys:  []string{"old"},
				ModifiedKeys: []string{"model", "zeta"},
			},
		},
		{
			name: "嵌套路径",
			before: map[string]any{
				"model": "a",
				"providers": map[string]any{
					"openai":    map[string]any{"model": "x"},
					"anthropic": map[string]any{"model": "y"},
				},
			},
			after: map[string]any{
				"model": "b",
				"providers": map[string]any{
					"openai": map[string]any{"model": "z"},
					"new":    map[string]any{"key": "k"},
				},
			},
			want: ConfigDiff{
				Changed:      true,
				AddedKeys:    []string{"providers.new.key"},
				RemovedKeys:  []string{"providers.anthropic.model"},
				ModifiedKeys: []string{"model", "providers.openai.model"},
			},
		},
		{
			name: "CJK 键与值",
			before: map[string]any{
				"模型": "一",
				"嵌套": map[string]any{"键": "旧"},
			},
			after: map[string]any{
				"模型": "二",
				"嵌套": map[string]any{"键": "新", "新键": "值"},
			},
			want: ConfigDiff{
				Changed:      true,
				AddedKeys:    []string{"嵌套.新键"},
				ModifiedKeys: []string{"模型", "嵌套.键"},
			},
		},
		{
			name:   "fmt.Sprint 等值不视为修改",
			before: map[string]any{"n": 1},
			after:  map[string]any{"n": "1"},
			want:   ConfigDiff{},
		},
		{
			name:   "非映射容器作为叶子",
			before: map[string]any{"list": []any{"a", "b"}},
			after:  map[string]any{"list": []any{"a", "c"}},
			want:   ConfigDiff{Changed: true, ModifiedKeys: []string{"list"}},
		},
		{
			name:   "nil 值视为叶子",
			before: map[string]any{"unset": nil},
			after:  map[string]any{"unset": "value"},
			want:   ConfigDiff{Changed: true, ModifiedKeys: []string{"unset"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DiffConfigMaps(tt.before, tt.after)
			if got.Changed != tt.want.Changed {
				t.Errorf("Changed = %v, want %v", got.Changed, tt.want.Changed)
			}
			assertKeySet(t, "AddedKeys", got.AddedKeys, tt.want.AddedKeys)
			assertKeySet(t, "RemovedKeys", got.RemovedKeys, tt.want.RemovedKeys)
			assertKeySet(t, "ModifiedKeys", got.ModifiedKeys, tt.want.ModifiedKeys)
		})
	}
}

func TestDiffConfigMapsStableOrdering(t *testing.T) {
	before := map[string]any{
		"zeta": 1, "alpha": 1, "模型": "一", "嵌套": map[string]any{"键": "旧"}, "beta": 1,
	}
	after := map[string]any{
		"zeta": 2, "alpha": 2, "模型": "二", "嵌套": map[string]any{"键": "新"}, "gamma": 1,
	}

	first := DiffConfigMaps(before, after)
	for i := 0; i < 20; i++ {
		got := DiffConfigMaps(before, after)
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("第 %d 次差异漂移: 首次 %+v, 本次 %+v", i+2, first, got)
		}
	}
	for _, list := range [][]string{first.AddedKeys, first.RemovedKeys, first.ModifiedKeys} {
		if !sort.StringsAreSorted(list) {
			t.Errorf("差异列表未稳定升序排序: %v", list)
		}
	}
}

func TestDiffConfigMapsDoesNotMutateInputs(t *testing.T) {
	before := map[string]any{"a": map[string]any{"b": "1"}, "x": 1}
	after := map[string]any{"a": map[string]any{"b": "2"}, "y": 2}
	wantBefore := map[string]any{"a": map[string]any{"b": "1"}, "x": 1}
	wantAfter := map[string]any{"a": map[string]any{"b": "2"}, "y": 2}

	_ = DiffConfigMaps(before, after)

	if !reflect.DeepEqual(before, wantBefore) {
		t.Errorf("before 被修改: %#v, want %#v", before, wantBefore)
	}
	if !reflect.DeepEqual(after, wantAfter) {
		t.Errorf("after 被修改: %#v, want %#v", after, wantAfter)
	}
}

// assertKeySet 校验差异列表既已升序排序，又与期望 key 集合一致（忽略 nil 与空切片的区别）。
func assertKeySet(t *testing.T, label string, got, want []string) {
	t.Helper()
	if !sort.StringsAreSorted(got) {
		t.Errorf("%s = %v, 期望稳定升序", label, got)
	}
	orderedGot := append([]string(nil), got...)
	orderedWant := append([]string(nil), want...)
	sort.Strings(orderedGot)
	sort.Strings(orderedWant)
	if !reflect.DeepEqual(orderedGot, orderedWant) {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}
