package llm_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// chatNoWait 让重试测试保持快速，而不触碰共享的测试辅助函数。
func chatNoWait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

func TestChatClientRetriesTransientFailure(t *testing.T) {
	var requests atomic.Int32
	var records []llm.AttemptRecord
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":{"message":"try later"}}`)
			return
		}
		writeSSE(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	client := llm.New(server.URL, "key")
	client.Retry = llm.RetryPolicy{MaxAttempts: 3, Sleep: chatNoWait}
	client.Options.AttemptHook = func(record llm.AttemptRecord) { records = append(records, record) }
	reply, err := client.Chat(context.Background(), "m", nil, nil, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if reply.Content != "ok" {
		t.Fatalf("content = %q, want ok", reply.Content)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("server received %d requests, want 2 (503 then 200)", got)
	}
	if len(records) != 2 || records[0].Kind != llm.AttemptRetry || records[1].Kind != llm.AttemptSuccess {
		t.Fatalf("attempt records = %+v, want retry then success", records)
	}
	if records[0].StatusCode != http.StatusServiceUnavailable || records[0].Protocol != llm.ProtocolChatCompletions {
		t.Fatalf("retry record = %+v", records[0])
	}
}

func TestChatClientDoesNotRetryHardQuota(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"insufficient_quota"}}`)
	}))
	defer server.Close()

	client := llm.New(server.URL, "key")
	client.Retry = llm.RetryPolicy{MaxAttempts: 3, Sleep: chatNoWait}
	if _, err := client.Chat(context.Background(), "m", nil, nil, nil); err == nil {
		t.Fatal("Chat succeeded on a hard quota response")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("server received %d requests, want 1 for a hard quota", got)
	}
}

func TestChatClientReadsMultilineSSEData(t *testing.T) {
	cases := []struct {
		name   string
		script string
	}{
		{
			name: "join at a token boundary",
			script: `: keep-alive
event: message
data: {"choices":[{"delta":
data: {"content":"Hello"}}]}

data: [DONE]

`,
		},
		{
			name: "split inside a JSON string",
			script: `data: {"choices":[{"delta":{"content":"Hel
data: lo"}}]}

data: [DONE]

`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeSSE(w, tc.script)
			}))
			defer server.Close()

			var deltas strings.Builder
			reply, err := llm.New(server.URL, "").Chat(context.Background(), "m", nil, nil, func(delta string) {
				deltas.WriteString(delta)
			})
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if reply.Content != "Hello" || deltas.String() != "Hello" {
				t.Fatalf("content = %q, deltas = %q, want Hello", reply.Content, deltas.String())
			}
		})
	}
}

func TestChatClientGeneratedToolCallIDsDoNotCollide(t *testing.T) {
	// 第一个调用到达时没有 id；第二个实际名为
	// call_1。生成的 id 必须跳过该名称。
	script := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"a","arguments":"{}"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_1","function":{"name":"b","arguments":"{}"}}]}}]}

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
	if len(reply.ToolCalls) != 2 {
		t.Fatalf("tool calls = %+v, want 2", reply.ToolCalls)
	}
	seen := make(map[string]bool, len(reply.ToolCalls))
	for _, call := range reply.ToolCalls {
		if call.ID == "" || seen[call.ID] {
			t.Fatalf("duplicate or empty tool call id in %+v", reply.ToolCalls)
		}
		seen[call.ID] = true
	}
	if !seen["call_1"] || len(seen) != 2 {
		t.Fatalf("tool call ids = %+v, want call_1 plus a distinct generated id", seen)
	}
	if reply.ToolCalls[0].ID == "call_1" {
		t.Fatalf("generated id collided with the provider id: %+v", reply.ToolCalls)
	}
}

func TestChatClientFallsBackOnMalformedToolArguments(t *testing.T) {
	script := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tc_1","function":{"name":"read","arguments":"{\"path\":\"a.rs"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"tc_2","function":{"name":"read","arguments":"{\"path\":\"b.rs\"}"}}]}}]}

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
	if len(reply.ToolCalls) != 2 {
		t.Fatalf("tool calls = %+v, want 2", reply.ToolCalls)
	}
	if got := reply.ToolCalls[0].Function.Arguments; got != "{}" {
		t.Fatalf("malformed arguments = %q, want {}", got)
	}
	if got := reply.ToolCalls[1].Function.Arguments; got != `{"path":"b.rs"}` {
		t.Fatalf("valid arguments = %q, want the original JSON", got)
	}
}

func TestChatClientZeroRetryStillStreams(t *testing.T) {
	// New() 让 Retry 保持零值；即便没有任何配置，它也必须是一个
	// 可用的客户端（回退到 DefaultRetryPolicy）。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	reply, err := llm.New(server.URL, "").Chat(context.Background(), "m", nil, nil, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if reply.Role != "assistant" || reply.Content != "hi" {
		t.Fatalf("reply = %+v, want assistant/hi", reply)
	}
}
