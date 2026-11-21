package tools

import (
	"reflect"
	"strings"
	"testing"
)

// TestSearchPresetsFields 核对三个预设的字段与稳定顺序。
func TestSearchPresetsFields(t *testing.T) {
	if len(SearchPresets) != 3 {
		t.Fatalf("len(SearchPresets) = %d, want 3", len(SearchPresets))
	}
	want := []SearchPreset{
		{
			Name:           "brave",
			URL:            "https://api.search.brave.com/res/v1/web/search",
			HeaderName:     "X-Subscription-Token",
			HeaderValueEnv: "BRAVE_API_KEY",
		},
		{
			Name:           "tavily",
			URL:            "https://api.tavily.com/search",
			HeaderValueEnv: "TAVILY_API_KEY",
		},
		{
			Name: "searxng",
		},
	}
	for i, w := range want {
		got := SearchPresets[i]
		if got.Name != w.Name || got.URL != w.URL || got.HeaderName != w.HeaderName || got.HeaderValueEnv != w.HeaderValueEnv {
			t.Errorf("SearchPresets[%d] = %+v, want name=%q url=%q header=%q env=%q",
				i, got, w.Name, w.URL, w.HeaderName, w.HeaderValueEnv)
		}
		if got.Note == "" {
			t.Errorf("SearchPresets[%d].Note is empty", i)
		}
	}
	if SearchPresets[0].Name+"|"+SearchPresets[1].Name+"|"+SearchPresets[2].Name != "brave|tavily|searxng" {
		t.Fatalf("unexpected preset order: %+v", SearchPresets)
	}
}

// TestResolveSearchPresetAliasesAndCase 覆盖大小写不敏感、空白与别名。
func TestResolveSearchPresetAliasesAndCase(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"brave", "brave"},
		{"BRAVE", "brave"},
		{" Brave ", "brave"},
		{"brave_search", "brave"},
		{"BraveSearch", "brave"},
		{"tavily", "tavily"},
		{"TAVILY", "tavily"},
		{"tavily_search", "tavily"},
		{"searxng", "searxng"},
		{"SEARXNG", "searxng"},
		{"searx", "searxng"},
		{" SearX ", "searxng"},
	}
	for _, tc := range cases {
		preset, ok := ResolveSearchPreset(tc.input)
		if !ok {
			t.Errorf("ResolveSearchPreset(%q) not found", tc.input)
			continue
		}
		if preset.Name != tc.want {
			t.Errorf("ResolveSearchPreset(%q).Name = %q, want %q", tc.input, preset.Name, tc.want)
		}
	}
}

// TestResolveSearchPresetUnknownFallsBack 验证未知名称回落为零值与 false。
func TestResolveSearchPresetUnknownFallsBack(t *testing.T) {
	for _, name := range []string{"", "   ", "bing", "google", "not-a-preset"} {
		preset, ok := ResolveSearchPreset(name)
		if ok {
			t.Errorf("ResolveSearchPreset(%q) unexpectedly ok: %+v", name, preset)
		}
		if !reflect.DeepEqual(preset, SearchPreset{}) {
			t.Errorf("ResolveSearchPreset(%q) = %+v, want zero value", name, preset)
		}
	}
}

// TestSearchPresetNoteStable 验证提示稳定且能引导未配置的用户。
func TestSearchPresetNoteStable(t *testing.T) {
	for _, preset := range SearchPresets {
		first := SearchPresetNote(preset.Name)
		second := SearchPresetNote(preset.Name)
		if first != second {
			t.Errorf("SearchPresetNote(%q) not stable: %q != %q", preset.Name, first, second)
		}
		if first != preset.Note {
			t.Errorf("SearchPresetNote(%q) = %q, want preset.Note %q", preset.Name, first, preset.Note)
		}
	}

	unknown := SearchPresetNote("bing")
	if unknown != SearchPresetNote("bing") {
		t.Fatal("SearchPresetNote for unknown name not stable")
	}
	for _, want := range []string{"ARGENT_WEB_SEARCH_URL", "BRAVE_API_KEY", "TAVILY_API_KEY", "brave", "tavily", "searxng"} {
		if !strings.Contains(unknown, want) {
			t.Errorf("unknown note %q missing %q", unknown, want)
		}
	}
}

// TestSearchPresetFunctionsDoNotMutateGlobals 确保解析函数只读全局，
// 且返回的预设是值拷贝，调用方修改它不会影响 SearchPresets。
func TestSearchPresetFunctionsDoNotMutateGlobals(t *testing.T) {
	before := append([]SearchPreset(nil), SearchPresets...)

	preset, ok := ResolveSearchPreset("brave")
	if !ok {
		t.Fatal("ResolveSearchPreset(brave) not found")
	}
	preset.Name = "mutated"
	preset.URL = "https://mutated.invalid"
	_ = SearchPresetNote("tavily")
	_ = SearchPresetNote("unknown")

	if !reflect.DeepEqual(SearchPresets, before) {
		t.Fatalf("SearchPresets mutated: got %+v, want %+v", SearchPresets, before)
	}
}

// TestSearchPresetDeterministic 验证重复调用结果一致、预设顺序不变。
func TestSearchPresetDeterministic(t *testing.T) {
	before := append([]SearchPreset(nil), SearchPresets...)
	for i := 0; i < 50; i++ {
		for _, name := range []string{"brave", "TAVILY", "searx", "unknown"} {
			p1, ok1 := ResolveSearchPreset(name)
			p2, ok2 := ResolveSearchPreset(name)
			if ok1 != ok2 || !reflect.DeepEqual(p1, p2) {
				t.Fatalf("ResolveSearchPreset(%q) non-deterministic: %+v/%v vs %+v/%v", name, p1, ok1, p2, ok2)
			}
		}
	}
	if !reflect.DeepEqual(SearchPresets, before) {
		t.Fatal("SearchPresets order/content changed across calls")
	}
}
