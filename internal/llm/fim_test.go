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

	"example.com/argentgo/internal/llm"
)

// TestRenderFIMTemplates 固定每个模型系列产生的精确线协议提示和显式停止 id。
// 错误的标记或停止 id 会让端点生成错误的片段范围，
// 或永远不终止填充。
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

// TestRenderFIMPreservesCJKAndMarkers 检查双语代码以及只是看起来像控制 token 的文本
// 是否原样到达线协议：RenderFIM 只添加自己的标记，
// 绝不能转义或重新解释调用方内容。
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

// TestRenderFIMReturnsIndependentStopIDs 守护共享的模板表：调用方修改
// 返回的切片不得污染后续的渲染。
func TestRenderFIMReturnsIndependentStopIDs(t *testing.T) {
	_, first := llm.RenderFIM("a", "b", "qwen")
	first[0] = 999
	_, second := llm.RenderFIM("a", "b", "qwen")
	if !reflect.DeepEqual(second, []int64{151643}) {
		t.Fatalf("stopIDs after caller mutation = %v, want [151643]", second)
	}
}

// TestFIMTemplateName 检查诊断选择器解析出的系列与 RenderFIM 一致，
// 包括未知模型的回退。
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

// captureChatRequest 提供一个最小的 SSE 回复并记录原始请求体，
// 以便对其做线协议形态断言。
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

// TestChatDoesNotSendStopIDs 是无回归检查：Chat 共享新的管线，
// 但其请求必须保持字节兼容（不含 stop_token_ids 键）。
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

// TestFillMiddleSendsFIMAndStopIDs 检查完整的线协议效果：FIM 提示
// 成为 user 消息，模板的停止 id 被发送，增量流式返回，
// 并返回组装好的填充内容。
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

// TestFillMiddleStopOverride 检查调用方提供的停止列表在线协议上
// 替换模板的默认值。
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

// TestFillMiddleNoNetworkIsConstructionOnly 检查无网络模式：报告渲染后的提示，
// 且绝不联系端点。
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

// TestFillMiddleUnknownModelUsesPlainText 检查无法识别的模型
// 仍产生可用的请求，且不强制添加停止 id。
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
