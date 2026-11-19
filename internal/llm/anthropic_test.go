package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argent-go/internal/llm"
)

const anthropicToolUseScript = `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":12,"cache_read_input_tokens":3,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Considering"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"c2ln"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hello "}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"world"}}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"read","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"a.rs\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":2}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}

event: message_stop
data: {"type":"message_stop"}

`

func writeSSE(w http.ResponseWriter, script string) {
	w.Header().Set("Content-Type", "text/event-stream")
	if flusher, ok := w.(http.Flusher); ok {
		defer flusher.Flush()
	}
	io.WriteString(w, script)
}

func noWait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

func TestAnthropicStreamParsing(t *testing.T) {
	cases := []struct {
		name          string
		script        string
		wantContent   string
		wantReasoning string
		wantCalls     []llm.ToolCall
		wantUsage     *llm.Usage
		wantErr       string
	}{
		{
			name:          "text thinking and tool call",
			script:        anthropicToolUseScript,
			wantContent:   "Hello world",
			wantReasoning: "Considering",
			wantCalls: []llm.ToolCall{{
				ID:       "toolu_1",
				Type:     "function",
				Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"a.rs"}`},
			}},
			wantUsage: &llm.Usage{PromptTokens: 15, CompletionTokens: 7},
		},
		{
			name: "tool call without content_block_stop is flushed at message_stop",
			script: `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_9","name":"ls","input":{}}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}

data: {"type":"message_stop"}

`,
			wantCalls: []llm.ToolCall{{
				ID:       "toolu_9",
				Type:     "function",
				Function: llm.ToolCallFunction{Name: "ls", Arguments: "{}"},
			}},
		},
		{
			name: "tool call with input supplied at start",
			script: `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_2","name":"read","input":{"path":"b.rs"}}}

data: {"type":"message_stop"}

`,
			wantCalls: []llm.ToolCall{{
				ID:       "toolu_2",
				Type:     "function",
				Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"b.rs"}`},
			}},
		},
		{
			name:        "text only stream ends without message_stop",
			script:      "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n",
			wantContent: "partial",
		},
		{
			name:    "stream error event",
			script:  "data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n",
			wantErr: "Overloaded",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeSSE(w, tc.script)
			}))
			defer server.Close()

			client := llm.NewAnthropic(server.URL, "test-key")
			var deltas []string
			reply, err := client.Chat(context.Background(), "claude-test", nil, nil, func(delta string) {
				deltas = append(deltas, delta)
			})

			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("chat: %v", err)
			}
			if reply.Content != tc.wantContent {
				t.Fatalf("content = %q, want %q", reply.Content, tc.wantContent)
			}
			if reply.Reasoning != tc.wantReasoning {
				t.Fatalf("reasoning = %q, want %q", reply.Reasoning, tc.wantReasoning)
			}
			if reply.Role != "assistant" {
				t.Fatalf("role = %q, want assistant", reply.Role)
			}
			if len(reply.ToolCalls) != len(tc.wantCalls) {
				t.Fatalf("tool calls = %#v, want %#v", reply.ToolCalls, tc.wantCalls)
			}
			for i, want := range tc.wantCalls {
				if !reflect.DeepEqual(reply.ToolCalls[i], want) {
					t.Fatalf("tool call %d = %#v, want %#v", i, reply.ToolCalls[i], want)
				}
			}
			if !reflect.DeepEqual(reply.Usage, tc.wantUsage) {
				t.Fatalf("usage = %#v, want %#v", reply.Usage, tc.wantUsage)
			}
			if len(deltas) > 0 && strings.Join(deltas, "") != tc.wantContent {
				t.Fatalf("streamed text = %q, want %q", strings.Join(deltas, ""), tc.wantContent)
			}
		})
	}
}

func TestAnthropicRequestShape(t *testing.T) {
	var body map[string]any
	var header http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Clone()
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %q, want /v1/messages", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		writeSSE(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	client := llm.NewAnthropic(server.URL, "secret")
	client.MaxTokens = 2048

	tools := []llm.ToolSpec{{
		Type: "function",
	}}
	tools[0].Function.Name = "read"
	tools[0].Function.Description = "read a file"
	tools[0].Function.Parameters = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)

	messages := []llm.Message{
		{Role: "system", Content: "be terse"},
		{Role: "user", Content: "read it"},
		{Role: "assistant", Content: "sure", ToolCalls: []llm.ToolCall{{
			ID:       "toolu_1",
			Type:     "function",
			Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"a.rs"}`},
		}}},
		{Role: "tool", ToolCallID: "toolu_1", Content: "fn main() {}"},
		{Role: "tool", ToolCallID: "toolu_2", Content: "second"},
	}

	if _, err := client.Chat(context.Background(), "claude-test", messages, tools, nil); err != nil {
		t.Fatalf("chat: %v", err)
	}

	if got := header.Get("x-api-key"); got != "secret" {
		t.Fatalf("x-api-key = %q, want secret", got)
	}
	if got := header.Get("anthropic-version"); got != llm.AnthropicVersion {
		t.Fatalf("anthropic-version = %q, want %q", got, llm.AnthropicVersion)
	}
	if got := header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want empty", got)
	}
	if got := body["max_tokens"]; got != float64(2048) {
		t.Fatalf("max_tokens = %#v, want 2048", got)
	}
	if got := body["model"]; got != "claude-test" {
		t.Fatalf("model = %#v", got)
	}
	if got := body["system"]; got != "be terse" {
		t.Fatalf("system = %#v, want hoisted system prompt", got)
	}
	if got := body["stream"]; got != true {
		t.Fatalf("stream = %#v, want true", got)
	}

	toolSpecs, _ := body["tools"].([]any)
	if len(toolSpecs) != 1 {
		t.Fatalf("tools = %#v", body["tools"])
	}
	tool, _ := toolSpecs[0].(map[string]any)
	if tool["name"] != "read" || tool["description"] != "read a file" {
		t.Fatalf("tool = %#v", tool)
	}
	if _, ok := tool["input_schema"].(map[string]any); !ok {
		t.Fatalf("tool input_schema = %#v", tool["input_schema"])
	}

	wire, _ := body["messages"].([]any)
	if len(wire) != 3 {
		t.Fatalf("messages = %#v, want user, assistant, merged tool results", wire)
	}
	last, _ := wire[2].(map[string]any)
	if last["role"] != "user" {
		t.Fatalf("last role = %#v, want user", last["role"])
	}
	blocks, _ := last["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("tool result blocks = %#v, want two merged results", blocks)
	}
	first, _ := blocks[0].(map[string]any)
	if first["type"] != "tool_result" || first["tool_use_id"] != "toolu_1" || first["content"] != "fn main() {}" {
		t.Fatalf("tool result = %#v", first)
	}
	assistant, _ := wire[1].(map[string]any)
	assistantBlocks, _ := assistant["content"].([]any)
	if len(assistantBlocks) != 2 {
		t.Fatalf("assistant blocks = %#v, want text + tool_use", assistantBlocks)
	}
	use, _ := assistantBlocks[1].(map[string]any)
	if use["type"] != "tool_use" || use["name"] != "read" {
		t.Fatalf("tool_use = %#v", use)
	}
	if input, _ := use["input"].(map[string]any); input["path"] != "a.rs" {
		t.Fatalf("tool_use input = %#v", use["input"])
	}
}

func TestAnthropicDefaultsMaxTokens(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		writeSSE(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	if _, err := llm.NewAnthropic(server.URL, "").Chat(context.Background(), "m", nil, nil, nil); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if got := body["max_tokens"]; got != float64(llm.DefaultAnthropicMaxTokens) {
		t.Fatalf("max_tokens = %#v, want %d", got, llm.DefaultAnthropicMaxTokens)
	}
}

func TestAnthropicErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"type":"error","error":{"message":"invalid x-api-key"}}`)
	}))
	defer server.Close()

	_, err := llm.NewAnthropic(server.URL, "bad").Chat(context.Background(), "m", nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "invalid x-api-key") {
		t.Fatalf("err = %v, want 401 with body", err)
	}
}

func TestAnthropicRetriesRateLimit(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"message":"slow down"}}`)
			return
		}
		writeSSE(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	client := llm.NewAnthropic(server.URL, "k")
	client.Retry = llm.RetryPolicy{Sleep: noWait}

	reply, err := client.Chat(context.Background(), "m", nil, nil, nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if reply.Content != "ok" {
		t.Fatalf("content = %q, want ok", reply.Content)
	}
}

func TestAnthropicGivesUpAfterRetries(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, "upstream down")
	}))
	defer server.Close()

	client := llm.NewAnthropic(server.URL, "k")
	client.Retry = llm.RetryPolicy{MaxAttempts: 3, Sleep: noWait}

	_, err := client.Chat(context.Background(), "m", nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("err = %v, want 502", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestAnthropicCancelsDuringBackoff(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := llm.NewAnthropic(server.URL, "k")
	_, err := client.Chat(ctx, "m", nil, nil, nil)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if attempts != 0 {
		t.Fatalf("attempts = %d, want 0 after cancellation", attempts)
	}
}

func TestAnthropicRequiresModel(t *testing.T) {
	client := llm.NewAnthropic("http://127.0.0.1:1", "k")
	if _, err := client.Chat(context.Background(), "  ", nil, nil, nil); err == nil {
		t.Fatal("err = nil, want model required")
	}
}

func TestAnthropicEndpoint(t *testing.T) {
	cases := map[string]string{
		"":               "/v1/messages",
		"/v1":            "/v1/messages",
		"/api/anthropic": "/api/anthropic/v1/messages",
		"/messages":      "/messages",
	}
	for suffix, want := range cases {
		var path string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path = r.URL.Path
			writeSSE(w, "data: {\"type\":\"message_stop\"}\n\n")
		}))
		client := llm.NewAnthropic(server.URL+suffix, "k")
		if _, err := client.Chat(context.Background(), "m", nil, nil, nil); err != nil {
			server.Close()
			t.Fatalf("base %q: %v", suffix, err)
		}
		server.Close()
		if path != want {
			t.Fatalf("base %q: path = %q, want %q", suffix, path, want)
		}
	}
}
