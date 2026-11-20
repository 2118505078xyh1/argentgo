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

// TestRenderFIMTemplates pins the exact wire prompt and explicit stop ids each
// model family produces. A wrong marker or stop id makes the endpoint generate
// the wrong span or never terminate the infill.
func TestRenderFIMTemplates(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		want    string
		wantIDs []int64
	}{
		{
			name:    "qwen",
			model:   "Qwen2.5-Coder-7B-Instruct",
			want:    "<|fim_prefix|>pre<|fim_suffix|>post<|fim_middle|>",
			wantIDs: []int64{151643},
		},
		{
			name:    "qwen case insensitive",
			model:   "QWEN2.5-CODER",
			want:    "<|fim_prefix|>pre<|fim_suffix|>post<|fim_middle|>",
			wantIDs: []int64{151643},
		},
		{
			name:    "deepseek",
			model:   "deepseek-coder-6.7b",
			want:    "<｜fim▁begin｜>pre<｜fim▁hole｜>post<｜fim▁end｜>",
			wantIDs: []int64{128805},
		},
		{
			name:    "starcoder via bigcode alias",
			model:   "bigcode/starcoder2:15b",
			want:    "<fim_prefix>pre<fim_suffix>post<fim_middle>",
			wantIDs: []int64{0},
		},
		{
			name:    "codestral",
			model:   "codestral-latest",
			want:    "[SUFFIX]post[PREFIX]pre",
			wantIDs: []int64{2},
		},
		{
			name:    "codellama",
			model:   "codellama/CodeLlama-13b",
			want:    "<PRE> pre <SUF>post <MID>",
			wantIDs: []int64{0},
		},
		{
			name:    "unknown falls back to text",
			model:   "gpt-4o",
			want:    "prepost",
			wantIDs: nil,
		},
		{
			name:    "empty model falls back to text",
			model:   "",
			want:    "prepost",
			wantIDs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt, stopIDs := llm.RenderFIM("pre", "post", tt.model)
			if prompt != tt.want {
				t.Fatalf("prompt = %q, want %q", prompt, tt.want)
			}
			if !reflect.DeepEqual(stopIDs, tt.wantIDs) {
				t.Fatalf("stopIDs = %v, want %v", stopIDs, tt.wantIDs)
			}
		})
	}
}

// TestRenderFIMPreservesCJKAndMarkers checks that bilingual code and text that
// merely looks like a control token reach the wire verbatim: RenderFIM only
// adds its own markers and must not escape or reinterpret caller content.
func TestRenderFIMPreservesCJKAndMarkers(t *testing.T) {
	prefix := "func 你好(x int) {\n  // 注意 <fim_middle> 不是真实标记\n"
	suffix := "\n  return \"世界\"\n}"
	prompt, stopIDs := llm.RenderFIM(prefix, suffix, "Qwen2.5-Coder")
	want := "<|fim_prefix|>" + prefix + "<|fim_suffix|>" + suffix + "<|fim_middle|>"
	if prompt != want {
		t.Fatalf("prompt = %q, want %q", prompt, want)
	}
	if !reflect.DeepEqual(stopIDs, []int64{151643}) {
		t.Fatalf("stopIDs = %v, want [151643]", stopIDs)
	}
}

// TestRenderFIMReturnsIndependentStopIDs guards the shared template table: a
// caller mutating the returned slice must not poison later renders.
func TestRenderFIMReturnsIndependentStopIDs(t *testing.T) {
	_, first := llm.RenderFIM("a", "b", "qwen")
	first[0] = 999
	_, second := llm.RenderFIM("a", "b", "qwen")
	if !reflect.DeepEqual(second, []int64{151643}) {
		t.Fatalf("stopIDs after caller mutation = %v, want [151643]", second)
	}
}

// TestFIMTemplateName checks the diagnostic selector resolves the same families
// as RenderFIM, including the unknown-model fallback.
func TestFIMTemplateName(t *testing.T) {
	tests := []struct{ model, want string }{
		{"Qwen2.5-Coder", "qwen"},
		{"DEEPSEEK-coder", "deepseek"},
		{"starcoder2", "starcoder"},
		{"Codestral", "codestral"},
		{"CodeLlama-7b", "codellama"},
		{"mystery-model", "text"},
	}
	for _, tt := range tests {
		if got := llm.FIMTemplateName(tt.model); got != tt.want {
			t.Fatalf("FIMTemplateName(%q) = %q, want %q", tt.model, got, tt.want)
		}
	}
}

// captureChatRequest serves a minimal SSE reply and records the raw request
// body so wire-shape assertions can be made on it.
func captureChatRequest(t *testing.T, reply string) (*string, *llm.Client) {
	t.Helper()
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
		}
		body = string(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return &body, llm.New(srv.URL, "k")
}

// TestChatDoesNotSendStopIDs is the no-regression check: Chat shares the new
// pipeline but its request must stay byte-compatible (no stop_token_ids key).
func TestChatDoesNotSendStopIDs(t *testing.T) {
	reply := sse("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n", "data: [DONE]\n\n")
	body, c := captureChatRequest(t, reply)
	if _, err := c.Chat(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(*body, "stop_token_ids") {
		t.Fatalf("Chat request unexpectedly contains stop_token_ids: %s", *body)
	}
}

// TestFillMiddleSendsFIMAndStopIDs checks the full wire effect: the FIM prompt
// becomes the user message, the template stop ids are sent, deltas stream back,
// and the assembled infill is returned.
func TestFillMiddleSendsFIMAndStopIDs(t *testing.T) {
	reply := sse(
		"data: {\"choices\":[{\"delta\":{\"content\":\"mid\"},\"finish_reason\":\"stop\"}]}\n\n",
		"data: [DONE]\n\n",
	)
	body, c := captureChatRequest(t, reply)
	var streamed []string
	msg, err := c.FillMiddle(context.Background(), "Qwen2.5-Coder", "pre", "post", llm.FillOptions{
		OnText: func(s string) { streamed = append(streamed, s) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "mid" {
		t.Fatalf("content = %q, want mid", msg.Content)
	}
	if !reflect.DeepEqual(streamed, []string{"mid"}) {
		t.Fatalf("streamed = %v, want [mid]", streamed)
	}

	var req struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		StopTokenIDs []int64 `json:"stop_token_ids"`
		Stream       bool    `json:"stream"`
	}
	if err := json.Unmarshal([]byte(*body), &req); err != nil {
		t.Fatalf("decode request: %v\nbody: %s", err, *body)
	}
	wantPrompt := "<|fim_prefix|>pre<|fim_suffix|>post<|fim_middle|>"
	if len(req.Messages) != 1 || req.Messages[0].Role != "user" || req.Messages[0].Content != wantPrompt {
		t.Fatalf("messages = %+v, want one user message with %q", req.Messages, wantPrompt)
	}
	if !reflect.DeepEqual(req.StopTokenIDs, []int64{151643}) {
		t.Fatalf("stop_token_ids = %v, want [151643]", req.StopTokenIDs)
	}
	if !req.Stream {
		t.Fatal("stream = false, want true")
	}
}

// TestFillMiddleStopOverride checks that a caller-supplied stop list replaces
// the template default on the wire.
func TestFillMiddleStopOverride(t *testing.T) {
	reply := sse("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n", "data: [DONE]\n\n")
	body, c := captureChatRequest(t, reply)
	if _, err := c.FillMiddle(context.Background(), "qwen", "a", "b", llm.FillOptions{
		StopTokenIDs: []int64{7, 8},
	}); err != nil {
		t.Fatal(err)
	}
	var req struct {
		StopTokenIDs []int64 `json:"stop_token_ids"`
	}
	if err := json.Unmarshal([]byte(*body), &req); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(req.StopTokenIDs, []int64{7, 8}) {
		t.Fatalf("stop_token_ids = %v, want [7 8]", req.StopTokenIDs)
	}
}

// TestFillMiddleNoNetworkIsConstructionOnly checks the network-free mode: the
// rendered prompt is reported and the endpoint is never contacted.
func TestFillMiddleNoNetworkIsConstructionOnly(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
	}))
	defer srv.Close()
	c := llm.New(srv.URL, "k")
	msg, err := c.FillMiddle(context.Background(), "qwen", "pre", "post", llm.FillOptions{NoNetwork: true})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "<|fim_prefix|>pre<|fim_suffix|>post<|fim_middle|>" {
		t.Fatalf("content = %q, want rendered FIM prompt", msg.Content)
	}
	if msg.Usage != nil || msg.FinishReason != "" {
		t.Fatalf("construction-only reply carries wire metadata: %+v", msg)
	}
	if calls != 0 {
		t.Fatalf("endpoint called %d times in NoNetwork mode, want 0", calls)
	}
}

// TestFillMiddleUnknownModelUsesPlainText checks that an unrecognised model
// still produces a usable request with no forced stop ids.
func TestFillMiddleUnknownModelUsesPlainText(t *testing.T) {
	reply := sse("data: {\"choices\":[{\"delta\":{\"content\":\"mid\"}}]}\n\n", "data: [DONE]\n\n")
	body, c := captureChatRequest(t, reply)
	if _, err := c.FillMiddle(context.Background(), "totally-unknown", "pre", "post", llm.FillOptions{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(*body, "stop_token_ids") {
		t.Fatalf("plain-text fallback request contains stop_token_ids: %s", *body)
	}
	if !strings.Contains(*body, `"content":"prepost"`) {
		t.Fatalf("plain-text fallback prompt not found in request: %s", *body)
	}
}
