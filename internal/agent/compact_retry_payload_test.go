package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestCompactionPayloadStrip 验证压缩请求自身超限时先剥离媒体与已完成工具输出，
// 再按 rune 预算分块；入参不被修改，分块不拆消息。
func TestCompactionPayloadStrip(t *testing.T) {
	longTool := strings.Repeat("工", compactionStripToolRunes+100)
	original := []llm.Message{
		{Role: "user", Content: "with image", ContentParts: json.RawMessage(`[{"type":"image_url"}]`)},
		{Role: "tool", Content: longTool},
		{Role: "assistant", Content: "answer"},
	}
	stripped, count := StripCompactionPayload(original)
	if count != 2 {
		t.Fatalf("stripped = %d, want 2", count)
	}
	if len(original[0].ContentParts) == 0 || len(original[1].Content) != len(longTool) {
		t.Fatal("StripCompactionPayload mutated its input")
	}
	if len(stripped[0].ContentParts) != 0 || stripped[0].Content != "with image" {
		t.Fatalf("media part not stripped (text content must be preserved): %+v", stripped[0])
	}
	if len([]rune(stripped[1].Content)) > compactionStripToolRunes {
		t.Fatal("tool output was not clipped")
	}
	if stripped[2].Content != "answer" {
		t.Fatal("unrelated message changed")
	}

	for _, message := range []string{
		"HTTP 413 request entity too large",
		"function_payload_too_large: image",
		"Request Entity Too Large",
	} {
		if !IsCompactionPayloadOverflow(errors.New(message)) {
			t.Fatalf("overflow not detected: %q", message)
		}
	}
	if IsCompactionPayloadOverflow(nil) || IsCompactionPayloadOverflow(errors.New("context window exceeded")) {
		t.Fatal("non-payload error misclassified")
	}

	chunks := SplitMessagesForCompaction(stripped, 8)
	if len(chunks) < 2 {
		t.Fatalf("SplitMessagesForCompaction = %d chunks, want >= 2", len(chunks))
	}
	total := 0
	for _, chunk := range chunks {
		if len(chunk) == 0 {
			t.Fatal("empty chunk")
		}
		total += len(chunk)
	}
	if total != len(stripped) {
		t.Fatalf("split lost messages: %d != %d", total, len(stripped))
	}
}

// TestChunkedCompactionReduce 验证逐块摘要 + 递归归约的深度/并发/输出上限。
func TestChunkedCompactionReduce(t *testing.T) {
	chunks := make([][]llm.Message, 5)
	for i := range chunks {
		chunks[i] = []llm.Message{{Role: "user", Content: strings.Repeat("x", 20)}}
	}
	var calls, inFlight, maxInFlight int32
	var mu sync.Mutex
	summarize := func(messages []llm.Message) (string, error) {
		atomic.AddInt32(&calls, 1)
		current := atomic.AddInt32(&inFlight, 1)
		mu.Lock()
		if current > maxInFlight {
			maxInFlight = current
		}
		mu.Unlock()
		atomic.AddInt32(&inFlight, -1)
		return "summary", nil
	}
	result, err := ChunkedCompactionReduce(chunks, summarize, 3, 3, 2048)
	if err != nil {
		t.Fatalf("ChunkedCompactionReduce: %v", err)
	}
	if result == "" || len([]rune(result)) > 2048 {
		t.Fatalf("summary = %q", result)
	}
	if calls > 10 {
		t.Fatalf("summarizer called %d times, want <= 10 (depth 3)", calls)
	}
	if maxInFlight > 3 {
		t.Fatalf("peak concurrency = %d, want <= 3", maxInFlight)
	}

	// 单块摘要失败应向上返回错误，而不是产出半截摘要。
	boom := errors.New("secondary model failed")
	if _, err := ChunkedCompactionReduce(chunks, func([]llm.Message) (string, error) {
		return "", boom
	}, 3, 3, 2048); !errors.Is(err, boom) {
		t.Fatalf("summarize error = %v, want %v", err, boom)
	}
}
