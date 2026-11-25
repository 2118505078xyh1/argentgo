package provider

import (
	"testing"

	"example.com/argentgo/internal/llm"
)

func TestProfileVisionParity(t *testing.T) {
	models := []string{
		"gpt-4o", "gpt-4o-mini", "claude-3-opus", "claude-4-sonnet",
		"gemini-2.0-flash", "qwen2.5-vl-72b", "llava", "文心一言-多模态",
		"gpt-3.5-turbo", "unknown-model", "", "GPT-4O",
	}
	for _, model := range models {
		got := supportsImage(model)
		want := llm.VisionMarkerFor(model)
		if got != want {
			t.Errorf("supportsImage(%q) = %v, llm.VisionMarkerFor = %v", model, got, want)
		}
	}
}

func TestProfileForExposesCapabilities(t *testing.T) {
	p := ProfileFor("openai", "gpt-4o")
	if !p.SupportsTools || !p.SupportsImage || p.ContextWindow <= 0 {
		t.Fatalf("profile = %+v, want tools/image/window set", p)
	}
	if p.Name != "openai" {
		t.Fatalf("profile name = %q", p.Name)
	}
}
