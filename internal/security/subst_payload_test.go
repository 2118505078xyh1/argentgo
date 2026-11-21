package security

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestExtractCommandSubstitutionsSingle 固定最基础的提取行为：单层 $(...) 与
// 反引号，以及双引号内的替换（sanitizeCommand 会遮盖它，正是本原语要补的洞）。
func TestExtractCommandSubstitutionsSingle(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{"dollar paren", `echo $(mv a b)`, []string{`mv a b`}},
		{"inside double quotes", `echo "$(mv a b)"`, []string{`mv a b`}},
		{"backtick", "echo `mv a b`", []string{`mv a b`}},
		{"parameter expansion is not a substitution", `echo ${HOME}`, nil},
		{"plain variable is not a substitution", `echo $HOME`, nil},
		{"plain parens are not a substitution", `echo (mv a b)`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractCommandSubstitutions(tc.command)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ExtractCommandSubstitutions(%q) = %q, want %q", tc.command, got, tc.want)
			}
		})
	}
}

// TestExtractCommandSubstitutionsNested 验证 $(...) 与反引号相互嵌套时配对正确，
// 且外层载荷在前、内层载荷在后（按替换起点顺序稳定排列）。
func TestExtractCommandSubstitutionsNested(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{
			"nested dollar paren",
			`echo $(echo $(mv a b))`,
			[]string{`echo $(mv a b)`, `mv a b`},
		},
		{
			"backtick inside dollar paren",
			"echo $(echo `mv a b`)",
			[]string{"echo `mv a b`", `mv a b`},
		},
		{
			"dollar paren inside backtick",
			"echo `echo $(mv a b)`",
			[]string{`echo $(mv a b)`, `mv a b`},
		},
		{
			"nested inside double quotes",
			`echo "$(echo $(mv a b))"`,
			[]string{`echo $(mv a b)`, `mv a b`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractCommandSubstitutions(tc.command)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ExtractCommandSubstitutions(%q) = %q, want %q", tc.command, got, tc.want)
			}
		})
	}
}

// TestExtractCommandSubstitutionsMultiple 验证多个并列替换按出现顺序返回。
func TestExtractCommandSubstitutionsMultiple(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{
			"two dollar parens",
			`echo $(mv a) $(cp b c)`,
			[]string{`mv a`, `cp b c`},
		},
		{
			"two quoted dollar parens",
			`echo "$(mv a)" "$(rm b)"`,
			[]string{`mv a`, `rm b`},
		},
		{
			"mixed dollar paren and backtick",
			"echo $(mv a) `cp b c`",
			[]string{`mv a`, `cp b c`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractCommandSubstitutions(tc.command)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ExtractCommandSubstitutions(%q) = %q, want %q", tc.command, got, tc.want)
			}
		})
	}
}

// TestExtractCommandSubstitutionsEscapes 验证转义与单引号字面量不会开启替换。
func TestExtractCommandSubstitutionsEscapes(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{"escaped dollar outside quotes", `echo \$(mv a b)`, nil},
		{"escaped dollar inside double quotes", `echo "\$(mv a b)"`, nil},
		{"single quoted literal", `echo '$(mv a b)'`, nil},
		{
			"escaped quote inside double quotes does not hide substitution",
			`echo "$(echo \"$(mv a b)\")"`,
			[]string{`echo \"$(mv a b)\"`, `mv a b`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractCommandSubstitutions(tc.command)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ExtractCommandSubstitutions(%q) = %q, want %q", tc.command, got, tc.want)
			}
		})
	}
}

// TestExtractCommandSubstitutionsCJK 验证 CJK 文本原样保留且不被切坏。
func TestExtractCommandSubstitutionsCJK(t *testing.T) {
	command := `echo "$(mv /tmp/数据 /tmp/备份)"`
	want := []string{`mv /tmp/数据 /tmp/备份`}
	got := ExtractCommandSubstitutions(command)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtractCommandSubstitutions(%q) = %q, want %q", command, got, want)
	}
}

// TestExtractCommandSubstitutionsBounded 验证超长与超深输入有界、可终止，且截断
// 发生在 rune 边界（CJK 不会被切开）。
func TestExtractCommandSubstitutionsBounded(t *testing.T) {
	t.Run("long ascii payload", func(t *testing.T) {
		command := `$(echo ` + strings.Repeat("x", 20000) + `)`
		got := ExtractCommandSubstitutions(command)
		if len(got) != 1 {
			t.Fatalf("got %d payloads, want 1", len(got))
		}
		if len(got[0]) > maxSubstitutionPayloadBytes {
			t.Fatalf("payload length %d exceeds bound %d", len(got[0]), maxSubstitutionPayloadBytes)
		}
	})

	t.Run("long cjk payload is valid utf8", func(t *testing.T) {
		command := `$(echo ` + strings.Repeat("数", 5000) + `)`
		got := ExtractCommandSubstitutions(command)
		if len(got) != 1 {
			t.Fatalf("got %d payloads, want 1", len(got))
		}
		if len(got[0]) > maxSubstitutionPayloadBytes {
			t.Fatalf("payload length %d exceeds bound %d", len(got[0]), maxSubstitutionPayloadBytes)
		}
		if !utf8.ValidString(got[0]) {
			t.Fatal("truncated payload is not valid UTF-8")
		}
	})

	t.Run("deep nesting terminates", func(t *testing.T) {
		command := strings.Repeat(`$(`, 40) + `mv a b` + strings.Repeat(`)`, 40)
		got := ExtractCommandSubstitutions(command)
		if len(got) == 0 || len(got) > maxSubstitutionDepth+1 {
			t.Fatalf("got %d payloads, want between 1 and %d", len(got), maxSubstitutionDepth+1)
		}
	})
}

// TestExtractCommandSubstitutionsDoesNotMutateInput 验证入参不被修改，返回值不
// 与入参共享底层内存。
func TestExtractCommandSubstitutionsDoesNotMutateInput(t *testing.T) {
	command := `echo "$(mv a b)"`
	original := command
	got := ExtractCommandSubstitutions(command)
	if command != original {
		t.Fatalf("input mutated: %q", command)
	}
	got[0] = "changed"
	if command != original {
		t.Fatalf("mutating result changed input: %q", command)
	}
}

// TestClassifyWithSubstitutions 验证主命令或任一替换载荷含修改类命令时返回 true。
func TestClassifyWithSubstitutions(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    bool
	}{
		{"plain mutating command", `rm -rf /`, true},
		{"mutating in dollar paren", `echo "$(mv a b)"`, true},
		{"mutating in backtick", "echo `chmod +x x`", true},
		{"mutating in nested dollar paren", `echo "$(echo $(git push))"`, true},
		{"mutating in second substitution", `echo "$(whoami)" "$(cp a b)"`, true},
		{"read only command", `ls -la`, false},
		{"read only substitution", `echo "$(whoami)"`, false},
		{"read only backtick", "echo `cat file`", false},
		{"escaped substitution", `echo "\$(mv a b)"`, false},
		{"single quoted literal", `echo '$(mv a b)'`, false},
		{"plain text", `echo "hello"`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyWithSubstitutions(tc.command); got != tc.want {
				t.Fatalf("ClassifyWithSubstitutions(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}

// TestClassifyWithSubstitutionsCatchesMaskedSubstitution 是针对可靠性评估 #2 的
// 回归测试：FindFileEditingCommand 会遮盖双引号字符串，因此对 `echo "$(mv a b)"`
// 漏报；本原语必须补上这个缺口。
func TestClassifyWithSubstitutionsCatchesMaskedSubstitution(t *testing.T) {
	command := `echo "$(mv a b)"`
	if token := FindFileEditingCommand(command); token != "" {
		t.Fatalf("baseline unexpectedly reports %q; the regression precondition changed", token)
	}
	if !ClassifyWithSubstitutions(command) {
		t.Fatal("ClassifyWithSubstitutions must report the masked $(mv a b) substitution")
	}
}
