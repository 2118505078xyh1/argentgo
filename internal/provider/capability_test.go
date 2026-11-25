package provider_test

import (
	"testing"

	"example.com/argentgo/internal/provider"
)

func TestCacheSupportIsAdvertisedOnlyForCachingProviders(t *testing.T) {
	want := map[string]bool{
		"anthropic":  true,
		"openai":     true,
		"xai":        true,
		"deepseek":   false,
		"moonshot":   false,
		"qwen":       false,
		"openrouter": false,
		"ollama":     false,
		"lmstudio":   false,
		"local":      false,
	}
	for name, expected := range want {
		template, ok := provider.Resolve(name)
		if !ok {
			t.Fatalf("Resolve(%q) failed", name)
		}
		if template.CacheSupport != expected {
			t.Errorf("%s CacheSupport = %v, want %v", name, template.CacheSupport, expected)
		}
	}
	for _, template := range provider.List() {
		if _, checked := want[template.Name]; !checked && template.CacheSupport {
			t.Errorf("%s unexpectedly advertises cache support", template.Name)
		}
	}
}

func TestNormalizeModelUsesCanonicalSpellingAndPreservesPrefixes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  GLM_5.3  ", "GLM-5.3"},
		{"glm–5.3", "GLM-5.3"},
		{"gpt—5.6", "gpt-5.6"},
		{"claude-sonnet-5---5", "claude-sonnet-5-5"},
		{"anthropic/claude-sonnet-4.5", "anthropic/claude-sonnet-4.5"},
		{"auto/deepseek-v4.1-flash", "auto/deepseek-v4.1-flash"},
		{"my-custom/v1", "my-custom/v1"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := provider.NormalizeModel(tc.in); got != tc.want {
			t.Errorf("NormalizeModel(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if got := provider.NormalizeModel(tc.want); got != tc.want {
			t.Errorf("NormalizeModel is not idempotent for %q: %q", tc.want, got)
		}
	}
}

func TestTemplateModelPrefersNormalizedOverride(t *testing.T) {
	template, ok := provider.Resolve("zhipu")
	if !ok {
		t.Fatal("Resolve(zhipu) failed")
	}
	if got := template.Model(""); got != "GLM-5.3" {
		t.Fatalf("Model(\"\") = %q, want GLM-5.3", got)
	}
	if got := template.Model(" glm_5.3 "); got != "GLM-5.3" {
		t.Fatalf("Model(override) = %q, want GLM-5.3", got)
	}
	if got := template.Model("custom/model"); got != "custom/model" {
		t.Fatalf("Model(custom) = %q", got)
	}
}
