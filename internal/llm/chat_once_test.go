package llm_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// 默认流式路径的请求字节是回归基线：ChatOnce 只把 stream 置 false，
// 绝不能改变 Chat 的既有线协议表示。
func TestAnthropicChatDefaultWireBytes(t *testing.T) {
	var body string
	var accept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		accept = r.Header.Get("Accept")
		writeSSE(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	client := llm.NewAnthropic(server.URL, "k")
	if _, err := client.Chat(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("chat: %v", err)
	}
	want := `{"model":"m","max_tokens":8192,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"stream":true}`
	if body != want {
		t.Fatalf("wire = %s, want %s", body, want)
	}
	if accept != "text/event-stream" {
		t.Fatalf("Accept = %q, want text/event-stream", accept)
	}
}

func TestResponsesChatDefaultWireBytes(t *testing.T) {
	var body string
	var accept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		accept = r.Header.Get("Accept")
		writeSSE(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()

	client := llm.NewResponses(server.URL, "k")
	if _, err := client.Chat(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("chat: %v", err)
	}
	want := `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"max_output_tokens":8192,"stream":true,"store":false}`
	if body != want {
		t.Fatalf("wire = %s, want %s", body, want)
	}
	if accept != "text/event-stream" {
		t.Fatalf("Accept = %q, want text/event-stream", accept)
	}
}

func TestAnthropicChatOnceRequest(t *testing.T) {
	var body map[string]any
	var header http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"content":[{"type":"text","text":"ok"}]}`)
	}))
	defer server.Close()

	client := llm.NewAnthropic(server.URL, "secret")
	reply, err := client.ChatOnce(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("chat once: %v", err)
	}
	if reply.Content != "ok" {
		t.Fatalf("content = %q, want ok", reply.Content)
	}
	if got := body["stream"]; got != false {
		t.Fatalf("stream = %#v, want false", got)
	}
	if got := header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q, want application/json", got)
	}
	if got := header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := header.Get("x-api-key"); got != "secret" {
		t.Fatalf("x-api-key = %q, want secret", got)
	}
	if got := header.Get("anthropic-version"); got != llm.AnthropicVersion {
		t.Fatalf("anthropic-version = %q, want %q", got, llm.AnthropicVersion)
	}
}

func TestResponsesChatOnceRequest(t *testing.T) {
	var body map[string]any
	var header http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
	}))
	defer server.Close()

	client := llm.NewResponses(server.URL, "secret")
	reply, err := client.ChatOnce(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("chat once: %v", err)
	}
	if reply.Content != "ok" {
		t.Fatalf("content = %q, want ok", reply.Content)
	}
	if got := body["stream"]; got != false {
		t.Fatalf("stream = %#v, want false", got)
	}
	if got := header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q, want application/json", got)
	}
	if got := header.Get("Authorization"); got != "Bearer secret" {
		t.Fatalf("Authorization = %q, want Bearer secret", got)
	}
}

func TestAnthropicChatOnceParsing(t *testing.T) {
	cases := []struct {
		name          string
		body          string
		wantContent   string
		wantReasoning string
		wantSignature string
		wantCalls     []llm.ToolCall
		wantUsage     *llm.Usage
		wantFinish    string
		wantErr       string
	}{
		{
			name: "text thinking tool and usage",
			body: `{"id":"msg_1","content":[
				{"type":"thinking","thinking":"Consider","signature":"c2ln"},
				{"type":"text","text":"Hello"},
				{"type":"tool_use","id":"toolu_1","name":"read","input":{"path":"a.rs"}}
			],"stop_reason":"tool_use","usage":{"input_tokens":12,"cache_read_input_tokens":3,"cache_creation_input_tokens":2,"output_tokens":7}}`,
			wantContent:   "Hello",
			wantReasoning: "Consider",
			wantSignature: "c2ln",
			wantCalls: []llm.ToolCall{{
				ID:       "toolu_1",
				Type:     "function",
				Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"a.rs"}`},
			}},
			wantUsage:  &llm.Usage{PromptTokens: 17, CompletionTokens: 7, CachedTokens: 3, CacheWriteTokens: 2},
			wantFinish: "tool_use",
		},
		{
			name: "missing tool id gets unique fallback without collision",
			body: `{"content":[
				{"type":"tool_use","name":"ls","input":{}},
				{"type":"tool_use","id":"toolu_0","name":"cat","input":{"p":"x"}}
			]}`,
			wantCalls: []llm.ToolCall{
				{ID: "toolu_1", Type: "function", Function: llm.ToolCallFunction{Name: "ls", Arguments: "{}"}},
				{ID: "toolu_0", Type: "function", Function: llm.ToolCallFunction{Name: "cat", Arguments: `{"p":"x"}`}},
			},
		},
		{
			name:    "empty content",
			body:    `{"content":[]}`,
			wantErr: "empty content",
		},
		{
			name:    "provider error body",
			body:    `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
			wantErr: "Overloaded",
		},
		{
			name:    "invalid json",
			body:    `not json`,
			wantErr: "decode response",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, tc.body)
			}))
			defer server.Close()

			reply, err := llm.NewAnthropic(server.URL, "k").ChatOnce(context.Background(), "m", nil, nil)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("chat once: %v", err)
			}
			if reply.Role != "assistant" {
				t.Fatalf("role = %q, want assistant", reply.Role)
			}
			if reply.Content != tc.wantContent {
				t.Fatalf("content = %q, want %q", reply.Content, tc.wantContent)
			}
			if reply.Reasoning != tc.wantReasoning {
				t.Fatalf("reasoning = %q, want %q", reply.Reasoning, tc.wantReasoning)
			}
			if reply.ReasoningSignature != tc.wantSignature {
				t.Fatalf("signature = %q, want %q", reply.ReasoningSignature, tc.wantSignature)
			}
			if reply.FinishReason != tc.wantFinish {
				t.Fatalf("finish reason = %q, want %q", reply.FinishReason, tc.wantFinish)
			}
			if !reflect.DeepEqual(reply.ToolCalls, tc.wantCalls) {
				t.Fatalf("tool calls = %#v, want %#v", reply.ToolCalls, tc.wantCalls)
			}
			if !reflect.DeepEqual(reply.Usage, tc.wantUsage) {
				t.Fatalf("usage = %#v, want %#v", reply.Usage, tc.wantUsage)
			}
		})
	}
}

func TestResponsesChatOnceParsing(t *testing.T) {
	cases := []struct {
		name          string
		body          string
		wantContent   string
		wantReasoning string
		wantCalls     []llm.ToolCall
		wantUsage     *llm.Usage
		wantFinish    string
		wantErr       string
	}{
		{
			name: "text reasoning tool and usage",
			body: `{"id":"resp_1","status":"completed","output":[
				{"type":"reasoning","summary":[{"type":"summary_text","text":"Think"}]},
				{"type":"message","content":[{"type":"output_text","text":"Hello"}]},
				{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":"{\"path\":\"a.rs\"}"}
			],"usage":{"input_tokens":10,"output_tokens":5,"cache_write_tokens":1,"input_tokens_details":{"cached_tokens":3,"cache_write_tokens":2}}}`,
			wantContent:   "Hello",
			wantReasoning: "Think",
			wantCalls: []llm.ToolCall{{
				ID:       "call_1",
				Type:     "function",
				Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"a.rs"}`},
			}},
			wantUsage:  &llm.Usage{PromptTokens: 10, CompletionTokens: 5, CachedTokens: 3, CacheWriteTokens: 2},
			wantFinish: "completed",
		},
		{
			name:        "incomplete details become finish reason",
			body:        `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"trunc"}]}]}`,
			wantContent: "trunc",
			wantFinish:  "max_output_tokens",
		},
		{
			name: "missing call id gets unique fallback",
			body: `{"status":"completed","output":[{"type":"function_call","name":"ls","arguments":"{}"}]}`,
			wantCalls: []llm.ToolCall{{
				ID:       "call_1",
				Type:     "function",
				Function: llm.ToolCallFunction{Name: "ls", Arguments: "{}"},
			}},
			wantFinish: "completed",
		},
		{
			name:    "empty output",
			body:    `{"status":"completed","output":[]}`,
			wantErr: "empty output",
		},
		{
			name:    "failed status",
			body:    `{"status":"failed","error":{"message":"quota exhausted"}}`,
			wantErr: "quota exhausted",
		},
		{
			name:    "invalid json",
			body:    `not json`,
			wantErr: "decode response",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, tc.body)
			}))
			defer server.Close()

			reply, err := llm.NewResponses(server.URL, "k").ChatOnce(context.Background(), "m", nil, nil)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("chat once: %v", err)
			}
			if reply.Role != "assistant" {
				t.Fatalf("role = %q, want assistant", reply.Role)
			}
			if reply.Content != tc.wantContent {
				t.Fatalf("content = %q, want %q", reply.Content, tc.wantContent)
			}
			if reply.Reasoning != tc.wantReasoning {
				t.Fatalf("reasoning = %q, want %q", reply.Reasoning, tc.wantReasoning)
			}
			if reply.FinishReason != tc.wantFinish {
				t.Fatalf("finish reason = %q, want %q", reply.FinishReason, tc.wantFinish)
			}
			if !reflect.DeepEqual(reply.ToolCalls, tc.wantCalls) {
				t.Fatalf("tool calls = %#v, want %#v", reply.ToolCalls, tc.wantCalls)
			}
			if !reflect.DeepEqual(reply.Usage, tc.wantUsage) {
				t.Fatalf("usage = %#v, want %#v", reply.Usage, tc.wantUsage)
			}
		})
	}
}
