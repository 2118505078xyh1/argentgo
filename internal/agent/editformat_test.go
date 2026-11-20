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
