package llm_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestStreamEOFWithIncompleteToolArgumentsFails covers the truncation bug: a
// provider that closes the SSE body mid-tool-call must not have its partial
// JSON silently replaced by "{}" and dispatched as a complete answer.
func TestStreamEOFWithIncompleteToolArgumentsFails(t *testing.T) {
	cases := []struct {
		name   string
		build  func(string, func(llm.AttemptRecord)) llm.Chatter
		script string
	}{
		{
			name: "chat completions",
			build: func(url string, hook func(llm.AttemptRecord)) llm.Chatter {
				c := llm.New(url, "")
				c.Options.AttemptHook = hook
				return c
			},
			// No [DONE]; the arguments stop inside a JSON string.
			script: `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tc_1","function":{"name":"read","arguments":"{\"path\":\"a.rs"}}]}}]}` + "\n\n",
		},
		{
			name: "anthropic messages",
			build: func(url string, hook func(llm.AttemptRecord)) llm.Chatter {
				c := llm.NewAnthropic(url, "")
				c.Options.AttemptHook = hook
				return c
			},
			// No content_block_stop and no message_stop.
			script: `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"read","input":{}}}` + "\n\n" +
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a.rs"}}` + "\n\n",
		},
		{
			name: "responses",
			build: func(url string, hook func(llm.AttemptRecord)) llm.Chatter {
				c := llm.NewResponses(url, "")
				c.Options.AttemptHook = hook
				return c
			},
			// No output_item.done and no response.completed.
			script: `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"read","arguments":""}}` + "\n\n" +
				`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"path\":\"a.rs"}` + "\n\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeSSE(w, tc.script)
			}))
			defer server.Close()

			var records []llm.AttemptRecord
			client := tc.build(server.URL, func(record llm.AttemptRecord) {
				records = append(records, record)
			})
			msg, err := client.Chat(context.Background(), "m", nil, nil, nil)
			if !errors.Is(err, llm.ErrStreamError) {
				t.Fatalf("err = %v, want ErrStreamError", err)
			}
			if msg.Content != "" || len(msg.ToolCalls) != 0 {
				t.Fatalf("truncated reply leaked to caller: %+v", msg)
			}
			if len(records) != 1 || records[0].Kind != llm.AttemptStreamError {
				t.Fatalf("attempt records = %+v, want one stream_error", records)
			}
			if !strings.Contains(records[0].ErrorClass, "tool call") {
				t.Fatalf("error class = %q, want the truncation reason", records[0].ErrorClass)
			}
		})
	}
}

// TestStreamEOFWithoutMarkerKeepsCompleteReplies documents the compatibility
// choice: gateways that omit the terminal marker but still finish their reply
// are accepted. Only an incomplete tool call (handled above) is fatal.
func TestStreamEOFWithoutMarkerKeepsCompleteReplies(t *testing.T) {
	cases := []struct {
		name        string
		build       func(string) llm.Chatter
		script      string
		wantContent string
		wantArgs    string
	}{
		{
			name:        "chat text only",
			build:       func(url string) llm.Chatter { return llm.New(url, "") },
			script:      `data: {"choices":[{"delta":{"content":"HALF-ANSWER"}}]}` + "\n\n",
			wantContent: "HALF-ANSWER",
		},
		{
			name:     "chat complete tool call",
			build:    func(url string) llm.Chatter { return llm.New(url, "") },
			script:   `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tc_1","function":{"name":"read","arguments":"{}"}}]}}]}` + "\n\n",
			wantArgs: "{}",
		},
		{
			name:        "anthropic text only",
			build:       func(url string) llm.Chatter { return llm.NewAnthropic(url, "") },
			script:      `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"HALF-ANSWER"}}` + "\n\n",
			wantContent: "HALF-ANSWER",
		},
		{
			name:        "responses text only",
			build:       func(url string) llm.Chatter { return llm.NewResponses(url, "") },
			script:      `data: {"type":"response.output_text.delta","delta":"HALF-ANSWER"}` + "\n\n",
			wantContent: "HALF-ANSWER",
		},
		{
			name:     "responses complete tool call",
			build:    func(url string) llm.Chatter { return llm.NewResponses(url, "") },
			script:   `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"read","arguments":"{}"}}` + "\n\n",
			wantArgs: "{}",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeSSE(w, tc.script)
			}))
			defer server.Close()

			msg, err := tc.build(server.URL).Chat(context.Background(), "m", nil, nil, nil)
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if msg.Content != tc.wantContent {
				t.Fatalf("content = %q, want %q", msg.Content, tc.wantContent)
			}
			if tc.wantArgs == "" {
				if len(msg.ToolCalls) != 0 {
					t.Fatalf("tool calls = %+v, want none", msg.ToolCalls)
				}
				return
			}
			if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Arguments != tc.wantArgs {
				t.Fatalf("tool calls = %+v, want one with arguments %q", msg.ToolCalls, tc.wantArgs)
			}
		})
	}
}
