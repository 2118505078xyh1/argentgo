package cli

import (
	"reflect"
	"testing"
	"unicode/utf8"
)

// TestResolveCommandPrefixExactWins 检查精确命中优先：字节精确命中、大小写
// 不敏感的精确命中，都不得因为自身同时是其它命令的前缀而误报歧义。
func TestResolveCommandPrefixExactWins(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		commands []string
		want     string
	}{
		{"byte exact", "audit", []string{"sessions", "audit", "audit-log"}, "audit"},
		{"exact beats longer prefix", "audit", []string{"audit-log", "audit", "audit-filter"}, "audit"},
		{"case insensitive exact", "AUDIT", []string{"audit", "audit-log"}, "audit"},
		{"mixed case exact", "AuDiT", []string{"audit-log", "Audit"}, "Audit"},
		{"single command exact", "doctor", []string{"doctor"}, "doctor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, candidates := resolveCommandPrefix(tc.input, tc.commands)
			if got != tc.want {
				t.Fatalf("resolveCommandPrefix(%q, %v) match = %q, want %q", tc.input, tc.commands, got, tc.want)
			}
			if len(candidates) != 0 {
				t.Fatalf("exact hit must not return candidates, got %v", candidates)
			}
		})
	}
}

// TestResolveCommandPrefixUniquePrefix 检查前缀唯一命中时返回该命令，且不携带候选。
func TestResolveCommandPrefixUniquePrefix(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		commands []string
		want     string
	}{
		{"first letters", "sess", []string{"sessions", "stop", "skills"}, "sessions"},
		{"longer prefix", "sessio", []string{"sessions", "stop"}, "sessions"},
		{"case insensitive prefix", "SESS", []string{"sessions", "stop"}, "sessions"},
		{"prefix of one among many", "upd", []string{"update", "usage", "audit"}, "update"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, candidates := resolveCommandPrefix(tc.input, tc.commands)
			if got != tc.want {
				t.Fatalf("resolveCommandPrefix(%q, %v) match = %q, want %q", tc.input, tc.commands, got, tc.want)
			}
			if len(candidates) != 0 {
				t.Fatalf("unique prefix must not return candidates, got %v", candidates)
			}
		})
	}
}

// TestResolveCommandPrefixAmbiguous 检查多命中时返回空 match，并给出排序、去重
// 后的候选列表；候选保留 commands 中的原始写法。
func TestResolveCommandPrefixAmbiguous(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		commands []string
		want     []string
	}{
		{
			"three subcommands", "s", []string{"sessions", "stop", "skills"},
			[]string{"sessions", "skills", "stop"},
		},
		{
			"unsorted input gets sorted", "a", []string{"update", "audit-log", "auth", "audit-filter"},
			[]string{"audit-filter", "audit-log", "auth"},
		},
		{
			"case insensitive grouping", "C", []string{"config", "completion", "commands", "doctor"},
			[]string{"commands", "completion", "config"},
		},
		{
			"duplicates removed among ambiguous hits", "he", []string{"help", "help", "hello"},
			[]string{"hello", "help"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, candidates := resolveCommandPrefix(tc.input, tc.commands)
			if got != "" {
				t.Fatalf("ambiguous prefix must not return a match, got %q", got)
			}
			if !reflect.DeepEqual(candidates, tc.want) {
				t.Fatalf("resolveCommandPrefix(%q, %v) candidates = %v, want %v", tc.input, tc.commands, candidates, tc.want)
			}
		})
	}
}

// TestResolveCommandPrefixNoMatch 检查没有任何命令以输入开头时返回空结果。
func TestResolveCommandPrefixNoMatch(t *testing.T) {
	commands := []string{"sessions", "audit", "doctor"}
	for _, input := range []string{"zzz", "xyz", "sesx", "AUDITX"} {
		t.Run(input, func(t *testing.T) {
			got, candidates := resolveCommandPrefix(input, commands)
			if got != "" || len(candidates) != 0 {
				t.Fatalf("resolveCommandPrefix(%q) = (%q, %v), want empty", input, got, candidates)
			}
		})
	}
}

// TestResolveCommandPrefixCJK 检查中文命令的前缀解析：按 rune 折叠大小写，不切分
// 多字节字符，返回的原样命令仍是合法 UTF-8。
func TestResolveCommandPrefixCJK(t *testing.T) {
	commands := []string{"会话列表", "会话搜索", "设置"}

	// 唯一中文前缀。
	if got, candidates := resolveCommandPrefix("设置", commands); got != "设置" || len(candidates) != 0 {
		t.Fatalf("CJK exact = (%q, %v), want 设置 with no candidates", got, candidates)
	}

	// 多个中文前缀：返回排序候选且不切断 UTF-8。
	got, candidates := resolveCommandPrefix("会话", commands)
	if got != "" {
		t.Fatalf("ambiguous CJK match = %q, want empty", got)
	}
	want := []string{"会话列表", "会话搜索"}
	if !reflect.DeepEqual(candidates, want) {
		t.Fatalf("ambiguous CJK candidates = %v, want %v", candidates, want)
	}
	for _, c := range candidates {
		if !utf8.ValidString(c) {
			t.Fatalf("candidate %q is not valid UTF-8", c)
		}
	}

	// 中英混排命令的大小写不敏感前缀。
	mixed := []string{"Audit审计", "Audit日志"}
	if got, candidates := resolveCommandPrefix("audit审", mixed); got != "Audit审计" || len(candidates) != 0 {
		t.Fatalf("mixed CJK prefix = (%q, %v), want Audit审计", got, candidates)
	}
}

// TestResolveCommandPrefixEmptyInput 检查空输入与空命令集都返回空结果，而不是把
// 空串当作「匹配一切」的前缀。
func TestResolveCommandPrefixEmptyInput(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		commands []string
	}{
		{"empty input", "", []string{"audit", "doctor"}},
		{"empty input single command", "", []string{"audit"}},
		{"nil commands", "audit", nil},
		{"empty commands", "audit", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, candidates := resolveCommandPrefix(tc.input, tc.commands)
			if got != "" || len(candidates) != 0 {
				t.Fatalf("resolveCommandPrefix(%q, %v) = (%q, %v), want empty", tc.input, tc.commands, got, candidates)
			}
		})
	}
}

// TestResolveCommandPrefixDeterministic 检查纯函数性质：命令顺序被打乱后候选仍
// 按字典序一致，且函数不会改动传入的切片。
func TestResolveCommandPrefixDeterministic(t *testing.T) {
	commands := []string{"sessions", "stop", "skills"}
	original := append([]string(nil), commands...)

	first, firstCandidates := resolveCommandPrefix("s", commands)
	if !reflect.DeepEqual(commands, original) {
		t.Fatalf("input slice was mutated: %v, want %v", commands, original)
	}

	reversed := []string{"stop", "skills", "sessions"}
	second, secondCandidates := resolveCommandPrefix("s", reversed)
	if first != second || !reflect.DeepEqual(firstCandidates, secondCandidates) {
		t.Fatalf("result depends on input order: (%q, %v) vs (%q, %v)",
			first, firstCandidates, second, secondCandidates)
	}
}
