package provider_test

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/provider"
)

// TestProfileForKnownModels 覆盖已知 provider + 已知模型的完整画像：
// 规范名、思考、图片、工具与上下文窗口四项同时校验。
func TestProfileForKnownModels(t *testing.T) {
	cases := []struct {
		name, template, model string
		want                  provider.CapabilityProfile
	}{
		{
			name: "claude vision", template: "anthropic", model: "claude-3-opus",
			want: provider.CapabilityProfile{
				Name: "anthropic", SupportsThinking: true, SupportsImage: true,
				SupportsTools: true, ContextWindow: 200_000,
			},
		},
		{
			name: "gpt-4o", template: "openai", model: "gpt-4o",
			want: provider.CapabilityProfile{
				Name: "openai", SupportsThinking: true, SupportsImage: true,
				SupportsTools: true, ContextWindow: 128_000,
			},
		},
		{
			name: "gemini", template: "gemini", model: "gemini-2.5-flash",
			want: provider.CapabilityProfile{
				Name: "gemini", SupportsThinking: true, SupportsImage: true,
				SupportsTools: true, ContextWindow: 1_000_000,
			},
		},
		{
			name: "deepseek text only", template: "deepseek", model: "deepseek-chat",
			want: provider.CapabilityProfile{
				Name: "deepseek", SupportsThinking: true, SupportsImage: false,
				SupportsTools: true, ContextWindow: 64_000,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := provider.ProfileFor(tc.template, tc.model)
			if got != tc.want {
				t.Errorf("ProfileFor(%q, %q) = %+v, want %+v", tc.template, tc.model, got, tc.want)
			}
		})
	}
}

// TestProfileForUnknownDefaults 验证未知 provider/模型的保守默认：
// tools=true，其余为 false，窗口来自 KnownContextWindow 或 0。
func TestProfileForUnknownDefaults(t *testing.T) {
	cases := []struct {
		template, model string
		want            provider.CapabilityProfile
	}{
		{
			template: "no-such-provider", model: "mystery-model",
			want: provider.CapabilityProfile{
				Name: "no-such-provider", SupportsTools: true,
			},
		},
		{
			template: "", model: "",
			want: provider.CapabilityProfile{SupportsTools: true},
		},
		{
			// 模板未知但模型窗口已知：window 仍来自 KnownContextWindow。
			template: "unknown-name", model: "deepseek-chat",
			want: provider.CapabilityProfile{
				Name: "unknown-name", SupportsTools: true, ContextWindow: 64_000,
			},
		},
	}
	for _, tc := range cases {
		got := provider.ProfileFor(tc.template, tc.model)
		if got != tc.want {
			t.Errorf("ProfileFor(%q, %q) = %+v, want %+v", tc.template, tc.model, got, tc.want)
		}
	}
}

// TestProfileForThinkingSupport 验证思考能力按协议判定：
// 具备推理控制的协议为 true，未知模板为 false。
func TestProfileForThinkingSupport(t *testing.T) {
	for _, template := range []string{"anthropic", "openai", "xai", "deepseek", "CLAUDE", " kimi "} {
		if got := provider.ProfileFor(template, "mystery-model"); !got.SupportsThinking {
			t.Errorf("ProfileFor(%q, ...) SupportsThinking = false, want true", template)
		}
	}
	for _, template := range []string{"", "unknown-provider"} {
		if got := provider.ProfileFor(template, "mystery-model"); got.SupportsThinking {
			t.Errorf("ProfileFor(%q, ...) SupportsThinking = true, want false", template)
		}
	}
}

// TestProfileForContextWindow 覆盖上下文窗口的回落链：
// 模型命中、gpt-5 特殊规则、模板默认模型回落、模型未命中模板未知。
func TestProfileForContextWindow(t *testing.T) {
	cases := []struct {
		template, model string
		want            int
	}{
		{"anthropic", "claude-3-opus", 200_000},
		{"openai", "gpt-5.6", 256_000},
		{"openai", "gpt-4o", 128_000},
		{"gemini", "gemini-2.5-flash", 1_000_000},
		// 模型为空时回落到模板默认模型 claude-sonnet-5-5。
		{"anthropic", "", 200_000},
		// 未知模板但模型名已知。
		{"unknown", "deepseek-chat", 64_000},
		// 两者都未知。
		{"unknown", "mystery-model", 0},
	}
	for _, tc := range cases {
		if got := provider.ProfileFor(tc.template, tc.model).ContextWindow; got != tc.want {
			t.Errorf("ProfileFor(%q, %q).ContextWindow = %d, want %d", tc.template, tc.model, got, tc.want)
		}
	}
}

// TestProfileForCJK 验证中文 provider 名与中文模型名被原样保留：
// Name 不截断，中文视觉标记命中，且不会破坏多字节字符。
func TestProfileForCJK(t *testing.T) {
	const name = "文心一言"
	const model = "文心一言-多模态"
	got := provider.ProfileFor(name, model)
	want := provider.CapabilityProfile{
		Name: name, SupportsImage: true, SupportsTools: true,
	}
	if got != want {
		t.Fatalf("ProfileFor(CJK) = %+v, want %+v", got, want)
	}
	if got.Name != name {
		t.Errorf("Name = %q, want exact %q", got.Name, name)
	}
}

// TestProfileForDoesNotMutateInputs 验证调用不改入参（字符串不可变），
// 且使用别名/空白模板名时同样归一而不污染调用方变量。
func TestProfileForDoesNotMutateInputs(t *testing.T) {
	template := "  CLAUDE  "
	model := "  claude-3-opus  "
	templateBefore, modelBefore := template, model
	first := provider.ProfileFor(template, model)
	if template != templateBefore || model != modelBefore {
		t.Fatalf("inputs mutated: template=%q model=%q", template, model)
	}
	if first.Name != "anthropic" {
		t.Errorf("alias name = %q, want anthropic", first.Name)
	}
}

// TestProfileForDeterministic 验证相同入参得到完全相同画像，重复调用稳定。
func TestProfileForDeterministic(t *testing.T) {
	cases := [][2]string{
		{"anthropic", "claude-3-opus"},
		{"openai", "gpt-4o"},
		{"文心一言", "文心一言-多模态"},
		{"unknown", "mystery-model"},
	}
	for _, tc := range cases {
		first := provider.ProfileFor(tc[0], tc[1])
		for i := 0; i < 3; i++ {
			if got := provider.ProfileFor(tc[0], tc[1]); got != first {
				t.Fatalf("ProfileFor(%q, %q) call %d = %+v, want stable %+v", tc[0], tc[1], i, got, first)
			}
		}
	}
}
