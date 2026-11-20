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

// sameBackingSlice 报告两个切片是否共享首元素，即 NormalizeToolPairing 是否原样返回了输入切片。
// （接上行）
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

// ① 和 ⑤：完全配对的记录按引用返回且未被修改。
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

// ②：后接用户边界的孤立调用会得到一个错误结果，调用名被折进文本，且输入未被修改。
// （接上行）
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

// ②（尾部变体）：记录末尾的孤立调用会得到回应。
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

// ③：多个助手/用户边界、每批多个调用，以及真实结果与孤立结果的混合都保持顺序并被配对。
// （接上行）
func TestNormalizeToolPairingAcrossMultipleBoundaries(t *testing.T) {
	input := []llm.Message{
		{Role: "user", Content: "q1"},
		pairingAssistant(pairingCall("c1", "a"), pairingCall("c2", "b")),
		pairingTool("c1", "ok1"),
		{Role: "user", Content: "q2"}, // c2 在这里是孤立的
		pairingAssistant(pairingCall("c3", "c")),
		pairingTool("c3", "ok3"),
		{Role: "user", Content: "q3"},
		pairingAssistant(pairingCall("c4", "d")), // 尾部的孤立调用
	}

	got := NormalizeToolPairing(input)

	// 遍历结果并记录工具结果与边界的顺序。
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
		"tool:c1", // 真实结果在前
		"tool:c2", // 在 q2 边界之前合成
		"user:q2",
		"assistant:c3",
		"tool:c3", // 真实结果
		"user:q3",
		"assistant:c4",
		"tool:c4", // 在尾部合成
	}
	if strings.Join(layout, ",") != strings.Join(want, ",") {
		t.Fatalf("layout = %v, want %v", layout, want)
	}

	// 合成的 c2 结果必须提及它的工具名。
	for _, m := range got {
		if m.Role == "tool" && m.ToolCallID == "c2" {
			if !strings.Contains(m.Content, "\"b\"") {
				t.Fatalf("c2 result %q lacks name backfill", m.Content)
			}
		}
	}
}

// ④：空/仅空白的工具输出会被规范化为占位符，无论是对已有结果还是对无名的孤立调用。
// （接上行）
func TestNormalizeToolPairingNormalizesEmptyOutput(t *testing.T) {
	input := []llm.Message{
		pairingAssistant(pairingCall("c1", "read_file")),
		pairingTool("c1", "   \t\n "), // 已有的空结果
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

// ⑥：UTF-8/中文工具名和结果内容在规范化后逐字节保留，完全配对的中文记录按引用返回。
// （接上行）
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
