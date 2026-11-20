package memory

import (
	"strings"
	"testing"
)

// 空、nil 和全部跳过的输入返回 nil 结果，与加载器一致。
func TestDescribeIgnoreRulesEmpty(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input []string
	}{
		{name: "nil", input: nil},
		{name: "empty slice", input: []string{}},
		{name: "only blanks and comments", input: []string{"", "   ", "# a comment", "\t# indented"}},
		{name: "only bare bang", input: []string{"!", "!   "}},
		{name: "only BOM comment", input: []string{"\ufeff# BOM comment"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DescribeIgnoreRules(tc.input); got != nil {
				t.Fatalf("DescribeIgnoreRules(%q) = %#v, want nil", tc.input, got)
			}
		})
	}
}

// 三个必需字段 —— 原始模式、语义描述和
// 敏感目录风险标志 —— 加上 glob/取反规范化，都会针对
// 字面、glob、取反和 CJK 模式的混合进行检查。
func TestDescribeIgnoreRulesTable(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantPattern  string
		wantGlob     string
		wantNegated  bool
		wantRisk     bool
		wantDescBits []string
		wantRiskBits []string
	}{
		{
			name:         "literal safe path",
			input:        "build/",
			wantPattern:  "build/",
			wantGlob:     "build",
			wantDescBits: []string{"exclude", "literal path", "build"},
		},
		{
			name:         "glob star within segment is safe",
			input:        "*.log",
			wantPattern:  "*.log",
			wantGlob:     "*.log",
			wantDescBits: []string{"exclude", "glob", "within one segment"},
		},
		{
			name:         "double star spans directories",
			input:        "src/**/generated.go",
			wantPattern:  "src/**/generated.go",
			wantGlob:     "src/**/generated.go",
			wantDescBits: []string{"exclude", "spans directories"},
		},
		{
			name:         "question mark",
			input:        "test?.go",
			wantGlob:     "test?.go",
			wantDescBits: []string{"one character"},
		},
		{
			name:         "broad star matches secrets",
			input:        "*",
			wantGlob:     "*",
			wantRisk:     true,
			wantDescBits: []string{"exclude", "within one segment"},
			wantRiskBits: []string{".ssh", ".env", "凭证.txt"},
		},
		{
			name:         "broad double star matches secrets",
			input:        "**/*",
			wantGlob:     "**/*",
			wantRisk:     true,
			wantRiskBits: []string{".ssh"},
		},
		{
			name:         "literal secret directory is flagged with reason",
			input:        ".ssh",
			wantGlob:     ".ssh",
			wantRisk:     true,
			wantDescBits: []string{"literal path"},
			wantRiskBits: []string{".ssh/id_rsa"},
		},
		{
			name:         "negation never harms sensitive dirs",
			input:        "!keep.log",
			wantGlob:     "keep.log",
			wantNegated:  true,
			wantRisk:     false,
			wantDescBits: []string{"re-include"},
		},
		{
			name:         "negated broad pattern is still safe",
			input:        "!*",
			wantGlob:     "*",
			wantNegated:  true,
			wantRisk:     false,
			wantDescBits: []string{"re-include"},
		},
		{
			name:         "CJK literal secret directory",
			input:        "密钥/",
			wantGlob:     "密钥",
			wantRisk:     true,
			wantDescBits: []string{"literal path", "密钥"},
			wantRiskBits: []string{"密钥/私钥.pem"},
		},
		{
			name:         "CJK secret file",
			input:        "凭证.txt",
			wantGlob:     "凭证.txt",
			wantRisk:     true,
			wantRiskBits: []string{"凭证.txt"},
		},
		{
			name:         "CJK glob across directories is safe",
			input:        "项目/**/生成.go",
			wantGlob:     "项目/**/生成.go",
			wantRisk:     false,
			wantDescBits: []string{"spans directories", "生成.go"},
		},
		{
			name:         "leading slashes trimmed and backslashes normalized",
			input:        "/src\\generated/",
			wantGlob:     "src/generated",
			wantDescBits: []string{"literal path"},
		},
		{
			name:         "whitespace trimmed",
			input:        "   *.tmp   ",
			wantGlob:     "*.tmp",
			wantDescBits: []string{"glob"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DescribeIgnoreRules([]string{tc.input})
			if len(got) != 1 {
				t.Fatalf("DescribeIgnoreRules(%q) returned %d entries, want 1: %#v", tc.input, len(got), got)
			}
			info := got[0]
			wantPattern := tc.wantPattern
			if wantPattern == "" {
				wantPattern = tc.input
			}
			if info.Pattern != wantPattern {
				t.Errorf("Pattern = %q, want %q", info.Pattern, wantPattern)
			}
			if info.Glob != tc.wantGlob {
				t.Errorf("Glob = %q, want %q", info.Glob, tc.wantGlob)
			}
			if info.Negated != tc.wantNegated {
				t.Errorf("Negated = %v, want %v", info.Negated, tc.wantNegated)
			}
			if info.SensitiveRisk != tc.wantRisk {
				t.Errorf("SensitiveRisk = %v, want %v (reason %q)", info.SensitiveRisk, tc.wantRisk, info.RiskReason)
			}
			for _, bit := range tc.wantDescBits {
				if !strings.Contains(info.Description, bit) {
					t.Errorf("Description %q missing %q", info.Description, bit)
				}
			}
			for _, bit := range tc.wantRiskBits {
				if !strings.Contains(info.RiskReason, bit) {
					t.Errorf("RiskReason %q missing %q", info.RiskReason, bit)
				}
			}
			if !tc.wantRisk && info.RiskReason != "" {
				t.Errorf("RiskReason should be empty when not at risk, got %q", info.RiskReason)
			}
		})
	}
}

// 多行保持输入顺序；被跳过的行不产生条目。
func TestDescribeIgnoreRulesOrderAndSkips(t *testing.T) {
	in := []string{
		"# header",
		"*.log",
		"",
		"!keep.log",
		"密钥/",
		"   # trailing comment",
	}
	got := DescribeIgnoreRules(in)
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3: %#v", len(got), got)
	}
	wantGlobs := []string{"*.log", "keep.log", "密钥"}
	for i, want := range wantGlobs {
		if got[i].Glob != want {
			t.Errorf("entry %d glob = %q, want %q", i, got[i].Glob, want)
		}
	}
	if got[1].Negated != true || got[2].SensitiveRisk != true {
		t.Errorf("unexpected flags: %#v", got)
	}
}

// 前导 BOM 是编码元数据：它不得成为模式的一部分，且带 BOM 前缀的注释
// 仍必须被跳过，与文件加载器一致。
func TestDescribeIgnoreRulesStripsBOM(t *testing.T) {
	got := DescribeIgnoreRules([]string{"\ufeff*.log", "\ufeff# comment", "\ufeff!keep.log"})
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2: %#v", len(got), got)
	}
	if got[0].Glob != "*.log" {
		t.Errorf("first glob = %q, want *.log (BOM not stripped?)", got[0].Glob)
	}
	if got[1].Glob != "keep.log" || !got[1].Negated {
		t.Errorf("second entry = %#v, want negated keep.log", got[1])
	}
}

// 该函数是纯函数：从不修改输入并返回新切片。
func TestDescribeIgnoreRulesIsPure(t *testing.T) {
	in := []string{"*.log", "!keep.log", "密钥/"}
	snapshot := append([]string(nil), in...)
	out := DescribeIgnoreRules(in)
	for i := range in {
		if in[i] != snapshot[i] {
			t.Fatalf("input mutated at %d: got %q, want %q", i, in[i], snapshot[i])
		}
	}
	out[0].Pattern = "CHANGED"
	if in[0] != "*.log" {
		t.Fatalf("output aliases input: %#v", in)
	}
}

// 报告的 Glob 正是 Ignored 匹配的字符串：带标志的敏感
// 模式在加载时确实必须排除其探测路径。
func TestDescribeIgnoreRulesRiskMatchesActualIgnored(t *testing.T) {
	info := DescribeIgnoreRules([]string{"密钥/"})
	if len(info) != 1 || !info[0].SensitiveRisk {
		t.Fatalf("expected a flagged CJK pattern, got %#v", info)
	}
	rules := &IgnoreRules{patterns: []pattern{{glob: info[0].Glob, negated: info[0].Negated}}}
	if !rules.Ignored("密钥/私钥.pem") {
		t.Error("reported risky glob should actually ignore the probe path")
	}
}
