package provider_test

import (
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
