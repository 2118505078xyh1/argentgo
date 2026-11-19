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

const responsesToolScript = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1"}}

event: response.reasoning_summary_text.delta
data: {"type":"response.reasoning_summary_text.delta","delta":"Thinking"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hello"}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"path\":"}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"\"a.rs\"}"}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call"}}

event: response.completed
data: {"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}

`

func TestResponsesStreamParsing(t *testing.T) {
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
			name:          "text reasoning and streamed function call",
			script:        responsesToolScript,
			wantContent:   "Hello",
			wantReasoning: "Thinking",
			wantCalls: []llm.ToolCall{{
				ID:       "call_1",
				Type:     "function",
				Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"a.rs"}`},
			}},
			wantUsage: &llm.Usage{PromptTokens: 10, CompletionTokens: 5},
		},
		{
			name: "function call completed in one done item",
			script: `data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_2","call_id":"call_2","name":"ls","arguments":"{}"}}

data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":2}}}

`,
			wantCalls: []llm.ToolCall{{
				ID:       "call_2",
				Type:     "function",
				Function: llm.ToolCallFunction{Name: "ls", Arguments: "{}"},
			}},
			wantUsage: &llm.Usage{PromptTokens: 1, CompletionTokens: 2},
		},
		{
			name:        "incomplete response keeps text",
			script:      "data: {\"type\":\"response.output_text.delta\",\"delta\":\"trunc\"}\n\ndata: {\"type\":\"response.incomplete\",\"response\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":4}}}\n\n",
			wantContent: "trunc",
			wantUsage:   &llm.Usage{PromptTokens: 3, CompletionTokens: 4},
		},
		{
			name:    "failed response",
			script:  "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"quota exhausted\"}}}\n\n",
			wantErr: "quota exhausted",
		},
		{
			name:    "bare error event",
			script:  "data: {\"type\":\"error\",\"message\":\"bad request shape\"}\n\n",
			wantErr: "bad request shape",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeSSE(w, tc.script)
			}))
			defer server.Close()

			client := llm.NewResponses(server.URL, "key")
			reply, err := client.Chat(context.Background(), "gpt-test", nil, nil, nil)

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
			if !reflect.DeepEqual(reply.Usage, tc.wantUsage) {
				t.Fatalf("usage = %#v, want %#v", reply.Usage, tc.wantUsage)
			}
			if len(reply.ToolCalls) != len(tc.wantCalls) {
				t.Fatalf("tool calls = %#v, want %#v", reply.ToolCalls, tc.wantCalls)
			}
			for i, want := range tc.wantCalls {
				if !reflect.DeepEqual(reply.ToolCalls[i], want) {
					t.Fatalf("tool call %d = %#v, want %#v", i, reply.ToolCalls[i], want)
				}
			}
		})
	}
}

func TestResponsesRequestShape(t *testing.T) {
	var body map[string]any
	var header http.Header
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Clone()
		path = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		writeSSE(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()

	client := llm.NewResponses(server.URL, "secret")
	client.MaxOutputTokens = 1024

	tools := []llm.ToolSpec{{Type: "function"}}
	tools[0].Function.Name = "read"
	tools[0].Function.Description = "read a file"
	tools[0].Function.Parameters = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)

	messages := []llm.Message{
		{Role: "system", Content: "be terse"},
		{Role: "user", Content: "read it"},
		{Role: "assistant", Content: "sure", ToolCalls: []llm.ToolCall{{
			ID:       "call_1",
			Type:     "function",
			Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"a.rs"}`},
		}}},
		{Role: "tool", ToolCallID: "call_1", Content: "fn main() {}"},
	}

	if _, err := client.Chat(context.Background(), "gpt-test", messages, tools, nil); err != nil {
		t.Fatalf("chat: %v", err)
	}

	if path != "/v1/responses" {
		t.Fatalf("path = %q, want /v1/responses", path)
	}
	if got := header.Get("Authorization"); got != "Bearer secret" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := header.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("Accept = %q", got)
	}
	if got := body["model"]; got != "gpt-test" {
		t.Fatalf("model = %#v", got)
	}
	if got := body["instructions"]; got != "be terse" {
		t.Fatalf("instructions = %#v", got)
	}
	if got := body["max_output_tokens"]; got != float64(1024) {
		t.Fatalf("max_output_tokens = %#v", got)
	}
	if got := body["stream"]; got != true {
		t.Fatalf("stream = %#v", got)
	}
	if got := body["store"]; got != false {
		t.Fatalf("store = %#v, want false", got)
	}

	toolSpecs, _ := body["tools"].([]any)
	if len(toolSpecs) != 1 {
		t.Fatalf("tools = %#v", body["tools"])
	}
	tool, _ := toolSpecs[0].(map[string]any)
	if tool["type"] != "function" || tool["name"] != "read" {
		t.Fatalf("tool = %#v", tool)
	}
	if _, ok := tool["parameters"].(map[string]any); !ok {
		t.Fatalf("tool parameters = %#v", tool["parameters"])
	}
	if _, nested := tool["function"]; nested {
		t.Fatalf("tool should be flattened, got %#v", tool)
	}

	input, _ := body["input"].([]any)
	if len(input) != 4 {
		t.Fatalf("input = %#v, want user, assistant text, function_call, function_call_output", input)
	}
	call, _ := input[2].(map[string]any)
	if call["type"] != "function_call" || call["call_id"] != "call_1" || call["name"] != "read" {
		t.Fatalf("function_call item = %#v", call)
	}
	output, _ := input[3].(map[string]any)
	if output["type"] != "function_call_output" || output["call_id"] != "call_1" || output["output"] != "fn main() {}" {
		t.Fatalf("function_call_output item = %#v", output)
	}
	user, _ := input[0].(map[string]any)
	parts, _ := user["content"].([]any)
	part, _ := parts[0].(map[string]any)
	if part["type"] != "input_text" {
		t.Fatalf("user content part = %#v", part)
	}
}

func TestResponsesErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"model not found"}}`)
	}))
	defer server.Close()

	_, err := llm.NewResponses(server.URL, "k").Chat(context.Background(), "m", nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("err = %v, want 400 with body", err)
	}
}

func TestResponsesRetriesServerError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, "try later")
			return
		}
		writeSSE(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer server.Close()

	client := llm.NewResponses(server.URL, "k")
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
