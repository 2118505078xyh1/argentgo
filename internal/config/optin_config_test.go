package config

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

// optInExpectedOrder 是白名单按 Key 升序排列后的期望顺序。
var optInExpectedOrder = []string{
	"apply_patch",
	"attempt_isolation",
	"cli_hints",
	"context_pressure",
	"grep_ex",
	"keep_implicit_context",
	"preserve_summary_sections",
}

// optInExpectedDefs 是键 -> (标签, 归类) 的期望表，与实现中的白名单表对应。
var optInExpectedDefs = map[string][2]string{
	"cli_hints":                 {"CLI 提示", "交互"},
	"apply_patch":               {"补丁应用", "交互"},
	"grep_ex":                   {"增强搜索", "交互"},
	"context_pressure":          {"上下文压力", "上下文"},
	"attempt_isolation":         {"失败尝试隔离", "上下文"},
	"keep_implicit_context":     {"保留隐式上下文", "上下文"},
	"preserve_summary_sections": {"保留摘要章节", "上下文"},
}

// TestParseOptInDisplayAllKeys 覆盖全部白名单键：显式 true 时应全部 Enabled，
// 输出恰好七个条目且按 Key 升序，标签与归类与期望一致。
func TestParseOptInDisplayAllKeys(t *testing.T) {
	raw := map[string]any{
		"optins": map[string]any{
			"cli_hints":                 true,
			"apply_patch":               true,
			"grep_ex":                   true,
			"context_pressure":          true,
			"attempt_isolation":         true,
			"keep_implicit_context":     true,
			"preserve_summary_sections": true,
		},
	}
	items := ParseOptInDisplay(raw)
	if len(items) != len(optInExpectedOrder) {
		t.Fatalf("条目数 = %d, want %d: %+v", len(items), len(optInExpectedOrder), items)
	}
	for i, wantKey := range optInExpectedOrder {
		got := items[i]
		if got.Key != wantKey {
			t.Errorf("第 %d 项 Key = %q, want %q", i, got.Key, wantKey)
		}
		if !got.Enabled {
			t.Errorf("键 %q 应 Enabled=true", wantKey)
		}
		def := optInExpectedDefs[wantKey]
		if got.Label != def[0] || got.Category != def[1] {
			t.Errorf("键 %q 元数据 = (%q, %q), want (%q, %q)", wantKey, got.Label, got.Category, def[0], def[1])
		}
	}
}

// TestParseOptInDisplayDefaultClosed 覆盖缺键场景：所有白名单键都应出现，
// 但默认全部关闭。
func TestParseOptInDisplayDefaultClosed(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]any
	}{
		{name: "nil raw", raw: nil},
		{name: "empty raw", raw: map[string]any{}},
		{name: "empty block", raw: map[string]any{"optins": map[string]any{}}},
		{name: "partial block", raw: map[string]any{"optins": map[string]any{"cli_hints": true}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items := ParseOptInDisplay(tc.raw)
			if len(items) != len(optInExpectedOrder) {
				t.Fatalf("条目数 = %d, want %d", len(items), len(optInExpectedOrder))
			}
			for _, item := range items {
				if item.Key != "cli_hints" && item.Enabled {
					t.Errorf("键 %q 在缺省场景中不应开启", item.Key)
				}
			}
			// partial 场景下 cli_hints 应开启，其余关闭；逐一核对。
			for _, item := range items {
				wantEnabled := tc.name == "partial block" && item.Key == "cli_hints"
				if item.Enabled != wantEnabled {
					t.Errorf("键 %q Enabled = %v, want %v", item.Key, item.Enabled, wantEnabled)
				}
			}
		})
	}
}

// TestParseOptInDisplayCJKLabel 验证标签与归类是完整 UTF-8 且包含 CJK 字符。
func TestParseOptInDisplayCJKLabel(t *testing.T) {
	items := ParseOptInDisplay(nil)
	for _, item := range items {
		if !utf8.ValidString(item.Label) || !utf8.ValidString(item.Category) {
			t.Errorf("键 %q 的标签/归类不是完整 UTF-8: %q / %q", item.Key, item.Label, item.Category)
		}
		// 至少有一个汉字（CLI 提示 / 补丁应用等）。
		found := false
		for _, r := range item.Label {
			if r >= 0x4e00 && r <= 0x9fff {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("键 %q 的标签 %q 不含 CJK 字符", item.Key, item.Label)
		}
	}
}

// TestParseOptInDisplayStableSort 验证输出恒按 Key 升序，与 map 的迭代顺序无关：
// 多次解析同一份配置应得到逐字节一致的结果。
func TestParseOptInDisplayStableSort(t *testing.T) {
	raw := map[string]any{
		"optins": map[string]any{
			"preserve_summary_sections": true,
			"cli_hints":                 true,
			"apply_patch":               false,
			"grep_ex":                   true,
		},
	}
	first := ParseOptInDisplay(raw)
	keys := make([]string, 0, len(first))
	for _, item := range first {
		keys = append(keys, item.Key)
	}
	if !sort.StringsAreSorted(keys) {
		t.Fatalf("输出未按 Key 升序: %v", keys)
	}
	for i := 0; i < 20; i++ {
		got := ParseOptInDisplay(raw)
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("第 %d 次解析结果不一致: %+v vs %+v", i, got, first)
		}
	}
}

// TestParseOptInDisplayUnknownKeysIgnored 验证未知键（含 CJK 键）被忽略，
// 输出只包含白名单键。
func TestParseOptInDisplayUnknownKeysIgnored(t *testing.T) {
	raw := map[string]any{
		"optins": map[string]any{
			"cli_hints":   true,
			"bogus":       true,
			"模型":          true,
			"CLI_HINTS":   true,
			"cli-hints":   true,
			"unknown_key": false,
		},
	}
	items := ParseOptInDisplay(raw)
	if len(items) != len(optInExpectedOrder) {
		t.Fatalf("未知键未被忽略，条目数 = %d: %+v", len(items), items)
	}
	for _, item := range items {
		if _, known := optInExpectedDefs[item.Key]; !known {
			t.Errorf("输出含未知键 %q", item.Key)
		}
	}
}

// TestParseOptInDisplayDoesNotMutateInput 验证纯函数约定：解析不修改入参
// （含嵌套块），且不修改上一次的返回切片。
func TestParseOptInDisplayDoesNotMutateInput(t *testing.T) {
	block := map[string]any{
		"cli_hints":   true,
		"apply_patch": false,
		"unknown":     "keep",
	}
	raw := map[string]any{"optins": block}
	before := map[string]any{
		"optins": map[string]any{
			"cli_hints":   true,
			"apply_patch": false,
			"unknown":     "keep",
		},
	}
	_ = ParseOptInDisplay(raw)
	if !reflect.DeepEqual(raw, before) {
		t.Fatalf("ParseOptInDisplay 修改了入参: got %+v, want %+v", raw, before)
	}

	// 修改返回切片不应影响后续调用。
	first := ParseOptInDisplay(raw)
	first[0].Enabled = false
	first[0].Label = "已篡改"
	second := ParseOptInDisplay(raw)
	if !reflect.DeepEqual(second, ParseOptInDisplay(raw)) || second[0].Label == "已篡改" {
		t.Fatalf("修改返回切片影响了后续调用: %+v", second)
	}
}

// TestParseOptInDisplayNonBoolValues 验证 fail-closed：非布尔值一律视为关闭。
func TestParseOptInDisplayNonBoolValues(t *testing.T) {
	raw := map[string]any{
		"optins": map[string]any{
			"cli_hints":         "true",
			"apply_patch":       1,
			"grep_ex":           nil,
			"context_pressure":  map[string]any{"enabled": true},
			"attempt_isolation": 1.0,
		},
	}
	items := ParseOptInDisplay(raw)
	for _, item := range items {
		if item.Enabled {
			t.Errorf("键 %q 的非布尔值被误判为开启", item.Key)
		}
	}
}

// TestParseOptInDisplayDirectForm 验证直接形式：raw 本身即 opt-in 映射时也能
// 正确读取，白名单之外的键被忽略。
func TestParseOptInDisplayDirectForm(t *testing.T) {
	raw := map[string]any{
		"cli_hints": true,
		"model":     "gpt",
		"grep_ex":   true,
	}
	items := ParseOptInDisplay(raw)
	byKey := make(map[string]OptInDisplay, len(items))
	for _, item := range items {
		byKey[item.Key] = item
	}
	if !byKey["cli_hints"].Enabled || !byKey["grep_ex"].Enabled {
		t.Fatalf("直接形式未正确读取: %+v", byKey)
	}
	if byKey["apply_patch"].Enabled {
		t.Errorf("缺键应默认关闭: %+v", byKey["apply_patch"])
	}
}

// TestParseOptInDisplayMalformedBlock 验证 optins 键类型不符时 fail-closed，
// 全部开关关闭。
func TestParseOptInDisplayMalformedBlock(t *testing.T) {
	for _, raw := range []map[string]any{
		{"optins": true},
		{"optins": "cli_hints"},
		{"optins": 42},
	} {
		items := ParseOptInDisplay(raw)
		for _, item := range items {
			if item.Enabled {
				t.Errorf("optins 类型不符时键 %q 不应开启: %+v", item.Key, raw)
			}
		}
	}
}

// TestOptInDisplaySummary 覆盖摘要：启用项按 Key 排序展示，未启用项不出现，
// 空场景返回固定短句。
func TestOptInDisplaySummary(t *testing.T) {
	cases := []struct {
		name    string
		raw     map[string]any
		wantAll []string // 摘要中应出现的子串（按顺序）
		wantNo  []string // 摘要中不应出现的子串
	}{
		{
			name:   "none",
			raw:    nil,
			wantNo: []string{"已启用"},
		},
		{
			name:    "one enabled",
			raw:     map[string]any{"optins": map[string]any{"cli_hints": true}},
			wantAll: []string{"已启用的 opt-in", "CLI 提示", "cli_hints"},
			wantNo:  []string{"补丁应用", "增强搜索"},
		},
		{
			name: "several enabled sorted",
			raw: map[string]any{"optins": map[string]any{
				"grep_ex":           true,
				"cli_hints":         true,
				"apply_patch":       false,
				"context_pressure":  true,
				"attempt_isolation": false,
			}},
			wantAll: []string{"CLI 提示", "上下文压力", "增强搜索"},
			wantNo:  []string{"补丁应用", "失败尝试隔离", "保留隐式上下文"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			summary := OptInDisplaySummary(ParseOptInDisplay(tc.raw))
			if !utf8.ValidString(summary) {
				t.Errorf("摘要不是完整 UTF-8: %q", summary)
			}
			pos := 0
			for _, want := range tc.wantAll {
				idx := strings.Index(summary[pos:], want)
				if idx < 0 {
					t.Errorf("摘要 %q 未包含（按顺序）%q", summary, want)
					continue
				}
				pos += idx + len(want)
			}
			for _, no := range tc.wantNo {
				if strings.Contains(summary, no) {
					t.Errorf("摘要 %q 不应包含 %q", summary, no)
				}
			}
		})
	}
}

// TestOptInDisplaySummaryStableAndPure 验证摘要与输入顺序无关，且不修改入参。
func TestOptInDisplaySummaryStableAndPure(t *testing.T) {
	items := ParseOptInDisplay(map[string]any{"optins": map[string]any{
		"cli_hints":        true,
		"grep_ex":          true,
		"context_pressure": true,
	}})
	want := OptInDisplaySummary(items)

	// 打乱顺序并加入重复项与一个禁用项，摘要应保持一致。
	// items 按 Key 升序：0..6 对应 apply_patch..preserve_summary_sections，
	// 其中 2/3/4 为 cli_hints/context_pressure/grep_ex（均已开启）。
	shuffled := []OptInDisplay{items[4], items[2], items[3], items[2], {Key: "apply_patch", Label: "补丁应用", Enabled: false}}
	before := append([]OptInDisplay(nil), shuffled...)
	if got := OptInDisplaySummary(shuffled); got != want {
		t.Errorf("顺序变化后摘要改变: %q vs %q", got, want)
	}
	if !reflect.DeepEqual(shuffled, before) {
		t.Errorf("OptInDisplaySummary 修改了入参: %+v vs %+v", shuffled, before)
	}
	if !strings.HasPrefix(want, "已启用的 opt-in：") || !strings.HasSuffix(want, "。") {
		t.Errorf("摘要格式异常: %q", want)
	}
}
