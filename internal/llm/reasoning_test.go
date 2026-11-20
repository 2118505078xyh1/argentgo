package llm_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestChatStreamCapturesReasoningContent covers the OpenAI-compatible chat
// protocol: incremental reasoning_content is aggregated into Reply.Reasoning,
// is never forwarded to onText, and CJK text survives byte-for-byte.
func TestChatStreamCapturesReasoningContent(t *testing.T) {
	script := `data: {"choices":[{"delta":{"reasoning_content":"让我"}}]}

data: {"choices":[{"delta":{"reasoning_content":"逐步思考：先看输入"}}]}

data: {"choices":[{"delta":{"content":"答案是 42"}}]}

data: [DONE]

`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, script)
	}))
	defer server.Close()

	var streamed strings.Builder
	reply, err := llm.New(server.URL, "").Chat(context.Background(), "m", nil, nil, func(delta string) {
		streamed.WriteString(delta)
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	wantReasoning := "让我逐步思考：先看输入"
	if reply.Reasoning != wantReasoning {
		t.Fatalf("reasoning = %q, want %q", reply.Reasoning, wantReasoning)
	}
	if reply.Content != "答案是 42" {
		t.Fatalf("content = %q, want 答案是 42", reply.Content)
	}
	if streamed.String() != "答案是 42" {
		t.Fatalf("onText = %q, want only answer content", streamed.String())
	}
}

// TestChatStreamReasoningAliasPrefersContent makes sure a provider that emits
// both reasoning_content and reasoning in one delta does not double the text.
func TestChatStreamReasoningAliasPrefersContent(t *testing.T) {
	script := `data: {"choices":[{"delta":{"reasoning_content":"A","reasoning":"B"}}]}

data: {"choices":[{"delta":{"reasoning":"C"}}]}

data: [DONE]

`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, script)
	}))
	defer server.Close()

	reply, err := llm.New(server.URL, "").Chat(context.Background(), "m", nil, nil, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if reply.Reasoning != "AC" {
		t.Fatalf("reasoning = %q, want AC (reasoning_content wins, alias still captured)", reply.Reasoning)
	}
}

// TestAnthropicStreamCapturesCJKReasoning confirms thinking deltas are
// aggregated into Reply.Reasoning without cutting any rune.
func TestAnthropicStreamCapturesCJKReasoning(t *testing.T) {
	script := `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"我需要"}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"核对边界"}}

data: {"type":"content_block_stop","index":0}

data: {"type":"message_stop"}

`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, script)
	}))
	defer server.Close()

	reply, err := llm.NewAnthropic(server.URL, "").Chat(context.Background(), "m", nil, nil, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if reply.Reasoning != "我需要核对边界" {
		t.Fatalf("reasoning = %q, want 我需要核对边界", reply.Reasoning)
	}
}

// TestResponsesStreamCapturesCJKReasoning confirms the Responses reasoning
// delta events are aggregated into Reply.Reasoning.
func TestResponsesStreamCapturesCJKReasoning(t *testing.T) {
	script := `data: {"type":"response.reasoning_summary_text.delta","delta":"先分析"}

data: {"type":"response.reasoning_text.delta","delta":"再判断"}

data: {"type":"response.completed","response":{"status":"completed"}}

`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, script)
	}))
	defer server.Close()

	reply, err := llm.NewResponses(server.URL, "").Chat(context.Background(), "m", nil, nil, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if reply.Reasoning != "先分析再判断" {
		t.Fatalf("reasoning = %q, want 先分析再判断", reply.Reasoning)
	}
}

// TestChatWireReasoningIsOptIn asserts the end-to-end wire contract: with the
// switch off no reasoning_content is sent; with it on, only the assistant turn
// carries the exact reasoning text captured earlier.
func TestChatWireReasoningIsOptIn(t *testing.T) {
	cases := []struct {
		name      string
		preserve  bool
		wantField bool
	}{
		{"default off", false, false},
		{"opt in", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body struct {
				Messages []struct {
					Role             string `json:"role"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"messages"`
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read body: %v", err)
					return
				}
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Errorf("decode body: %v", err)
				}
				writeSSE(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()

			client := llm.New(server.URL, "")
			client.Options.PreserveReasoning = tc.preserve
			messages := []llm.Message{
				{Role: "user", Content: "hi"},
				{Role: "assistant", Content: "answer", Reasoning: "中文推理"},
			}
			if _, err := client.Chat(context.Background(), "m", messages, nil, nil); err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if len(body.Messages) != 2 {
				t.Fatalf("messages = %d, want 2", len(body.Messages))
			}
			got := body.Messages[1].ReasoningContent
			if tc.wantField && got != "中文推理" {
				t.Fatalf("reasoning_content = %q, want 中文推理", got)
			}
			if !tc.wantField && got != "" {
				t.Fatalf("reasoning_content = %q, want empty by default", got)
			}
			if body.Messages[0].ReasoningContent != "" {
				t.Fatalf("user message must not carry reasoning_content")
			}
		})
	}
}
