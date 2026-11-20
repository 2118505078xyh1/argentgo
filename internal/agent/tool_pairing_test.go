package agent

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func pairingCall(id, name string) llm.ToolCall {
	return llm.ToolCall{
		ID:       id,
		Type:     "function",
		Function: llm.ToolCallFunction{Name: name, Arguments: "{}"},
	}
}

func pairingAssistant(calls ...llm.ToolCall) llm.Message {
	return llm.Message{Role: "assistant", ToolCalls: calls}
}

func pairingTool(id, content string) llm.Message {
	return llm.Message{Role: "tool", ToolCallID: id, Content: content}
}

// sameBackingSlice reports whether two slices share their first element, i.e.
// NormalizeToolPairing returned the input slice unchanged.
func sameBackingSlice(a, b []llm.Message) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	return &a[0] == &b[0]
}

func cloneMessages(messages []llm.Message) []llm.Message {
	out := make([]llm.Message, len(messages))
	copy(out, messages)
	return out
}

// ① and ⑤: a fully paired transcript is returned by reference and untouched.
func TestNormalizeToolPairingCompleteBatchReturnsSameSlice(t *testing.T) {
	input := []llm.Message{
		{Role: "system", Content: "be brief"},
		{Role: "user", Content: "read the file"},
		pairingAssistant(pairingCall("c1", "read_file")),
		pairingTool("c1", "file contents"),
		{Role: "assistant", Content: "done"},
	}
	original := cloneMessages(input)

	got := NormalizeToolPairing(input)

	if !sameBackingSlice(got, input) {
		t.Fatal("fully paired transcript should return the input slice by reference")
	}
	if len(got) != len(input) {
		t.Fatalf("len = %d, want %d", len(got), len(input))
	}
	for i := range input {
		if got[i].Role != input[i].Role || got[i].Content != input[i].Content {
			t.Fatalf("message %d changed: %+v -> %+v", i, original[i], got[i])
		}
	}
}

// ②: an orphan call followed by a user boundary gets an error result with the
// call's name folded into the text, and the input is not mutated.
func TestNormalizeToolPairingFillsOrphanBeforeUserBoundary(t *testing.T) {
	input := []llm.Message{
		{Role: "user", Content: "first"},
		pairingAssistant(pairingCall("c1", "read_file")),
		{Role: "user", Content: "next question"},
	}
	original := cloneMessages(input)

	got := NormalizeToolPairing(input)

	if len(got) != len(input)+1 {
		t.Fatalf("len = %d, want %d", len(got), len(input)+1)
	}
	if got[0].Content != "first" || got[1].Role != "assistant" {
		t.Fatalf("prefix changed: %+v, %+v", got[0], got[1])
	}
	result := got[2]
	if result.Role != "tool" || result.ToolCallID != "c1" {
		t.Fatalf("synthetic result = %+v, want tool/c1", result)
	}
	if !strings.HasPrefix(result.Content, "error: ") {
		t.Fatalf("result content %q has no error prefix", result.Content)
	}
	if !strings.Contains(result.Content, missingToolResultText) {
		t.Fatalf("result content %q lacks %q", result.Content, missingToolResultText)
	}
	if !strings.Contains(result.Content, "read_file") {
		t.Fatalf("result content %q lacks the call name", result.Content)
	}
	if got[3].Role != "user" || got[3].Content != "next question" {
		t.Fatalf("boundary message not preserved after result: %+v", got[3])
	}
	if len(input) != len(original) || input[2].Role != original[2].Role {
		t.Fatal("input slice was mutated")
	}
}

// ② (tail variant): an orphan call at the end of the transcript is answered.
func TestNormalizeToolPairingFillsOrphanAtEnd(t *testing.T) {
	input := []llm.Message{
		{Role: "user", Content: "go"},
		pairingAssistant(pairingCall("c9", "shell")),
	}

	got := NormalizeToolPairing(input)

	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[2].Role != "tool" || got[2].ToolCallID != "c9" {
		t.Fatalf("tail result = %+v, want tool/c9", got[2])
	}
}

// ③: multiple assistant/user boundaries, multiple calls per batch, and a
// mixture of real and orphan results keep their order and get paired.
func TestNormalizeToolPairingAcrossMultipleBoundaries(t *testing.T) {
	input := []llm.Message{
		{Role: "user", Content: "q1"},
		pairingAssistant(pairingCall("c1", "a"), pairingCall("c2", "b")),
		pairingTool("c1", "ok1"),
		{Role: "user", Content: "q2"}, // c2 is orphaned here
		pairingAssistant(pairingCall("c3", "c")),
		pairingTool("c3", "ok3"),
		{Role: "user", Content: "q3"},
		pairingAssistant(pairingCall("c4", "d")), // orphan at the tail
	}

	got := NormalizeToolPairing(input)

	// Walk the result and record the ordering of tool results vs boundaries.
	var layout []string
	for _, m := range got {
		switch {
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			layout = append(layout, "assistant:"+m.ToolCalls[0].ID)
		case m.Role == "tool":
			layout = append(layout, "tool:"+m.ToolCallID)
		case m.Role == "user":
			layout = append(layout, "user:"+m.Content)
		}
	}
	want := []string{
		"user:q1",
		"assistant:c1",
		"tool:c1", // real result first
		"tool:c2", // synthesized before the q2 boundary
		"user:q2",
		"assistant:c3",
		"tool:c3", // real result
		"user:q3",
		"assistant:c4",
		"tool:c4", // synthesized at the tail
	}
	if strings.Join(layout, ",") != strings.Join(want, ",") {
		t.Fatalf("layout = %v, want %v", layout, want)
	}

	// The synthesized c2 result must mention its tool name.
	for _, m := range got {
		if m.Role == "tool" && m.ToolCallID == "c2" {
			if !strings.Contains(m.Content, "\"b\"") {
				t.Fatalf("c2 result %q lacks name backfill", m.Content)
			}
		}
	}
}

// ④: empty/whitespace-only tool output is normalized to a placeholder, both
// for existing results and for an orphan call with no name.
func TestNormalizeToolPairingNormalizesEmptyOutput(t *testing.T) {
	input := []llm.Message{
		pairingAssistant(pairingCall("c1", "read_file")),
		pairingTool("c1", "   \t\n "), // existing empty result
		pairingAssistant(pairingCall("c2", "")),
	}

	got := NormalizeToolPairing(input)

	if len(got) != 4 {
		t.Fatalf("len = %d, want 4", len(got))
	}
	if got[1].Content != emptyToolResultText {
		t.Fatalf("empty output = %q, want %q", got[1].Content, emptyToolResultText)
	}
	if got[3].Role != "tool" || got[3].ToolCallID != "c2" {
		t.Fatalf("tail result = %+v, want tool/c2", got[3])
	}
	if !strings.Contains(got[3].Content, unknownToolName) {
		t.Fatalf("synthetic result %q lacks name placeholder", got[3].Content)
	}
	if !strings.Contains(got[3].Content, missingToolResultText) {
		t.Fatalf("synthetic result %q lacks missing-result text", got[3].Content)
	}
}

// ⑥: UTF-8 / Chinese tool names and result content survive normalization
// byte-for-byte, and a fully paired Chinese transcript is returned by ref.
func TestNormalizeToolPairingUTF8Safe(t *testing.T) {
	paired := []llm.Message{
		pairingAssistant(pairingCall("工具-1", "读取文件")),
		pairingTool("工具-1", "结果：你好，世界 🌍"),
	}
	if got := NormalizeToolPairing(paired); !sameBackingSlice(got, paired) {
		t.Fatal("paired UTF-8 transcript should be returned by reference")
	}
	if paired[1].Content != "结果：你好，世界 🌍" {
		t.Fatal("paired UTF-8 content was altered")
	}

	orphan := []llm.Message{
		pairingAssistant(pairingCall("工具-2", "写入文件")),
		{Role: "user", Content: "继续"},
	}
	got := NormalizeToolPairing(orphan)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	content := got[1].Content
	if !utf8.ValidString(content) {
		t.Fatalf("synthetic content is not valid UTF-8: %q", content)
	}
	if !strings.Contains(content, "写入文件") {
		t.Fatalf("synthetic content %q lost the Chinese tool name", content)
	}
	if !strings.Contains(content, missingToolResultText) {
		t.Fatalf("synthetic content %q lacks missing-result text", content)
	}
	if got[2].Content != "继续" {
		t.Fatalf("Chinese boundary message altered: %q", got[2].Content)
	}
}
