package workflow

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// guardRenderWords 渲染模板并用测试内的小型词法还原器拆成 argv。
// 复用 workflow_guard_test.go 中的 guardSplitShellWords，避免重复实现。
func guardRenderWords(t *testing.T, template string, vars map[string]string) []string {
	t.Helper()
	rendered, err := RenderCommand(CommandTemplate{Template: template, Vars: vars})
	if err != nil {
		t.Fatalf("RenderCommand(%q, %#v): %v", template, vars, err)
	}
	return guardSplitShellWords(t, rendered)
}

// guardAssertWords 断言词法还原后的参数列表与期望逐字节一致。
func guardAssertWords(t *testing.T, got, want []string, ctx string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: shell 还原 argv = %#v, want %#v", ctx, got, want)
	}
}

// guardRoundTripValue 把单个值放进 echo {{x}} 模板：先校验引用形态（安全词原样、
// 其余值必须被单引号包裹），再用词法还原器断言精确还原为 {"echo", value}。
// 返回完整渲染文本，便于调用方追加断言。
func guardRoundTripValue(t *testing.T, value string) string {
	t.Helper()
	rendered, err := RenderCommand(CommandTemplate{
		Template: "echo {{x}}",
		Vars:     map[string]string{"x": value},
	})
	if err != nil {
		t.Fatalf("RenderCommand 值 %q: %v", value, err)
	}
	if !strings.HasPrefix(rendered, "echo ") {
		t.Fatalf("渲染结果缺少命令前缀: %q", rendered)
	}
	quoted := strings.TrimPrefix(rendered, "echo ")
	if !safeShellWord(value) && !(strings.HasPrefix(quoted, "'") && strings.HasSuffix(quoted, "'")) {
		t.Fatalf("危险值未被单引号包裹: value=%q quoted=%q rendered=%q", value, quoted, rendered)
	}
	guardAssertWords(t, guardSplitShellWords(t, rendered), []string{"echo", value},
		fmt.Sprintf("值 %q", value))
	return rendered
}

// TestGuardRenderExhaustiveValueRoundTrip 穷举各类值，确认 RenderCommand 的引用
// 不会在 shell 词法层面丢失或改写任何一个字节：ASCII 安全词、空值与空白、shell
// 元字符（引号/反斜杠/$()/分号/管道）、控制字符与换行、CJK 与 emoji。
func TestGuardRenderExhaustiveValueRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"ascii safe word", "abcXYZ019"},
		{"ascii safe punctuation", "safe_path-1.0.tar.gz"},
		{"ascii safe separators", "a:b,c@d%e+f=g"},
		{"empty", ""},
		{"single space", " "},
		{"only spaces", "   "},
		{"leading and trailing spaces", "  padded  "},
		{"space inside", "hello world"},
		{"tab", "a\tb"},
		{"only tab", "\t"},
		{"semicolon", "a;b"},
		{"semicolon command chain", "a;rm -rf /"},
		{"pipe", "a|b"},
		{"pipe chain", "a || b && c"},
		{"dollar", "$HOME"},
		{"braced dollar", "${HOME}"},
		{"command substitution", "$(id)"},
		{"nested command substitution", "$(echo $(whoami))"},
		{"backtick", "`whoami`"},
		{"single quote", "it's"},
		{"multiple single quotes", "x'y'z"},
		{"only single quote", "'"},
		{"only double single quotes", "''"},
		{"backslash", "\\"},
		{"backslash inside", "a\\b"},
		{"backslash before quote", "a\\'b"},
		{"newline", "a\nb"},
		{"only newline", "\n"},
		{"crlf", "a\r\nb"},
		{"carriage return", "a\rb"},
		{"control x01", "\x01"},
		{"control x1f", "a\x1fb"},
		{"del", "\x7f"},
		{"nul byte", "a\x00b"},
		{"mixed control and quote", "a\x00b'c\n\t\x7f"},
		{"cjk", "世界"},
		{"cjk with space", "你好 世界"},
		{"cjk with quote", "中文'引号"},
		{"cjk mixed emoji", "日本語😀混在"},
		{"emoji", "😀"},
		{"emoji sequence", "🎉✨🚀"},
		{"emoji with space", "🚀 🚀"},
		{"emoji with quote and newline", "emoji 'quoted' 😀\n"},
		{"placeholder looking", "{{y}}"},
		{"placeholder with injection", "{{missing}}; $(id)"},
		{"default looking", "{{a:-b}}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			guardRoundTripValue(t, tc.value)
		})
	}
}

// TestGuardRenderNestedPlaceholderNotRescanned 验证替换产生的文本不再被当作新标记：
// 值中的 {{...}} 连同其中的分号、命令替换都作为字面量还原；即便内层名字不存在于
// Vars，也不会触发 ErrMissingVar。
func TestGuardRenderNestedPlaceholderNotRescanned(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"plain nested name", "{{y}}"},
		{"nested with default", "{{y:-fallback}}"},
		{"nested missing with injection", "{{boom}}; $(id)"},
		{"two nested markers", "{{a}} {{b}}"},
		{"triple braces", "{{{x}}}"},
		{"nested cjk name", "{{名字:-世界}}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Vars 只有 x：若内层标记被重扫，{{boom}} 等会触发 ErrMissingVar。
			words := guardRenderWords(t, "echo {{x}}", map[string]string{"x": tc.value})
			guardAssertWords(t, words, []string{"echo", tc.value}, "嵌套标记不被重扫")
		})
	}
}

// TestGuardRenderMultiArgBoundaries 验证多参数边界：值里的空格、制表符、换行、
// 引号或看似命令的文本都不会把相邻参数拼在一起，也不会被拆成更多参数。
func TestGuardRenderMultiArgBoundaries(t *testing.T) {
	t.Run("mixed dangerous values", func(t *testing.T) {
		words := guardRenderWords(t, "guardcmd {{a}} {{b}} {{c}} {{d}}", map[string]string{
			"a": "hello world",
			"b": "x'y",
			"c": "{{z}} $(id)",
			"d": "😀 世界\n末行",
		})
		guardAssertWords(t, words,
			[]string{"guardcmd", "hello world", "x'y", "{{z}} $(id)", "😀 世界\n末行"},
			"多参数混合危险值")
	})

	t.Run("empty and whitespace values", func(t *testing.T) {
		words := guardRenderWords(t, "guardcmd {{a}} {{b}} {{c}} {{d}}", map[string]string{
			"a": "  padded  ",
			"b": "",
			"c": "\t",
			"d": "a\nb",
		})
		guardAssertWords(t, words,
			[]string{"guardcmd", "  padded  ", "", "\t", "a\nb"},
			"空值与空白参数")
	})

	t.Run("value equal to command name", func(t *testing.T) {
		words := guardRenderWords(t, "guardcmd {{a}} {{b}}", map[string]string{
			"a": "guardcmd",
			"b": "echo",
		})
		guardAssertWords(t, words, []string{"guardcmd", "guardcmd", "echo"}, "值与命令名相同")
	})

	t.Run("repeated variable keeps boundaries", func(t *testing.T) {
		words := guardRenderWords(t, "guardcmd {{a}} {{a}} {{a}}", map[string]string{
			"a": "x y",
		})
		guardAssertWords(t, words, []string{"guardcmd", "x y", "x y", "x y"}, "重复变量")
	})
}

// TestGuardRenderValueDefaultCombo 验证显式值与默认值的组合：默认值同样经过 shell
// 引用，缺席时才生效，显式提供的空值也算提供；两者都参与参数边界守护。
func TestGuardRenderValueDefaultCombo(t *testing.T) {
	template := "run {{a}} {{b:-fallback value}} {{c}} {{d:-世界 'q'}}"

	t.Run("defaults fill missing vars", func(t *testing.T) {
		words := guardRenderWords(t, template, map[string]string{
			"a": "A;B",
			"c": "$HOME",
		})
		guardAssertWords(t, words,
			[]string{"run", "A;B", "fallback value", "$HOME", "世界 'q'"},
			"默认值补全")
	})

	t.Run("provided values override defaults", func(t *testing.T) {
		words := guardRenderWords(t, template, map[string]string{
			"a": "A;B",
			"b": "override;$(id)",
			"c": "$HOME",
			"d": "",
		})
		guardAssertWords(t, words,
			[]string{"run", "A;B", "override;$(id)", "$HOME", ""},
			"显式值覆盖默认值")
	})

	t.Run("default with metacharacters", func(t *testing.T) {
		words := guardRenderWords(t, "echo {{x:-$(id);a|b}}", nil)
		guardAssertWords(t, words, []string{"echo", "$(id);a|b"}, "默认值含元字符")
	})

	t.Run("default with single quote", func(t *testing.T) {
		words := guardRenderWords(t, "echo {{x:-it's}}", nil)
		guardAssertWords(t, words, []string{"echo", "it's"}, "默认值含单引号")
	})
}

// TestGuardRenderDoesNotMutateInput 固定纯函数性质：无论值多复杂、是否使用默认值，
// 调用前后 Template 与 Vars 都逐字节不变，重复调用结果一致，nil Vars 保持 nil。
func TestGuardRenderDoesNotMutateInput(t *testing.T) {
	vars := map[string]string{
		"a":      "hello world",
		"b":      "it's\\$(id)\n😀",
		"c":      "{{nested}}",
		"unused": "keep",
	}
	snapshot := map[string]string{
		"a":      "hello world",
		"b":      "it's\\$(id)\n😀",
		"c":      "{{nested}}",
		"unused": "keep",
	}
	template := "run {{a}} {{b:-世界 'q'}} {{c}} {{missing:-$(id)}}"
	tmpl := CommandTemplate{Template: template, Vars: vars}

	first, err := RenderCommand(tmpl)
	if err != nil {
		t.Fatalf("RenderCommand: %v", err)
	}
	second, err := RenderCommand(tmpl)
	if err != nil {
		t.Fatalf("重复 RenderCommand: %v", err)
	}
	if first != second {
		t.Fatalf("重复调用结果不一致: %q vs %q", first, second)
	}
	if tmpl.Template != template {
		t.Fatalf("Template 被修改: %q", tmpl.Template)
	}
	if !reflect.DeepEqual(tmpl.Vars, snapshot) {
		t.Fatalf("Vars 被修改: got %#v, want %#v", tmpl.Vars, snapshot)
	}

	// nil Vars 与含默认值的模板：保持 nil，不引入空映射。
	nilTmpl := CommandTemplate{Template: "echo {{x:-d}}", Vars: nil}
	if _, err := RenderCommand(nilTmpl); err != nil {
		t.Fatalf("RenderCommand(nil Vars): %v", err)
	}
	if nilTmpl.Vars != nil {
		t.Fatalf("nil Vars 被改写成 %#v", nilTmpl.Vars)
	}
}
