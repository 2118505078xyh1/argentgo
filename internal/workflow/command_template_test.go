package workflow

import (
	"errors"
	"reflect"
	"testing"
)

// TestRenderCommandSubstitutesValues 固定基础替换行为：提供的值被写入，
// 安全词不加引用，重复变量逐个替换，且注入的标记不会被重扫。
func TestRenderCommandSubstitutesValues(t *testing.T) {
	cases := []struct {
		name     string
		template string
		vars     map[string]string
		want     string
	}{
		{"no placeholder", "echo hi", nil, "echo hi"},
		{"single", "echo {{name}}", map[string]string{"name": "Ada"}, "echo Ada"},
		{"multiple placeholders", "{{tool}} --out {{path}}", map[string]string{"tool": "go", "path": "/tmp/x"}, "go --out /tmp/x"},
		{"repeated var", "{{x}} {{x}} {{x}}", map[string]string{"x": "v"}, "v v v"},
		{"trimmed name", "echo {{  name  }}", map[string]string{"name": "Ada"}, "echo Ada"},
		{"extra vars ignored", "echo {{a}}", map[string]string{"a": "1", "b": "2"}, "echo 1"},
		{"unterminated kept", "echo {{ok}} tail {{bad", map[string]string{"ok": "1"}, "echo 1 tail {{bad"},
		{"empty name kept", "echo {{}}", nil, "echo {{}}"},
		{
			name:     "injected placeholder not rescanned",
			template: "echo {{x}}",
			vars:     map[string]string{"x": "{{y}}"},
			want:     "echo '{{y}}'",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RenderCommand(CommandTemplate{Template: tc.template, Vars: tc.vars})
			if err != nil {
				t.Fatalf("RenderCommand(%q): %v", tc.template, err)
			}
			if got != tc.want {
				t.Fatalf("RenderCommand(%q) = %q, want %q", tc.template, got, tc.want)
			}
		})
	}
}

// TestRenderCommandMissingVar 固定缺失变量的错误语义：错误必须能由
// errors.Is 匹配 ErrMissingVar，并且点名缺失的变量。
func TestRenderCommandMissingVar(t *testing.T) {
	cases := []struct {
		name     string
		template string
		vars     map[string]string
		wantName string
	}{
		{"nil vars", "echo {{name}}", nil, "name"},
		{"other var present", "echo {{a}} {{b}}", map[string]string{"a": "1"}, "b"},
		{"empty map", "echo {{name}}", map[string]string{}, "name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := RenderCommand(CommandTemplate{Template: tc.template, Vars: tc.vars})
			if !errors.Is(err, ErrMissingVar) {
				t.Fatalf("RenderCommand(%q) error = %v, want ErrMissingVar", tc.template, err)
			}
		})
	}
}

// TestRenderCommandDefaultValues 固定 {{var:-default}} 的语义：变量缺席时
// 用默认值（默认值同样经过 shell 引用），有值时值优先，名字两端空白被裁剪。
func TestRenderCommandDefaultValues(t *testing.T) {
	cases := []struct {
		name     string
		template string
		vars     map[string]string
		want     string
	}{
		{"missing uses default", "echo {{name:-world}}", nil, "echo world"},
		{"value wins over default", "echo {{name:-world}}", map[string]string{"name": "Ada"}, "echo Ada"},
		{"default with spaces quoted", "echo {{name:-hello world}}", nil, "echo 'hello world'"},
		{"default keeps extra separator", "echo {{url:-a:-b}}", nil, "echo a:-b"},
		{"trimmed name and default", "echo {{ name :- world }}", nil, "echo world"},
		{"empty value is not missing", "echo {{name:-world}}", map[string]string{"name": ""}, "echo ''"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RenderCommand(CommandTemplate{Template: tc.template, Vars: tc.vars})
			if err != nil {
				t.Fatalf("RenderCommand(%q): %v", tc.template, err)
			}
			if got != tc.want {
				t.Fatalf("RenderCommand(%q) = %q, want %q", tc.template, got, tc.want)
			}
		})
	}
}

// TestRenderCommandQuotesSpecialValues 固定 shell 引用的规则：安全词原样
// 输出，空值与含特殊字符（含 CJK）的值用单引号包裹，内嵌单引号被转义。
func TestRenderCommandQuotesSpecialValues(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"safe path", "/tmp/foo-bar_1.txt", "/tmp/foo-bar_1.txt"},
		{"safe letters", "ABCxyz019", "ABCxyz019"},
		{"empty", "", "''"},
		{"space", "hello world", "'hello world'"},
		{"semicolon injection", "a;rm -rf /", "'a;rm -rf /'"},
		{"dollar injection", "$HOME", "'$HOME'"},
		{"embedded single quote", "it's", "'it'\\''s'"},
		{"cjk", "世界", "'世界'"},
		{"cjk with space", "你好 世界", "'你好 世界'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RenderCommand(CommandTemplate{
				Template: "echo {{x}}",
				Vars:     map[string]string{"x": tc.value},
			})
			if err != nil {
				t.Fatalf("RenderCommand value %q: %v", tc.value, err)
			}
			want := "echo " + tc.want
			if got != want {
				t.Fatalf("RenderCommand value %q = %q, want %q", tc.value, got, want)
			}
		})
	}
}

// TestRenderCommandDoesNotMutateInput 固定纯函数性质：调用前后模板字符串与
// Vars 映射保持不变。
func TestRenderCommandDoesNotMutateInput(t *testing.T) {
	vars := map[string]string{"name": "Ada", "path": "/tmp/x"}
	snapshot := map[string]string{"name": "Ada", "path": "/tmp/x"}
	tmpl := CommandTemplate{Template: "echo {{name}} {{path}} {{missing:-fallback}}", Vars: vars}
	if _, err := RenderCommand(tmpl); err != nil {
		t.Fatalf("RenderCommand: %v", err)
	}
	if tmpl.Template != "echo {{name}} {{path}} {{missing:-fallback}}" {
		t.Fatalf("Template mutated to %q", tmpl.Template)
	}
	if !reflect.DeepEqual(tmpl.Vars, snapshot) {
		t.Fatalf("Vars mutated to %#v, want %#v", tmpl.Vars, snapshot)
	}
}

// TestTemplateVars 固定占位符提取：按首次出现顺序去重，默认值只报告名字，
// 空名与未终止的标记被忽略。
func TestTemplateVars(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want []string
	}{
		{"none", "plain text", nil},
		{"stable order and dedup", "{{b}} {{a}} {{b}} {{c}}", []string{"b", "a", "c"}},
		{"default reports name only", "{{url:-http://x}}", []string{"url"}},
		{"mixed defaults", "{{a:-1}} {{b}} {{a:-2}}", []string{"a", "b"}},
		{"trimmed names", "{{ a }} {{b}}", []string{"a", "b"}},
		{"ignores empty and unterminated", "{{}} {{ok}} {{unterminated", []string{"ok"}},
		{"cjk value unaffected", "{{名字:-世界}}", []string{"名字"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TemplateVars(tc.s)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("TemplateVars(%q) = %q, want %q", tc.s, got, tc.want)
			}
		})
	}
}
