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

// captureThinkingWire 启动一个 httptest 端点，回一段最小 SSE 流，
// 并把收到的原始请求体写入返回的指针，供逐字节断言。
func captureThinkingWire(t *testing.T) (*string, *llm.Client) {
	t.Helper()
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("读取请求体失败: %v", err)
			return
		}
		body = string(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return &body, llm.New(srv.URL, "k")
}

// TestChatThinkingNilKeepsGoldenBytes 锁定默认线协议字节：Options.Thinking 为 nil 时，
// 请求体必须与接线前逐字节一致（不含 reasoning_effort），
// 防止字段顺序或格式在接线过程中发生漂移。
func TestChatThinkingNilKeepsGoldenBytes(t *testing.T) {
	body, c := captureThinkingWire(t)
	if _, err := c.Chat(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	const golden = `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`
	if *body != golden {
		t.Fatalf("默认请求字节漂移\n got: %s\nwant: %s", *body, golden)
	}
	if strings.Contains(*body, "reasoning_effort") {
		t.Fatalf("默认请求不应含 reasoning_effort: %s", *body)
	}
}

// TestChatThinkingEffortWire 验证 Thinking.Effort 被映射为 reasoning_effort，
// 且请求体仍是合法 JSON、字段值正确。
func TestChatThinkingEffortWire(t *testing.T) {
	body, c := captureThinkingWire(t)
	c.Options.Thinking = &llm.ThinkingConfig{Effort: "high"}
	if _, err := c.Chat(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(*body, `"reasoning_effort":"high"`) {
		t.Fatalf("请求体缺少 reasoning_effort=high: %s", *body)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(*body), &decoded); err != nil {
		t.Fatalf("请求体不是合法 JSON: %v; body=%s", err, *body)
	}
	if decoded["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %#v, want high", decoded["reasoning_effort"])
	}
}

// TestFillMiddleNeverSendsReasoningEffort 确认 FIM 路径永不携带推理参数：
// 即使客户端设置了 Thinking.Effort，FillMiddle 仍传空串，请求体不含
// reasoning_effort；同一客户端的普通 Chat 则携带它，证明隔离来自
// FIM 显式传空串而非客户端配置缺失。
func TestFillMiddleNeverSendsReasoningEffort(t *testing.T) {
	body, c := captureThinkingWire(t)
	c.Options.Thinking = &llm.ThinkingConfig{Effort: "high"}

	if _, err := c.FillMiddle(context.Background(), "Qwen2.5-Coder", "pre", "post", llm.FillOptions{}); err != nil {
		t.Fatalf("FillMiddle: %v", err)
	}
	if strings.Contains(*body, "reasoning_effort") {
		t.Fatalf("FIM 请求不应含 reasoning_effort: %s", *body)
	}

	if _, err := c.Chat(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(*body, `"reasoning_effort":"high"`) {
		t.Fatalf("同一客户端的普通 Chat 应携带 reasoning_effort: %s", *body)
	}
}
