package llm_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestWireClientsExposeFinishReason 检查每个协议客户端是否把提供方的
// 停止/截断原因捕获到 llm.Message 上。agent 循环用它
// 拒绝参数可能被长度截断的工具批次。
func TestWireClientsExposeFinishReason(t *testing.T) {
	t.Run("anthropic", func(t *testing.T) {
		script := sse("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n",
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"},\"usage\":{\"output_tokens\":2}}\n\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, script)
		}))
		defer srv.Close()
		c := llm.NewAnthropic(srv.URL+"/v1", "k")
		msg, err := c.Chat(context.Background(), "m", nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if msg.FinishReason != "max_tokens" {
			t.Fatalf("finish reason = %q, want max_tokens", msg.FinishReason)
		}
	})

	t.Run("chat", func(t *testing.T) {
		script := sse("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"length\"}]}\n\n",
			"data: [DONE]\n\n")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, script)
		}))
		defer srv.Close()
		c := llm.New(srv.URL+"/v1", "k")
		msg, err := c.Chat(context.Background(), "m", nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if msg.FinishReason != "length" {
			t.Fatalf("finish reason = %q, want length", msg.FinishReason)
		}
	})
}

func sse(parts ...string) string { return strings.Join(parts, "") }
