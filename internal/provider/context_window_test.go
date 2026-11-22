package provider_test

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/provider"
)

func TestKnownContextWindowByModel(t *testing.T) {
	// 覆盖主流供应商、大小写、首尾空白、unicode 破折号与供应商前缀。
	cases := []struct {
		model string
		want  int
	}{
		// Claude 全系 200k。
		{"claude-sonnet-5-5", 200_000},
		{"CLAUDE-SONNET-5-5", 200_000},
		{"  claude-sonnet-4.5  ", 200_000},
		{"claude—sonnet—4.5", 200_000}, // em dash 归一化为 '-'
		{"anthropic/claude-sonnet-4.5", 200_000},
		{"openrouter/anthropic/claude-sonnet-4.5", 200_000},
		// GPT-5 系列 256k，其余 GPT 128k。
		{"gpt-5.6", 256_000},
		{"GPT-5", 256_000},
		{"openai/gpt-5.6", 256_000},
		{"gpt-4o", 128_000},
		{"gpt-4-turbo", 128_000},
		{"gpt-3.5-turbo", 128_000},
		// 其他供应商。
		{"gemini-2.5-flash", 1_000_000},
		{"google/gemini-2.5-pro", 1_000_000},
		{"deepseek-v4-pro", 64_000},
		{"kimi-k3.1-pro", 128_000},
		{"moonshot/kimi-k3.1-pro", 128_000},
		{"grok-4.7", 128_000},
		{"GLM-5.3", 128_000},
		{"qwen3.8-max", 128_000},
		{"MiniMax-M3", 200_000},
		// R52 研究 #46：Llama / Mistral 系必须命中，否则回落 32000 而过早压缩。
		{"llama3.2", 131_072},
		{"ollama/llama3.2", 131_072},
		{"llama-3.3-70b-versatile", 131_072},
		{"meta-llama/Llama-3.3-70B", 131_072},
		{"accounts/fireworks/models/llama-v3p3-70b", 131_072},
		{"mistral-large-latest", 128_000},
		{"MISTRAL-SMALL-LATEST", 128_000},
		// 未知模型一律返回 0。
		{"local-model", 0},
		{"", 0},
		{"   ", 0},
		{"claude/", 0},
	}
	for _, tc := range cases {
		if got := provider.KnownContextWindow(tc.model); got != tc.want {
			t.Errorf("KnownContextWindow(%q) = %d, want %d", tc.model, got, tc.want)
		}
	}
}

func TestKnownContextWindowIsCJKSafe(t *testing.T) {
	// CJK 字符按 UTF-8 原样处理：不会因字节截断而误命中 ASCII 前缀。
	for _, model := range []string{"中文模型", "模型-claude", "🤖-gemini"} {
		if got := provider.KnownContextWindow(model); got != 0 {
			t.Errorf("KnownContextWindow(%q) = %d, want 0 for CJK model", model, got)
		}
	}
	// CJK 作为供应商前缀被剥离后，仍能正确识别后面的已知模型。
	if got := provider.KnownContextWindow("模型/gpt-5.6"); got != 256_000 {
		t.Errorf("KnownContextWindow(模型/gpt-5.6) = %d, want 256000", got)
	}
}

func TestContextWindowForFallbackChain(t *testing.T) {
	cases := []struct {
		name                string
		templateName, model string
		configured          int
		want                int
	}{
		{"configured 优先于已知模型", "anthropic", "claude-sonnet-5-5", 999, 999},
		{"configured 对未知模板也优先", "bogus", "bogus", 456_789, 456_789},
		{"configured 非正数视为未配置并回落模型", "bogus", "claude-sonnet-4.5", -1, 200_000},
		{"configured 为零回落模型", "ollama", "gpt-5.6", 0, 256_000},
		{"模型未知回落模板默认", "openai", "", 0, 256_000},
		{"模板默认 Claude", "anthropic", "", 0, 200_000},
		{"模板别名 claude", "claude", "", 0, 200_000},
		{"模板别名 kimi", "kimi", "", 0, 128_000},
		{"模板默认 Gemini", "gemini", "", 0, 1_000_000},
		{"模板默认 DeepSeek", "deepseek", "", 0, 64_000},
		{"未知模板但模型已知", "bogus", "gemini-2.5-flash", 0, 1_000_000},
		{"llama 模型命中窗口表", "ollama", "llama3.2", 0, 131_072},
		{"mistral 模型命中窗口表", "bogus", "mistral-large-latest", 0, 128_000},
		{"未知模板与空模型返回零", "bogus", "", 0, 0},
		{"空模板与空模型返回零", "", "", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := provider.ContextWindowFor(tc.templateName, tc.model, tc.configured); got != tc.want {
				t.Errorf("ContextWindowFor(%q, %q, %d) = %d, want %d",
					tc.templateName, tc.model, tc.configured, got, tc.want)
			}
		})
	}
}

func TestContextWindowForPureStableAndNonMutating(t *testing.T) {
	templateName, model, configured := "openai", "gpt-5.6", 0
	want := provider.ContextWindowFor(templateName, model, configured)

	// 字符串是值类型，调用不可能改写入参；重复调用结果必须一致（无隐藏状态）。
	for i := 0; i < 100; i++ {
		if got := provider.ContextWindowFor(templateName, model, configured); got != want {
			t.Fatalf("call %d = %d, want stable %d", i, got, want)
		}
	}
	if templateName != "openai" || model != "gpt-5.6" || configured != 0 {
		t.Fatalf("inputs mutated: %q %q %d", templateName, model, configured)
	}

	for i := 0; i < 100; i++ {
		if got := provider.KnownContextWindow(model); got != 256_000 {
			t.Fatalf("call %d = %d, want stable 256000", i, got)
		}
	}
	if model != "gpt-5.6" {
		t.Fatalf("model mutated: %q", model)
	}
}

// TestAllTemplateDefaultModelsHaveKnownWindow 是 R52 研究 #46 的守护测试：
// 遍历全部内置模板（当前 20 个），除 lmstudio 的 local-model（本地模型，窗口无法预知）外，
// 每个默认模型都必须解析出非零窗口。否则 ContextWindowFor 返回 0，app 会回落到
// 编译默认 32000 而过早压缩、丢失上下文。
func TestAllTemplateDefaultModelsHaveKnownWindow(t *testing.T) {
	templates := provider.List()
	if len(templates) == 0 {
		t.Fatal("provider.List() 为空，无法守护默认模型窗口")
	}
	for _, tpl := range templates {
		if tpl.DefaultModel == "local-model" {
			continue // 本地模型由用户环境决定，无法预知窗口。
		}
		if got := provider.ContextWindowFor(tpl.Name, tpl.DefaultModel, 0); got == 0 {
			t.Errorf("模板 %q 的默认模型 %q 解析窗口为 0（会让 app 回落到 32000 而过早压缩丢上下文）",
				tpl.Name, tpl.DefaultModel)
		}
	}
}
