package agent

import (
	"strings"
	"testing"
)

func TestEditFormatGuidanceFamilyStrategies(t *testing.T) {
	cases := []struct {
		name    string
		model   string
		phrases []string
	}{
		{
			name:    "claude prefers hashline",
			model:   "claude-3-5-sonnet-20241022",
			phrases: []string{"hashline:true", "anchor_hash", "sha256", "12 hex"},
		},
		{
			name:    "anthropic alias prefers hashline",
			model:   "anthropic/claude-opus-4",
			phrases: []string{"hashline:true", "anchor_hash", "sha256", "12 hex"},
		},
		{
			name:    "gpt prefers edit plus repair",
			model:   "gpt-4o",
			phrases: []string{"old->new", "repair:true", "multi_edit"},
		},
		{
			name:    "openai alias prefers edit plus repair",
			model:   "openai/o3-mini",
			phrases: []string{"old->new", "repair:true"},
		},
		{
			name:    "o1 family prefers edit plus repair",
			model:   "o1-preview",
			phrases: []string{"repair:true"},
		},
		{
			name:    "grok prefers small hashline chunks",
			model:   "grok-2-1212",
			phrases: []string{"hashline", "small chunks", "anchor_hash"},
		},
		{
			name:    "gemini prefers small hashline chunks",
			model:   "gemini-2.0-flash",
			phrases: []string{"hashline", "small chunks"},
		},
		{
			name:    "qwen prefers small hashline chunks",
			model:   "qwen2.5-coder",
			phrases: []string{"hashline", "small chunks"},
		},
		{
			name:    "minimax prefers small hashline chunks",
			model:   "MiniMax-Text-01",
			phrases: []string{"hashline", "small chunks"},
		},
		{
			name:    "unknown model is generic",
			model:   "some-unlisted-model",
			phrases: []string{"Unknown model", "edit", "hashline"},
		},
		{
			name:    "empty model is generic",
			model:   "",
			phrases: []string{"Unknown model", "edit", "hashline"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EditFormatGuidance(tc.model)
			for _, phrase := range tc.phrases {
				if !strings.Contains(got, phrase) {
					t.Errorf("guidance for %q missing %q: %s", tc.model, phrase, got)
				}
			}
		})
	}
}

func TestEditFormatGuidanceIsCaseInsensitive(t *testing.T) {
	upper := EditFormatGuidance("CLAUDE-3-5-SONNET")
	lower := EditFormatGuidance("claude-3-5-sonnet")
	if upper != lower {
		t.Fatalf("model family detection must ignore case: %q vs %q", upper, lower)
	}
	if !strings.Contains(upper, "hashline:true") {
		t.Fatalf("uppercase Claude name should still prefer hashline: %s", upper)
	}
}

func TestEditFormatGuidanceAlwaysIncludesCommonRules(t *testing.T) {
	const (
		toolsRule = "All changes must go through tools (write/edit/multi_edit); never output code blocks as edits."
		readRule  = "read before edit to get exact anchor text."
	)
	for _, model := range []string{"claude-3", "gpt-4o", "grok-2", "unknown"} {
		got := EditFormatGuidance(model)
		if !strings.Contains(got, toolsRule) {
			t.Errorf("model %q: missing tools-only rule: %s", model, got)
		}
		if !strings.Contains(got, readRule) {
			t.Errorf("model %q: missing read-before-edit rule: %s", model, got)
		}
	}
}

func TestEditFormatGuidanceWithin600Bytes(t *testing.T) {
	for _, model := range []string{
		"claude-3-5-sonnet-20241022", "gpt-4o", "grok-2", "gemini-2.0-flash",
		"qwen2.5-coder", "MiniMax-Text-01", "unlisted-model", "",
	} {
		if n := len(EditFormatGuidance(model)); n > 600 {
			t.Errorf("model %q: guidance is %d bytes, want <=600", model, n)
		}
	}
}

func TestEditFormatGuidanceIncludesMarker(t *testing.T) {
	if got := EditFormatGuidance("gpt-4o"); !strings.Contains(got, editFormatMarker) {
		t.Fatalf("guidance is missing its idempotency marker: %s", got)
	}
}

func TestEditFormatGuidanceHasNoWireOrProtocolDetails(t *testing.T) {
	forbidden := []string{
		"http", "https", "bearer", "authorization", "api key", "api_key",
		"content-type", "chat/completions", "anthropic-version", "endpoint",
		"request body", "response body", "status code",
	}
	for _, model := range []string{"claude-3", "gpt-4o", "grok-2", "unknown"} {
		got := strings.ToLower(EditFormatGuidance(model))
		for _, term := range forbidden {
			if strings.Contains(got, term) {
				t.Errorf("model %q: guidance leaked wire/protocol term %q: %s", model, term, got)
			}
		}
	}
}

func TestAppendEditFormatAddsGuidanceOnce(t *testing.T) {
	const system = "You are a coding assistant."
	once := AppendEditFormat(system, "gpt-4o")
	if strings.Count(once, editFormatMarker) != 1 {
		t.Fatalf("expected exactly one marker after first append: %s", once)
	}
	twice := AppendEditFormat(once, "gpt-4o")
	if strings.Count(twice, editFormatMarker) != 1 {
		t.Fatalf("idempotency broken: marker appeared twice: %s", twice)
	}
	if twice != once {
		t.Fatalf("second append changed the prompt:\nfirst : %s\nsecond: %s", once, twice)
	}
}

func TestAppendEditFormatPreservesOriginalSystem(t *testing.T) {
	cases := []struct{ system, sep string }{
		{"base system", "\n\n"},
		{"base system\n", "\n"},
		{"base system\n\n", ""},
	}
	for _, tc := range cases {
		got := AppendEditFormat(tc.system, "claude-3")
		if !strings.HasPrefix(got, tc.system+tc.sep) {
			t.Errorf("original system %q was not preserved as a prefix of %q", tc.system, got)
		}
		if !strings.HasSuffix(got, commonEditFormat) {
			t.Errorf("guidance tail missing for system %q: %s", tc.system, got)
		}
	}
}

func TestAppendEditFormatKeepsExistingMarkerUnchanged(t *testing.T) {
	system := "custom prompt\n\n" + editFormatMarker + "\ncustom guidance"
	if got := AppendEditFormat(system, "gpt-4o"); got != system {
		t.Fatalf("prompt with an existing marker must be returned byte-identical:\ngot : %s\nwant: %s", got, system)
	}
}

func TestAppendEditFormatEmptySystemReturnsGuidance(t *testing.T) {
	got := AppendEditFormat("", "grok-2")
	if got != EditFormatGuidance("grok-2") {
		t.Fatalf("empty system should yield bare guidance: %s", got)
	}
	if !strings.Contains(got, editFormatMarker) {
		t.Fatalf("bare guidance must include the marker: %s", got)
	}
}

// ---- 模型系列配置测试 ----

// 下面四个字符串是配置化之前 strategyFor 的输出，原样复制到这里。固定它们能在某个系列被重新分类时
// 捕获任何意外的提示文本回归。
// （接上行）
const (
	legacyHintHashline = "Claude/Anthropic: prefer hashline edits (hashline:true). Set anchor to the exact text you first read and " +
		"anchor_hash to the first 12 hex chars of sha256(anchor bytes). Keep anchors small and unique."
	legacyHintEditRepair = "GPT/OpenAI: prefer edit with one unique old->new pair; on whitespace drift retry with repair:true. " +
		"Batch several changes with multi_edit."
	legacyHintSmallChunks = "Grok/Gemini/MiniMax/Qwen: prefer hashline edits in small chunks - read a short unique anchor, then edit " +
		"with hashline:true and anchor_hash (12-hex sha256). Split large rewrites."
	legacyHintUnknown = "Unknown model: both styles are available - edit (unique old->new, repair:true for whitespace drift) or " +
		"hashline (hashline:true + anchor_hash = 12-hex sha256 of the text you read). Read first; choose the best fit."
)

func TestDetectModelFamily(t *testing.T) {
	cases := []struct {
		model string
		want  ModelFamily
	}{
		{"claude-3-5-sonnet-20241022", FamilyAnthropic},
		{"anthropic/claude-opus-4", FamilyAnthropic},
		{"CLAUDE-3-5-SONNET", FamilyAnthropic},
		{"gpt-4o", FamilyOpenAI},
		{"openai/o3-mini", FamilyOpenAI},
		{"o1-preview", FamilyOpenAI},
		{"gemini-2.0-flash", FamilyGemini},
		{"Gemini-1.5-Pro", FamilyGemini},
		{"deepseek-chat", FamilyDeepseek},
		{"DeepSeek-R1", FamilyDeepseek},
		{"grok-2-1212", FamilyHashlineChunked},
		{"MiniMax-Text-01", FamilyHashlineChunked},
		{"qwen2.5-coder", FamilyHashlineChunked},
		{"some-unlisted-model", FamilyUnknown},
		{"mystery-7b", FamilyUnknown},
		{"", FamilyUnknown},
	}

	for _, tc := range cases {
		if got := DetectModelFamily(tc.model); got != tc.want {
			t.Errorf("DetectModelFamily(%q) = %q, want %q", tc.model, got, tc.want)
		}
	}
}

func TestEditFormatLegacyHintsAreByteStable(t *testing.T) {
	cases := []struct{ model, hint string }{
		{"claude-3-5-sonnet-20241022", legacyHintHashline},
		{"anthropic/claude-opus-4", legacyHintHashline},
		{"gpt-4o", legacyHintEditRepair},
		{"openai/o3-mini", legacyHintEditRepair},
		{"o1-preview", legacyHintEditRepair},
		{"gemini-2.0-flash", legacyHintSmallChunks},
		{"grok-2-1212", legacyHintSmallChunks},
		{"MiniMax-Text-01", legacyHintSmallChunks},
		{"qwen2.5-coder", legacyHintSmallChunks},
		{"some-unlisted-model", legacyHintUnknown},
		{"", legacyHintUnknown},
	}

	for _, tc := range cases {
		want := editFormatMarker + "\n" + tc.hint + "\n" + commonEditFormat
		if got := EditFormatGuidance(tc.model); got != want {
			t.Errorf("guidance for %q changed:\ngot : %s\nwant: %s", tc.model, got, want)
		}
	}
}

func TestUnknownModelFallsBackToLegacyGuidance(t *testing.T) {
	for _, model := range []string{"some-unlisted-model", "mystery-7b", ""} {
		if got := DetectModelFamily(model); got != FamilyUnknown {
			t.Errorf("DetectModelFamily(%q) = %q, want %q", model, got, FamilyUnknown)
		}
		if got := RecommendedEditTool(model); got != "" {
			t.Errorf("unknown model %q should have no tool preference, got %q", model, got)
		}
		if got := EditToolPreference(model); got != "" {
			t.Errorf("unknown model %q should have no advisory line, got %q", model, got)
		}
		if got := EditFormatGuidance(model); !strings.Contains(got, legacyHintUnknown) {
			t.Errorf("unknown model %q lost generic hint: %s", model, got)
		}
	}
}

func TestRecommendedEditToolIsStableAndAdvisoryOnly(t *testing.T) {
	cases := []struct{ model, tool string }{
		{"claude-3", "edit"},
		{"gpt-4o", "edit"},
		{"gemini-2.0-flash", "edit"},
		{"deepseek-chat", "edit"},
		{"grok-2", "edit"},
		{"unknown", ""},
	}
	for _, tc := range cases {
		if got := RecommendedEditTool(tc.model); got != tc.tool {
			t.Errorf("RecommendedEditTool(%q) = %q, want %q", tc.model, got, tc.tool)
		}
	}

	if got, want := EditToolPreference("claude-3"), "Preferred edit tool for this model: edit."; got != want {
		t.Errorf("EditToolPreference = %q, want %q", got, want)
	}

	// 建议性语句绝不能泄漏进系统提示：提示字节由 TestEditFormatLegacyHintsAreByteStable 固定。
	// （接上行）
	for _, model := range []string{"claude-3", "deepseek-chat", "gpt-4o"} {
		if got := EditFormatGuidance(model); strings.Contains(got, "Preferred edit tool") {
			t.Errorf("advisory line leaked into prompt for %q: %s", model, got)
		}
	}
}

func TestDeepseekFamilyGuidance(t *testing.T) {
	got := EditFormatGuidance("deepseek-chat")
	for _, phrase := range []string{"DeepSeek", "old->new", "repair:true", "multi_edit", commonEditFormat} {
		if !strings.Contains(got, phrase) {
			t.Errorf("deepseek guidance missing %q: %s", phrase, got)
		}
	}
	if n := len(got); n > 600 {
		t.Errorf("deepseek guidance is %d bytes, want <=600", n)
	}
}

func TestEditFormatGuidanceIsDeterministic(t *testing.T) {
	for _, model := range []string{
		"claude-3", "gpt-4o", "gemini-2.0-flash", "deepseek-chat", "grok-2", "unknown", "",
	} {
		first := EditFormatGuidance(model)
		for i := 0; i < 3; i++ {
			if got := EditFormatGuidance(model); got != first {
				t.Fatalf("guidance for %q not stable on call %d: %q vs %q", model, i, got, first)
			}
		}
	}
}
