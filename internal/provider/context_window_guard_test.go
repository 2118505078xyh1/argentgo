package provider_test

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/provider"
)

// 本文件是 R17 P2 窗口元数据改动的守卫测试：只依赖公开 API
// KnownContextWindow / ContextWindowFor，按真实签名编写。与同目录的
// context_windows_test.go 使用不同的测试名，避免重复符号。

// TestGuardKnownContextWindowModels 覆盖已知模型的窗口表：主流供应商、
// 大小写不敏感、供应商前缀剥离、unicode 分隔符归一化、未知模型回落 0。
func TestGuardKnownContextWindowModels(t *testing.T) {
	cases := []struct {
		name  string
		model string
		want  int
	}{
		// Claude 全系 200k。
		{"claude 基础", "claude-sonnet-5-5", 200_000},
		{"claude 大写", "CLAUDE-SONNET-5-5", 200_000},
		{"claude 混合大小写", "Claude-Sonnet-4.5", 200_000},
		{"claude 首尾空白", "  claude-sonnet-4.5  ", 200_000},
		{"claude 中文破折号", "claude——sonnet——4.5", 200_000},
		{"claude em dash", "claude—sonnet—4.5", 200_000},
		{"claude 下划线与空格", "claude_sonnet 4.5", 200_000},
		{"claude 单供应商前缀", "anthropic/claude-sonnet-4.5", 200_000},
		{"claude 多供应商前缀", "openrouter/anthropic/claude-sonnet-4.5", 200_000},
		// GPT-5 必须先于通用 GPT 规则匹配。
		{"gpt-5 小数版本", "gpt-5.6", 256_000},
		{"gpt-5 大写", "GPT-5", 256_000},
		{"gpt-5 带前缀", "openai/gpt-5.6", 256_000},
		{"gpt-4o", "gpt-4o", 128_000},
		{"gpt-4-turbo", "gpt-4-turbo", 128_000},
		{"gpt-3.5", "gpt-3.5-turbo", 128_000},
		// 其余供应商。
		{"gemini", "gemini-2.5-flash", 1_000_000},
		{"gemini 带前缀大写", "Google/GEMINI-2.5-Pro", 1_000_000},
		{"deepseek", "deepseek-v4-pro", 64_000},
		{"deepseek 别名前缀", "local/deepseek-v4.1-flash", 64_000},
		{"kimi", "kimi-k3.1-pro", 128_000},
		{"moonshot", "moonshot/kimi-k3.1-pro", 128_000},
		{"grok", "grok-4.7", 128_000},
		{"glm 大写", "GLM-5.3", 128_000},
		{"qwen", "qwen3.8-max", 128_000},
		{"minimax 混合大小写", "MiniMax-M3", 200_000},
		// R52 研究 #46：Llama / Mistral 系已收录，必须命中非零窗口。
		{"llama", "llama3.2", 131_072},
		{"mistral", "mistral-large-latest", 128_000},
		// 未知一律 0，包含空白、空串、前缀悬空与未收录供应商。
		{"local-model", "local-model", 0},
		{"空串", "", 0},
		{"纯空白", "   ", 0},
		{"仅分隔符", "---", 0},
		{"悬空斜杠", "claude/", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := provider.KnownContextWindow(tc.model); got != tc.want {
				t.Errorf("KnownContextWindow(%q) = %d, want %d", tc.model, got, tc.want)
			}
		})
	}
}

// TestGuardKnownContextWindowCJKSafety 验证 CJK/emoji 按 UTF-8 原样处理：
// 既不会因字节截断误命中 ASCII 前缀，也不会妨碍剥离前缀后的已知模型识别。
func TestGuardKnownContextWindowCJKSafety(t *testing.T) {
	cases := []struct {
		model string
		want  int
	}{
		{"中文模型", 0},
		{"模型-claude", 0},
		{"claude-模型", 200_000}, // 已知前缀开头，尾部 CJK 不影响命中。
		{"🤖-gemini", 0},
		{"供应商/模型", 0},
		{"模型/gpt-5.6", 256_000}, // CJK 作为供应商前缀被剥离。
		{"anthropic/中文-claude", 0},
	}
	for _, tc := range cases {
		if got := provider.KnownContextWindow(tc.model); got != tc.want {
			t.Errorf("KnownContextWindow(%q) = %d, want %d", tc.model, got, tc.want)
		}
	}
}

// TestGuardContextWindowForFallbackChain 验证回落链优先级：
// configured > 0 → 模型元数据 → 模板默认模型 → 0。
func TestGuardContextWindowForFallbackChain(t *testing.T) {
	cases := []struct {
		name                string
		templateName, model string
		configured          int
		want                int
	}{
		// configured > 0 永远优先，即使模型与模板都已知。
		{"configured 压过已知模型与模板", "anthropic", "claude-sonnet-5-5", 999, 999},
		{"configured 压过未知模板", "bogus", "bogus", 456_789, 456_789},
		{"configured 压过 CJK 模型", "bogus", "中文模型", 32_000, 32_000},
		// 非正数 configured 视为未配置，继续回落。
		{"负数 configured 回落模型", "bogus", "claude-sonnet-4.5", -1, 200_000},
		{"零 configured 回落模型", "ollama", "gpt-5.6", 0, 256_000},
		// 模型优先级高于模板默认：openai 默认 256k，但模型 claude 为 200k。
		{"模型压过模板默认", "openai", "claude-sonnet-4.5", 0, 200_000},
		{"已知模型压过未知模板", "bogus", "gemini-2.5-flash", 0, 1_000_000},
		// 模型未知时回落到模板默认模型。
		{"空模型回落 openai 默认", "openai", "", 0, 256_000},
		{"空模型回落 anthropic 默认", "anthropic", "", 0, 200_000},
		{"空模型回落 deepseek 默认", "deepseek", "", 0, 64_000},
		{"空模型回落 gemini 默认", "gemini", "", 0, 1_000_000},
		{"空模型回落 openrouter 默认", "openrouter", "", 0, 200_000},
		// ollama 默认 llama3.2 已命中窗口表；lmstudio 的 local-model 仍未知返回 0。
		{"ollama 默认模型命中 llama", "ollama", "", 0, 131_072},
		{"模板默认模型未知返回零", "lmstudio", "", 0, 0},
		{"模型未收录且模板未知返回零", "bogus", "local-model", 0, 0},
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

// TestGuardContextWindowForTemplateAliases 验证模板别名与大小写不敏感：
// 别名经 Resolve 解析后取其规范模板的默认模型窗口。
func TestGuardContextWindowForTemplateAliases(t *testing.T) {
	cases := []struct {
		name, templateName string
		want               int
	}{
		{"别名 claude → anthropic", "claude", 200_000},
		{"别名 kimi → moonshot", "kimi", 128_000},
		{"别名 google → gemini", "google", 1_000_000},
		{"别名 grok → xai", "grok", 128_000},
		{"别名 glm → zhipu", "glm", 128_000},
		{"别名 dashscope → qwen", "dashscope", 128_000},
		{"规范名大写", "ANTHROPIC", 200_000},
		{"规范名混合大小写", "OpenAI", 256_000},
		{"别名大写", "CLAUDE", 200_000},
		{"模板名首尾空白", "  gemini  ", 1_000_000},
		// CJK 模板名不是已知别名，回落 0。
		{"CJK 模板名未知", "中文模板", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := provider.ContextWindowFor(tc.templateName, "", 0); got != tc.want {
				t.Errorf("ContextWindowFor(%q, \"\", 0) = %d, want %d", tc.templateName, got, tc.want)
			}
		})
	}
}

// TestGuardContextWindowPureStableAndNonMutating 验证两个函数是纯函数：
// 不修改任何入参、无隐藏状态、重复调用结果稳定、可并发安全读取。
func TestGuardContextWindowPureStableAndNonMutating(t *testing.T) {
	templateName, model, configured := "openai", "gpt-5.6", 0
	want := provider.ContextWindowFor(templateName, model, configured)

	for i := 0; i < 100; i++ {
		if got := provider.ContextWindowFor(templateName, model, configured); got != want {
			t.Fatalf("ContextWindowFor call %d = %d, want stable %d", i, got, want)
		}
	}
	if templateName != "openai" || model != "gpt-5.6" || configured != 0 {
		t.Fatalf("ContextWindowFor inputs mutated: %q %q %d", templateName, model, configured)
	}

	for i := 0; i < 100; i++ {
		if got := provider.KnownContextWindow(model); got != 256_000 {
			t.Fatalf("KnownContextWindow call %d = %d, want stable 256000", i, got)
		}
	}
	if model != "gpt-5.6" {
		t.Fatalf("KnownContextWindow input mutated: %q", model)
	}
}
