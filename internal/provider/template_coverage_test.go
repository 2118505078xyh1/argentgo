package provider_test

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/provider"
)

// TestTemplateCoverageAllTwenty 锁定全部 20 个内置模板的关键字段，
// 防止误改 xai 协议 / bigmodel 凭据来源等脆弱点而不被测试发现。
func TestTemplateCoverageAllTwenty(t *testing.T) {
	want := map[string]struct {
		protocol, baseURL, apiKeyEnv, model string
		cache                               bool
	}{
		"anthropic":  {string(provider.ProtocolAnthropic), "https://api.anthropic.com/v1", "ANTHROPIC_API_KEY", "claude-sonnet-5-5", true},
		"openai":     {string(provider.ProtocolOpenAI), "https://api.openai.com/v1", "OPENAI_API_KEY", "gpt-5.6", true},
		"deepseek":   {string(provider.ProtocolAnthropic), "https://api.deepseek.com/anthropic", "DEEPSEEK_API_KEY", "deepseek-v4-pro", false},
		"moonshot":   {string(provider.ProtocolAnthropic), "https://api.moonshot.cn/anthropic", "MOONSHOT_API_KEY", "kimi-k3.1-pro", false},
		"zhipu":      {string(provider.ProtocolAnthropic), "https://open.bigmodel.cn/api/anthropic", "ZHIPU_API_KEY", "GLM-5.3", false},
		"zai":        {string(provider.ProtocolAnthropic), "https://api.z.ai/api/anthropic", "ZAI_API_KEY", "GLM-5.3", false},
		"bigmodel":   {string(provider.ProtocolAnthropic), "https://open.bigmodel.cn/api/anthropic", "BIGMODEL_API_KEY", "GLM-5.3", false},
		"qwen":       {string(provider.ProtocolOpenAI), "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", "DASHSCOPE_API_KEY", "qwen3.8-max", false},
		"openrouter": {string(provider.ProtocolOpenAI), "https://openrouter.ai/api/v1", "OPENROUTER_API_KEY", "anthropic/claude-sonnet-4.5", false},
		"ollama":     {string(provider.ProtocolOpenAI), "http://127.0.0.1:11434/v1", "", "llama3.2", false},
		"lmstudio":   {string(provider.ProtocolOpenAI), "http://127.0.0.1:1234/v1", "", "local-model", false},
		"local":      {string(provider.ProtocolOpenAI), "http://127.0.0.1:3425/v1", "", "auto/deepseek-v4.1-flash", false},
		"xai":        {string(provider.ProtocolResponses), "https://api.x.ai/v1", "XAI_API_KEY", "grok-4.7", true},
		"minimax":    {string(provider.ProtocolAnthropic), "https://api.minimaxi.com/anthropic", "MINIMAX_API_KEY", "MiniMax-M3", false},
		"gemini":     {string(provider.ProtocolOpenAI), "https://generativelanguage.googleapis.com/v1beta/openai", "GEMINI_API_KEY", "gemini-2.5-flash", false},
		"groq":       {string(provider.ProtocolOpenAI), "https://api.groq.com/openai/v1", "GROQ_API_KEY", "llama-3.3-70b-versatile", false},
		"mistral":    {string(provider.ProtocolOpenAI), "https://api.mistral.ai/v1", "MISTRAL_API_KEY", "mistral-large-latest", false},
		"together":   {string(provider.ProtocolOpenAI), "https://api.together.xyz/v1", "TOGETHER_API_KEY", "meta-llama/Llama-3.3-70B", false},
		"fireworks":  {string(provider.ProtocolOpenAI), "https://api.fireworks.ai/inference/v1", "FIREWORKS_API_KEY", "accounts/fireworks/models/llama-v3p3-70b", false},
		"cerebras":   {string(provider.ProtocolOpenAI), "https://api.cerebras.ai/v1", "CEREBRAS_API_KEY", "llama-3.3-70b", false},
	}
	all := provider.List()
	if len(all) != len(want) {
		t.Fatalf("template count = %d, want %d", len(all), len(want))
	}
	for _, tmpl := range all {
		w, ok := want[tmpl.Name]
		if !ok {
			t.Fatalf("unexpected template %q", tmpl.Name)
		}
		if string(tmpl.Protocol) != w.protocol || tmpl.BaseURL != w.baseURL ||
			tmpl.APIKeyEnv != w.apiKeyEnv || tmpl.DefaultModel != w.model ||
			tmpl.CacheSupport != w.cache {
			t.Errorf("template %q = %+v, want %+v", tmpl.Name, tmpl, w)
		}
	}
}

// TestAllAliasesResolve 遍历全部别名，断言规范名与 BaseURL 非空。
func TestAllAliasesResolve(t *testing.T) {
	aliases := []string{
		"claude", "kimi", "dashscope", "aliyun", "z.ai", "glm", "bigmodel.cn",
		"lm-studio", "lm_studio", "open-router", "grok", "google",
	}
	for _, alias := range aliases {
		res, ok := provider.Resolve(alias)
		if !ok || res.Name == "" || res.BaseURL == "" {
			t.Errorf("alias %q resolve = %+v, %v", alias, res, ok)
		}
	}
}

// TestBigmodelAndZhipuCredentialDifference 锁定 bigmodel 与 zhipu 同 BaseURL
// 但凭据来源不同的事实，防止被误"修复"成同一个。
func TestBigmodelAndZhipuCredentialDifference(t *testing.T) {
	var bigmodel, zhipu provider.Template
	for _, tmpl := range provider.List() {
		if tmpl.Name == "bigmodel" {
			bigmodel = tmpl
		}
		if tmpl.Name == "zhipu" {
			zhipu = tmpl
		}
	}
	if bigmodel.APIKeyEnv == zhipu.APIKeyEnv {
		t.Fatalf("bigmodel and zhipu must use different key envs, both %q", bigmodel.APIKeyEnv)
	}
	if bigmodel.BaseURL != zhipu.BaseURL {
		t.Fatalf("bigmodel and zhipu should share BaseURL: %q vs %q", bigmodel.BaseURL, zhipu.BaseURL)
	}
}
