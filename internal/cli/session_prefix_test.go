package cli

import (
	"reflect"
	"testing"
	"unicode/utf8"
)

// TestResolveSessionPrefixExactWins 检查精确命中优先：字节精确命中、大小写不敏感
// 的精确命中，都不得因为自身同时是其它会话 ID 的前缀而误报歧义。
func TestResolveSessionPrefixExactWins(t *testing.T) {
	cases := []struct {
		name  string
		input string
		ids   []string
		want  string
	}{
		{
			"byte exact beats longer prefix",
			"a1b2",
			[]string{"a1b2c3", "a1b2", "b3c4"},
			"a1b2",
		},
		{
			"full id exact despite shared prefix",
			"20261120-120000-a1b2",
			[]string{"20261120-120000-a1b2c3", "20261120-120000-a1b2"},
			"20261120-120000-a1b2",
		},
		{
			"case insensitive exact",
			"A1B2",
			[]string{"a1b2c3", "a1b2"},
			"a1b2",
		},
		{
			"mixed case full id exact",
			"20261120-120000-A1B2",
			[]string{"20261120-120000-a1b2c3", "20261120-120000-a1b2"},
			"20261120-120000-a1b2",
		},
		{
			"single id exact",
			"a1b2",
			[]string{"a1b2"},
			"a1b2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, candidates := resolveSessionPrefix(tc.input, tc.ids)
			if got != tc.want {
				t.Fatalf("resolveSessionPrefix(%q, %v) match = %q, want %q", tc.input, tc.ids, got, tc.want)
			}
			if len(candidates) != 0 {
				t.Fatalf("exact hit must not return candidates, got %v", candidates)
			}
		})
	}
}

// TestResolveSessionPrefixUniquePrefix 检查前缀唯一命中时返回该会话 ID，且不携带候选。
func TestResolveSessionPrefixUniquePrefix(t *testing.T) {
	ids := []string{
		"20261120-120000-a1b2",
		"20261120-120000-b3c4",
		"20261121-090000-f7a8",
	}
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"date prefix", "20261121", "20261121-090000-f7a8"},
		{"time prefix", "20261121-09", "20261121-090000-f7a8"},
		{"long prefix", "20261121-090000-f7", "20261121-090000-f7a8"},
		{"case insensitive prefix", "20261121-090000-F7", "20261121-090000-f7a8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, candidates := resolveSessionPrefix(tc.input, ids)
			if got != tc.want {
				t.Fatalf("resolveSessionPrefix(%q) match = %q, want %q", tc.input, got, tc.want)
			}
			if len(candidates) != 0 {
				t.Fatalf("unique prefix must not return candidates, got %v", candidates)
			}
		})
	}
}

// TestResolveSessionPrefixAmbiguous 检查多命中时返回空 match，并给出排序、去重后的
// 候选列表；候选是裸 ID（创建日期占位由调用方在展示时补充）。
func TestResolveSessionPrefixAmbiguous(t *testing.T) {
	cases := []struct {
		name  string
		input string
		ids   []string
		want  []string
	}{
		{
			"same day unsorted gets sorted",
			"20261120",
			[]string{
				"20261120-130000-d5e6",
				"20261120-120000-a1b2",
				"20261121-090000-f7a8",
				"20261120-120000-b3c4",
			},
			[]string{
				"20261120-120000-a1b2",
				"20261120-120000-b3c4",
				"20261120-130000-d5e6",
			},
		},
		{
			"case insensitive grouping",
			"c",
			[]string{"config", "Config", "commands", "doctor"},
			[]string{"Config", "commands", "config"},
		},
		{
			"duplicates removed among ambiguous hits",
			"ab",
			[]string{"abce", "abcd", "abce"},
			[]string{"abcd", "abce"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, candidates := resolveSessionPrefix(tc.input, tc.ids)
			if got != "" {
				t.Fatalf("ambiguous prefix must not return a match, got %q", got)
			}
			if !reflect.DeepEqual(candidates, tc.want) {
				t.Fatalf("resolveSessionPrefix(%q, %v) candidates = %v, want %v", tc.input, tc.ids, candidates, tc.want)
			}
			// 候选必须是 ids 中的裸 ID，不能夹带调用方负责补充的创建日期占位。
			for _, candidate := range candidates {
				if !sessionPrefixContainsID(tc.ids, candidate) {
					t.Fatalf("candidate %q is not a raw id from %v", candidate, tc.ids)
				}
			}
		})
	}
}

// TestResolveSessionPrefixNoMatch 检查没有任何会话 ID 以输入开头时返回空结果。
func TestResolveSessionPrefixNoMatch(t *testing.T) {
	ids := []string{"a1b2", "c3d4", "20261120-120000-a1b2"}
	for _, input := range []string{"zzz", "b2", "A1B2X", "   "} {
		t.Run(input, func(t *testing.T) {
			got, candidates := resolveSessionPrefix(input, ids)
			if got != "" || len(candidates) != 0 {
				t.Fatalf("resolveSessionPrefix(%q) = (%q, %v), want empty", input, got, candidates)
			}
		})
	}
}

// TestResolveSessionPrefixEmptyInput 检查空输入与空 ID 集都返回空结果，而不是把空串
// 当作「匹配一切」的前缀。
func TestResolveSessionPrefixEmptyInput(t *testing.T) {
	cases := []struct {
		name  string
		input string
		ids   []string
	}{
		{"empty input", "", []string{"a1b2", "c3d4"}},
		{"empty input single id", "", []string{"a1b2"}},
		{"nil ids", "a1", nil},
		{"empty ids", "a1", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, candidates := resolveSessionPrefix(tc.input, tc.ids)
			if got != "" || len(candidates) != 0 {
				t.Fatalf("resolveSessionPrefix(%q, %v) = (%q, %v), want empty", tc.input, tc.ids, got, candidates)
			}
		})
	}
}

// TestResolveSessionPrefixCJKAndCase 检查中文会话 ID 的前缀解析：按 rune 折叠大小写，
// 不切断多字节字符，返回的原样 ID 仍是合法 UTF-8。
func TestResolveSessionPrefixCJKAndCase(t *testing.T) {
	ids := []string{"会话20261120-a1", "会话20261120-b2", "设置-c3"}

	// 唯一中文前缀。
	if got, candidates := resolveSessionPrefix("设置-c3", ids); got != "设置-c3" || len(candidates) != 0 {
		t.Fatalf("CJK exact = (%q, %v), want 设置-c3 with no candidates", got, candidates)
	}

	// 多个中文前缀：返回排序候选且不切断 UTF-8。
	got, candidates := resolveSessionPrefix("会话2", ids)
	if got != "" {
		t.Fatalf("ambiguous CJK match = %q, want empty", got)
	}
	want := []string{"会话20261120-a1", "会话20261120-b2"}
	if !reflect.DeepEqual(candidates, want) {
		t.Fatalf("ambiguous CJK candidates = %v, want %v", candidates, want)
	}
	for _, candidate := range candidates {
		if !utf8.ValidString(candidate) {
			t.Fatalf("candidate %q is not valid UTF-8", candidate)
		}
	}

	// 中英混排 ID 的大小写不敏感前缀。
	mixed := []string{"Audit会话", "audit日志"}
	if got, candidates := resolveSessionPrefix("AUDIT会", mixed); got != "Audit会话" || len(candidates) != 0 {
		t.Fatalf("mixed CJK prefix = (%q, %v), want Audit会话", got, candidates)
	}
}

// TestResolveSessionPrefixDeterministic 检查纯函数性质：ID 顺序被打乱后候选仍按字典序
// 一致，且函数不会改动传入的切片。
func TestResolveSessionPrefixDeterministic(t *testing.T) {
	ids := []string{"20261120-130000-d5e6", "20261120-120000-b3c4", "20261120-120000-a1b2"}
	original := append([]string(nil), ids...)

	first, firstCandidates := resolveSessionPrefix("20261120", ids)
	if !reflect.DeepEqual(ids, original) {
		t.Fatalf("input slice was mutated: %v, want %v", ids, original)
	}

	reversed := []string{"20261120-120000-a1b2", "20261120-120000-b3c4", "20261120-130000-d5e6"}
	second, secondCandidates := resolveSessionPrefix("20261120", reversed)
	if first != second || !reflect.DeepEqual(firstCandidates, secondCandidates) {
		t.Fatalf("result depends on input order: (%q, %v) vs (%q, %v)",
			first, firstCandidates, second, secondCandidates)
	}
}

// sessionPrefixContainsID 报告列表里是否存在目标 ID，仅用于测试中校验候选来自原始 ID。
func sessionPrefixContainsID(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}
