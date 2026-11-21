package provider_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/provider"
)

func TestListCoversBuiltInProviders(t *testing.T) {
	want := map[string]struct {
		protocol     provider.Protocol
		baseURL      string
		apiKeyEnv    string
		defaultModel string
	}{
		"anthropic":  {provider.ProtocolAnthropic, "https://api.anthropic.com/v1", "ANTHROPIC_API_KEY", "claude-sonnet-5-5"},
		"openai":     {provider.ProtocolOpenAI, "https://api.openai.com/v1", "OPENAI_API_KEY", "gpt-5.6"},
		"deepseek":   {provider.ProtocolAnthropic, "https://api.deepseek.com/anthropic", "DEEPSEEK_API_KEY", "deepseek-v4-pro"},
		"moonshot":   {provider.ProtocolAnthropic, "https://api.moonshot.cn/anthropic", "MOONSHOT_API_KEY", "kimi-k3.1-pro"},
		"zhipu":      {provider.ProtocolAnthropic, "https://open.bigmodel.cn/api/anthropic", "ZHIPU_API_KEY", "GLM-5.3"},
		"qwen":       {provider.ProtocolOpenAI, "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", "DASHSCOPE_API_KEY", "qwen3.8-max"},
		"openrouter": {provider.ProtocolOpenAI, "https://openrouter.ai/api/v1", "OPENROUTER_API_KEY", "anthropic/claude-sonnet-4.5"},
		"ollama":     {provider.ProtocolOpenAI, "http://127.0.0.1:11434/v1", "", "llama3.2"},
		"lmstudio":   {provider.ProtocolOpenAI, "http://127.0.0.1:1234/v1", "", "local-model"},
		"gemini":     {provider.ProtocolOpenAI, "https://generativelanguage.googleapis.com/v1beta/openai", "GEMINI_API_KEY", "gemini-2.5-flash"},
		"groq":       {provider.ProtocolOpenAI, "https://api.groq.com/openai/v1", "GROQ_API_KEY", "llama-3.3-70b-versatile"},
		"mistral":    {provider.ProtocolOpenAI, "https://api.mistral.ai/v1", "MISTRAL_API_KEY", "mistral-large-latest"},
		"together":   {provider.ProtocolOpenAI, "https://api.together.xyz/v1", "TOGETHER_API_KEY", "meta-llama/Llama-3.3-70B"},
		"fireworks":  {provider.ProtocolOpenAI, "https://api.fireworks.ai/inference/v1", "FIREWORKS_API_KEY", "accounts/fireworks/models/llama-v3p3-70b"},
		"cerebras":   {provider.ProtocolOpenAI, "https://api.cerebras.ai/v1", "CEREBRAS_API_KEY", "llama-3.3-70b"},
	}

	seen := make(map[string]bool)
	for _, template := range provider.List() {
		seen[template.Name] = true
		expected, ok := want[template.Name]
		if !ok {
			continue
		}
		if template.Protocol != expected.protocol {
			t.Errorf("%s protocol = %q, want %q", template.Name, template.Protocol, expected.protocol)
		}
		if template.BaseURL != expected.baseURL {
			t.Errorf("%s base URL = %q, want %q", template.Name, template.BaseURL, expected.baseURL)
		}
		if template.APIKeyEnv != expected.apiKeyEnv {
			t.Errorf("%s api key env = %q, want %q", template.Name, template.APIKeyEnv, expected.apiKeyEnv)
		}
		if template.DefaultModel != expected.defaultModel {
			t.Errorf("%s default model = %q, want %q", template.Name, template.DefaultModel, expected.defaultModel)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("provider %q missing from List()", name)
		}
	}
}

func TestListIsUniqueAndComplete(t *testing.T) {
	seen := make(map[string]bool)
	for _, template := range provider.List() {
		if seen[template.Name] {
			t.Fatalf("duplicate provider %q", template.Name)
		}
		seen[template.Name] = true
		if template.Name == "" || template.BaseURL == "" || template.DefaultModel == "" {
			t.Fatalf("incomplete template: %#v", template)
		}
		switch template.Protocol {
		case provider.ProtocolAnthropic, provider.ProtocolOpenAI, provider.ProtocolResponses:
		default:
			t.Fatalf("%s has unknown protocol %q", template.Name, template.Protocol)
		}
	}
}

func TestResolve(t *testing.T) {
	cases := []struct {
		name     string
		wantName string
		wantOK   bool
	}{
		{"anthropic", "anthropic", true},
		{"  Anthropic ", "anthropic", true},
		{"openai", "openai", true},
		{"deepseek", "deepseek", true},
		{"moonshot", "moonshot", true},
		{"kimi", "moonshot", true},
		{"zhipu", "zhipu", true},
		{"glm", "zhipu", true},
		{"qwen", "qwen", true},
		{"dashscope", "qwen", true},
		{"ollama", "ollama", true},
		{"lmstudio", "lmstudio", true},
		{"openrouter", "openrouter", true},
		{"gemini", "gemini", true},
		{"google", "gemini", true},
		{"groq", "groq", true},
		{"mistral", "mistral", true},
		{"nope", "", false},
		{"", "", false},
	}

	for _, tc := range cases {
		template, ok := provider.Resolve(tc.name)
		if ok != tc.wantOK {
			t.Fatalf("Resolve(%q) ok = %v, want %v", tc.name, ok, tc.wantOK)
		}
		if !ok {
			continue
		}
		if template.Name != tc.wantName {
			t.Fatalf("Resolve(%q) = %q, want %q", tc.name, template.Name, tc.wantName)
		}
	}
}

func TestTemplateAuthResolution(t *testing.T) {
	env := map[string]string{
		"LEGACY_KEY": "legacy-key",
		"EMPTY_KEY":  "  ",
		"FIRST_KEY":  "first-key",
		"SECOND_KEY": "second-key",
	}
	lookup := func(name string) string { return env[name] }

	cases := []struct {
		name       string
		template   provider.Template
		header     string
		scheme     string
		wantHeader string
		wantValue  string
	}{
		{
			name:       "legacy APIKeyEnv still resolves",
			template:   provider.Template{APIKeyEnv: "LEGACY_KEY"},
			header:     "x-api-key",
			wantHeader: "x-api-key",
			wantValue:  "legacy-key",
		},
		{
			name:       "first env empty then second wins",
			template:   provider.Template{APIKeyEnvs: []string{"EMPTY_KEY", "FIRST_KEY", "SECOND_KEY"}},
			header:     "Authorization",
			scheme:     "Bearer",
			wantHeader: "Authorization",
			wantValue:  "Bearer first-key",
		},
		{
			name:       "legacy env first for compatibility",
			template:   provider.Template{APIKeyEnv: "LEGACY_KEY", APIKeyEnvs: []string{"FIRST_KEY"}},
			header:     "x-api-key",
			wantHeader: "x-api-key",
			wantValue:  "legacy-key",
		},
		{
			name:     "all envs empty sends no header",
			template: provider.Template{APIKeyEnvs: []string{"EMPTY_KEY", "MISSING_KEY"}},
			header:   "x-api-key",
		},
		{
			name:     "no auth overrides a resolvable env",
			template: provider.Template{NoAuth: true, APIKeyEnv: "LEGACY_KEY", APIKeyEnvs: []string{"FIRST_KEY"}},
			header:   "Authorization",
			scheme:   "Bearer",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth := tc.template.Auth(lookup, tc.header, tc.scheme)
			if auth.Header != tc.wantHeader || auth.Value.Value() != tc.wantValue {
				t.Fatalf("auth = %q/%q, want %q/%q", auth.Header, auth.Value.Value(), tc.wantHeader, tc.wantValue)
			}
			headers := make(http.Header)
			auth.Apply(headers)
			if got := headers.Get(tc.header); got != tc.wantValue {
				t.Fatalf("%s = %q, want %q", tc.header, got, tc.wantValue)
			}
			if tc.wantValue != "" && strings.Contains(fmt.Sprintf("%+v", auth), tc.wantValue) {
				t.Fatalf("auth formatting leaked the credential: %+v", auth)
			}
			if len(headers) > 1 {
				t.Fatalf("unexpected extra headers: %#v", headers)
			}
		})
	}
}

func TestBuiltInTemplatesHaveNoAuthOverride(t *testing.T) {
	for _, template := range provider.List() {
		if template.NoAuth || len(template.APIKeyEnvs) != 0 {
			t.Fatalf("built-in template %q changed its credential shape", template.Name)
		}
	}
}

func TestListReturnsACopy(t *testing.T) {
	list := provider.List()
	list[0].BaseURL = "https://example.invalid"

	again, ok := provider.Resolve(list[0].Name)
	if !ok {
		t.Fatal("resolve failed")
	}
	if again.BaseURL == "https://example.invalid" {
		t.Fatal("List() leaked internal table storage")
	}
}
