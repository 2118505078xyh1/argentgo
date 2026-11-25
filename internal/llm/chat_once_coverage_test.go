package llm_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestChatCompletionsChatOnceWireBytes 守护最常用的 chat completions 回补线：
// stream=false、无 stream_options（与 stream=false 互斥会被引擎拒绝）、
// Accept=application/json、Authorization 携带 key。
func TestChatCompletionsChatOnceWireBytes(t *testing.T) {
	var body, accept, auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		accept = r.Header.Get("Accept")
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer server.Close()

	client := llm.New(server.URL, "sk-test-key")
	if _, err := client.ChatOnce(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("ChatOnce: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("body not JSON: %s", body)
	}
	if decoded["stream"] != false {
		t.Fatalf("stream must be false: %s", body)
	}
	if _, ok := decoded["stream_options"]; ok {
		t.Fatalf("stream_options must be absent with stream=false: %s", body)
	}
	if accept != "application/json" {
		t.Fatalf("Accept = %q, want application/json", accept)
	}
	if !strings.Contains(auth, "sk-test-key") {
		t.Fatalf("Authorization = %q", auth)
	}
}

// TestChatCompletionsChatOnceParsing 表驱动覆盖 reasoning 双拼写回退、
// finish_reason、cached token、缺省 tool id 与空 choices 报错。
func TestChatCompletionsChatOnceParsing(t *testing.T) {
	cases := []struct {
		name       string
		resp       string
		wantReason string
		wantText   string
		wantToolID string // 非空时断言首个工具 id 前缀
		wantErr    bool
	}{
		{
			name:       "reasoning_content preferred",
			resp:       `{"choices":[{"message":{"content":"a","reasoning_content":"RC","reasoning":"R"},"finish_reason":"stop"}]}`,
			wantReason: "RC", wantText: "a",
		},
		{
			name:       "reasoning fallback",
			resp:       `{"choices":[{"message":{"content":"a","reasoning":"R"},"finish_reason":"stop"}]}`,
			wantReason: "R", wantText: "a",
		},
		{
			name:       "tool call missing id",
			resp:       `{"choices":[{"message":{"tool_calls":[{"type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
			wantToolID: "call_",
		},
		{
			name:    "empty choices",
			resp:    `{"choices":[]}`,
			wantErr: true,
		},
		{
			name:     "cached tokens",
			resp:     `{"choices":[{"message":{"content":"a"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":7}}}`,
			wantText: "a",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(tc.resp))
			}))
			defer server.Close()
			client := llm.New(server.URL, "k")
			msg, err := client.ChatOnce(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got msg %+v", msg)
				}
				return
			}
			if err != nil {
				t.Fatalf("ChatOnce: %v", err)
			}
			if tc.wantReason != "" && msg.Reasoning != tc.wantReason {
				t.Fatalf("reasoning = %q, want %q", msg.Reasoning, tc.wantReason)
			}
			if tc.wantText != "" && msg.Content != tc.wantText {
				t.Fatalf("content = %q, want %q", msg.Content, tc.wantText)
			}
			if tc.wantToolID != "" {
				if len(msg.ToolCalls) == 0 || !strings.HasPrefix(msg.ToolCalls[0].ID, tc.wantToolID) {
					t.Fatalf("tool calls = %+v, want id prefix %q", msg.ToolCalls, tc.wantToolID)
				}
			}
		})
	}
}
