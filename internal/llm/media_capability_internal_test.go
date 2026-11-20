package llm

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestMediaCapabilityNoteSupportedModels 确认已知支持图像的模型即便内容含图像
// 也不给出提示（避免误伤 capable 模型）。
func TestMediaCapabilityNoteSupportedModels(t *testing.T) {
	for _, model := range []string{
		"gpt-4o",
		"gpt-4o-mini",
		"gpt-4-vision-preview",
		"claude-3-5-sonnet-20241022",
		"gemini-1.5-pro",
		"qwen-vl-max",
		"llava-1.5",
	} {
		t.Run(model, func(t *testing.T) {
			if got := mediaCapabilityNote(model, true); got != "" {
				t.Errorf("mediaCapabilityNote(%q, true) = %q, want empty", model, got)
			}
		})
	}
}

// TestMediaCapabilityNoteUnsupportedModels 确认已知不支持图像的模型在内容含图像时
// 给出明确提示，且提示中按原样嵌入模型名。
func TestMediaCapabilityNoteUnsupportedModels(t *testing.T) {
	for _, model := range []string{
		"gpt-3.5-turbo",
		"o1-mini",
		"o3-mini",
		"deepseek-chat",
		"deepseek-reasoner",
		"text-embedding-3-large",
	} {
		t.Run(model, func(t *testing.T) {
			got := mediaCapabilityNote(model, true)
			want := fmt.Sprintf(mediaCapabilityNoteDefault, model)
			if got != want {
				t.Errorf("mediaCapabilityNote(%q, true) = %q, want %q", model, got, want)
			}
		})
	}
}

// TestMediaCapabilityNoteCJKModel 确认 CJK 模型名被原样、完整地嵌入提示，
// 结果仍是合法 UTF-8，且不会被从多字节字符中间截断。
func TestMediaCapabilityNoteCJKModel(t *testing.T) {
	models := []string{
		"文心一言",
		"百川-13B",
		"文心一言-for-测试-超长名称后缀以验证不会被截断",
	}
	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			got := mediaCapabilityNote(model, true)
			if !utf8.ValidString(got) {
				t.Fatalf("result is not valid UTF-8: %q", got)
			}
			want := fmt.Sprintf(mediaCapabilityNoteDefault, model)
			if got != want {
				t.Errorf("mediaCapabilityNote(%q, true) = %q, want %q", model, got, want)
			}
			if !strings.Contains(got, model) {
				t.Errorf("result %q lost the CJK model name %q", got, model)
			}
		})
	}
}

// TestMediaCapabilityNoteNoImage 确认内容不含图像时，无论模型为何都不给出提示。
func TestMediaCapabilityNoteNoImage(t *testing.T) {
	for _, model := range []string{
		"gpt-3.5-turbo",
		"o1-mini",
		"gpt-4o",
		"文心一言",
		"unknown-model",
		"",
	} {
		t.Run("model="+model, func(t *testing.T) {
			if got := mediaCapabilityNote(model, false); got != "" {
				t.Errorf("mediaCapabilityNote(%q, false) = %q, want empty", model, got)
			}
		})
	}
}

// TestMediaCapabilityNoteEmptyAndUnknown 确认空模型、纯空白模型以及能力未知的模型
// 在内容含图像时也保守地不给出提示，避免误伤未知/未来模型。
func TestMediaCapabilityNoteEmptyAndUnknown(t *testing.T) {
	for _, model := range []string{
		"",
		"   ",
		"\t\n  ",
		"completely-unknown-model",
		"未来新模型",
	} {
		t.Run("model="+strings.TrimSpace(model), func(t *testing.T) {
			if got := mediaCapabilityNote(model, true); got != "" {
				t.Errorf("mediaCapabilityNote(%q, true) = %q, want empty", model, got)
			}
		})
	}
}

// TestMediaCapabilityNoteCaseInsensitive 确认模型名匹配大小写不敏感，
// 且提示中嵌入的是去除首尾空白后的显示名。
func TestMediaCapabilityNoteCaseInsensitive(t *testing.T) {
	got := mediaCapabilityNote("  GPT-3.5-TURBO  ", true)
	want := fmt.Sprintf(mediaCapabilityNoteDefault, "GPT-3.5-TURBO")
	if got != want {
		t.Errorf("mediaCapabilityNote with mixed case = %q, want %q", got, want)
	}
}

// TestMediaCapabilityNoteVisionMarkerWins 确认同时命中视觉标记与文本标记时
// 以视觉能力为准，不给出提示（保守：有歧义时不误报）。
func TestMediaCapabilityNoteVisionMarkerWins(t *testing.T) {
	got := mediaCapabilityNote("gpt-3.5-vision", true)
	if got != "" {
		t.Errorf("mediaCapabilityNote(%q, true) = %q, want empty", "gpt-3.5-vision", got)
	}
}

// TestMediaCapabilityNoteDeterministic 确认函数是纯的：相同输入重复调用结果一致。
func TestMediaCapabilityNoteDeterministic(t *testing.T) {
	first := mediaCapabilityNote("deepseek-chat", true)
	for i := 0; i < 20; i++ {
		if got := mediaCapabilityNote("deepseek-chat", true); got != first {
			t.Fatalf("call %d = %q, want %q", i, got, first)
		}
	}
}
