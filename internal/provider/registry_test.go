package provider

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
)

// TestTemplateRegistryStableOrder 验证乱序注册时输出仍按 (order, name) 稳定排序。
func TestTemplateRegistryStableOrder(t *testing.T) {
	cases := []struct {
		name      string
		entries   []builtInTemplate
		wantNames []string
	}{
		{
			name: "乱序注册按 order 排序",
			entries: []builtInTemplate{
				{30, Template{Name: "charlie"}},
				{10, Template{Name: "alpha"}},
				{20, Template{Name: "bravo"}},
			},
			wantNames: []string{"alpha", "bravo", "charlie"},
		},
		{
			name: "相同 order 按 name 决胜",
			entries: []builtInTemplate{
				{10, Template{Name: "charlie"}},
				{10, Template{Name: "alpha"}},
				{10, Template{Name: "bravo"}},
			},
			wantNames: []string{"alpha", "bravo", "charlie"},
		},
		{
			name: "order 与 name 混合决胜",
			entries: []builtInTemplate{
				{20, Template{Name: "delta"}},
				{10, Template{Name: "bravo"}},
				{20, Template{Name: "alpha"}},
				{10, Template{Name: "charlie"}},
			},
			wantNames: []string{"bravo", "charlie", "alpha", "delta"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r templateRegistry
			for _, entry := range tc.entries {
				if err := r.Register(entry.template.Name, entry.order, entry.template); err != nil {
					t.Fatalf("Register(%q) = %v", entry.template.Name, err)
				}
			}
			if got := r.Names(); !slices.Equal(got, tc.wantNames) {
				t.Fatalf("Names() = %v, want %v", got, tc.wantNames)
			}
			// Templates 的顺序必须与 Names 一致，且携带对应条目。
			templates := r.Templates()
			if len(templates) != len(tc.wantNames) {
				t.Fatalf("len(Templates()) = %d, want %d", len(templates), len(tc.wantNames))
			}
			for i, template := range templates {
				if template.Name != tc.wantNames[i] {
					t.Fatalf("Templates()[%d].Name = %q, want %q", i, template.Name, tc.wantNames[i])
				}
			}
		})
	}
}

// TestTemplateRegistryRejectsDuplicateAndEmpty 验证重名与空名在注册期即报错，且不污染注册表。
func TestTemplateRegistryRejectsDuplicateAndEmpty(t *testing.T) {
	cases := []struct {
		name     string
		badName  string
		badOrder int
		wantErr  error
	}{
		{name: "空名称", badName: "", wantErr: ErrEmptyTemplateName},
		{name: "纯空白名称", badName: "   ", wantErr: ErrEmptyTemplateName},
		{name: "同名同 order 重名", badName: "x", badOrder: 0, wantErr: ErrDuplicateTemplate},
		{name: "同名不同 order 重名", badName: "x", badOrder: 5, wantErr: ErrDuplicateTemplate},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r templateRegistry
			if err := r.Register("x", 0, Template{Name: "x"}); err != nil {
				t.Fatalf("seed Register(x) = %v", err)
			}
			err := r.Register(tc.badName, tc.badOrder, Template{Name: tc.badName})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Register(%q) error = %v, want errors.Is(_, %v)", tc.badName, err, tc.wantErr)
			}
			if r.Len() != 1 {
				t.Fatalf("Len() = %d, want 1: failed registration must not mutate the registry", r.Len())
			}
		})
	}
}

// TestTemplateRegistryMustRegisterPanicsOnDuplicate 验证静态注册入口把重名当作程序员错误立即暴露。
func TestTemplateRegistryMustRegisterPanicsOnDuplicate(t *testing.T) {
	var r templateRegistry
	r.MustRegister("x", 0, Template{Name: "x"})
	defer func() {
		if recover() == nil {
			t.Fatal("MustRegister should panic on duplicate")
		}
	}()
	r.MustRegister("x", 1, Template{Name: "x"})
}

// TestResolveUnchangedByRegistry 验证 Resolve 对外签名与解析结果不因注册表改造而改变。
func TestResolveUnchangedByRegistry(t *testing.T) {
	cases := []struct {
		query    string
		wantName string
		wantOK   bool
	}{
		{"anthropic", "anthropic", true},
		{"  Anthropic  ", "anthropic", true},
		{"openai", "openai", true},
		{"claude", "anthropic", true},
		{"kimi", "moonshot", true},
		{"glm", "zhipu", true},
		{"bigmodel.cn", "zhipu", true},
		{"z.ai", "zai", true},
		{"grok", "xai", true},
		{"open-router", "openrouter", true},
		{"lm_studio", "lmstudio", true},
		{"local", "local", true},
		{"nope", "", false},
		{"", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			got, ok := Resolve(tc.query)
			if ok != tc.wantOK {
				t.Fatalf("Resolve(%q) ok = %v, want %v", tc.query, ok, tc.wantOK)
			}
			if !ok {
				if !reflect.DeepEqual(got, Template{}) {
					t.Fatalf("Resolve(%q) = %#v, want zero Template", tc.query, got)
				}
				return
			}
			if got.Name != tc.wantName {
				t.Fatalf("Resolve(%q) = %q, want %q", tc.query, got.Name, tc.wantName)
			}
			// 解析结果必须与 List 中同名条目完全一致（逐字段）。
			var fromList *Template
			for _, template := range List() {
				if template.Name == got.Name {
					copy := template
					fromList = &copy
					break
				}
			}
			if fromList == nil || !reflect.DeepEqual(got, *fromList) {
				t.Fatalf("Resolve(%q) = %#v, List entry = %#v", tc.query, got, fromList)
			}
		})
	}
}

// TestDefaultListByteForByteStable 锁定默认模板的顺序与内容，防止注册表改造产生任何字节级漂移。
func TestDefaultListByteForByteStable(t *testing.T) {
	const want = `[{"Name":"anthropic","Protocol":"anthropic-messages","BaseURL":"https://api.anthropic.com/v1","APIKeyEnv":"ANTHROPIC_API_KEY","APIKeyEnvs":null,"NoAuth":false,"DefaultModel":"claude-sonnet-5-5","CacheSupport":true},{"Name":"openai","Protocol":"openai-chat-completions","BaseURL":"https://api.openai.com/v1","APIKeyEnv":"OPENAI_API_KEY","APIKeyEnvs":null,"NoAuth":false,"DefaultModel":"gpt-5.6","CacheSupport":true},{"Name":"deepseek","Protocol":"anthropic-messages","BaseURL":"https://api.deepseek.com/anthropic","APIKeyEnv":"DEEPSEEK_API_KEY","APIKeyEnvs":null,"NoAuth":false,"DefaultModel":"deepseek-v4-pro","CacheSupport":false},{"Name":"moonshot","Protocol":"anthropic-messages","BaseURL":"https://api.moonshot.cn/anthropic","APIKeyEnv":"MOONSHOT_API_KEY","APIKeyEnvs":null,"NoAuth":false,"DefaultModel":"kimi-k3.1-pro","CacheSupport":false},{"Name":"zhipu","Protocol":"anthropic-messages","BaseURL":"https://open.bigmodel.cn/api/anthropic","APIKeyEnv":"ZHIPU_API_KEY","APIKeyEnvs":null,"NoAuth":false,"DefaultModel":"GLM-5.3","CacheSupport":false},{"Name":"qwen","Protocol":"openai-chat-completions","BaseURL":"https://dashscope-intl.aliyuncs.com/compatible-mode/v1","APIKeyEnv":"DASHSCOPE_API_KEY","APIKeyEnvs":null,"NoAuth":false,"DefaultModel":"qwen3.8-max","CacheSupport":false},{"Name":"openrouter","Protocol":"openai-chat-completions","BaseURL":"https://openrouter.ai/api/v1","APIKeyEnv":"OPENROUTER_API_KEY","APIKeyEnvs":null,"NoAuth":false,"DefaultModel":"anthropic/claude-sonnet-4.5","CacheSupport":false},{"Name":"ollama","Protocol":"openai-chat-completions","BaseURL":"http://127.0.0.1:11434/v1","APIKeyEnv":"","APIKeyEnvs":null,"NoAuth":false,"DefaultModel":"llama3.2","CacheSupport":false},{"Name":"lmstudio","Protocol":"openai-chat-completions","BaseURL":"http://127.0.0.1:1234/v1","APIKeyEnv":"","APIKeyEnvs":null,"NoAuth":false,"DefaultModel":"local-model","CacheSupport":false},{"Name":"xai","Protocol":"openai-responses","BaseURL":"https://api.x.ai/v1","APIKeyEnv":"XAI_API_KEY","APIKeyEnvs":null,"NoAuth":false,"DefaultModel":"grok-4.7","CacheSupport":true},{"Name":"zai","Protocol":"anthropic-messages","BaseURL":"https://api.z.ai/api/anthropic","APIKeyEnv":"ZAI_API_KEY","APIKeyEnvs":null,"NoAuth":false,"DefaultModel":"GLM-5.3","CacheSupport":false},{"Name":"bigmodel","Protocol":"anthropic-messages","BaseURL":"https://open.bigmodel.cn/api/anthropic","APIKeyEnv":"BIGMODEL_API_KEY","APIKeyEnvs":null,"NoAuth":false,"DefaultModel":"GLM-5.3","CacheSupport":false},{"Name":"minimax","Protocol":"anthropic-messages","BaseURL":"https://api.minimaxi.com/anthropic","APIKeyEnv":"MINIMAX_API_KEY","APIKeyEnvs":null,"NoAuth":false,"DefaultModel":"MiniMax-M3","CacheSupport":false},{"Name":"local","Protocol":"openai-chat-completions","BaseURL":"http://127.0.0.1:3425/v1","APIKeyEnv":"","APIKeyEnvs":null,"NoAuth":false,"DefaultModel":"auto/deepseek-v4.1-flash","CacheSupport":false}]`

	got, err := json.Marshal(List())
	if err != nil {
		t.Fatalf("json.Marshal(List()) = %v", err)
	}
	if string(got) != want {
		t.Fatalf("List() drifted from baseline:\n got: %s\nwant: %s", got, want)
	}
}

// TestTemplateRegistrySnapshotsAreIndependent 验证快照互不共享底层存储。
func TestTemplateRegistrySnapshotsAreIndependent(t *testing.T) {
	var r templateRegistry
	r.MustRegister("a", 0, Template{Name: "a", DefaultModel: "m"})

	names := r.Names()
	names[0] = "mutated"
	templates := r.Templates()
	templates[0].DefaultModel = "mutated"

	if got := r.Names(); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("Names() = %v after mutating snapshot", got)
	}
	if got := r.Templates(); got[0].DefaultModel != "m" {
		t.Fatalf("Templates()[0].DefaultModel = %q after mutating snapshot", got[0].DefaultModel)
	}
}

// TestTemplateRegistryZeroValue 验证零值注册表为空且可用。
func TestTemplateRegistryZeroValue(t *testing.T) {
	var r templateRegistry
	if r.Len() != 0 || r.Names() != nil || r.Templates() != nil {
		t.Fatal("zero-value registry should be empty")
	}
	if _, ok := r.Lookup("anything"); ok {
		t.Fatal("zero-value registry should not resolve anything")
	}
}
