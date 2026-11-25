package llm_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestStreamTruncationWithoutTerminator 锁定严格终止符校验的 opt-in 语义：
// 服务端发出一个文本增量后直接关闭、不带任何协议终止符时，
// 默认配置容忍截断并返回逐字节不变的半截文本；strict 配置返回 ErrStreamError，
// 从而让 "文本中途 TCP 关闭" 这类最常见的截断能触发 agent 层的回补门。
func TestStreamTruncationWithoutTerminator(t *testing.T) {
	const partial = "partial"
	cases := []struct {
		name  string
		build func(url string) llm.Chatter
		// delta 是关闭前发出的唯一文本增量事件，刻意不含协议终止符
		// （chat 的 [DONE]、anthropic 的 message_stop、responses 的 response.completed）。
		delta string
	}{
		{
			name:  "chat completions",
			build: func(url string) llm.Chatter { return llm.New(url, "k") },
			delta: `data: {"choices":[{"delta":{"content":"` + partial + `"}}]}` + "\n\n",
		},
		{
			name:  "anthropic messages",
			build: func(url string) llm.Chatter { return llm.NewAnthropic(url, "k") },
			delta: `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + partial + `"}}` + "\n\n",
		},
		{
			name:  "responses",
			build: func(url string) llm.Chatter { return llm.NewResponses(url, "k") },
			delta: `data: {"type":"response.output_text.delta","delta":"` + partial + `"}` + "\n\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 截流服务器：写完一个文本增量即返回，连接关闭，无终止事件。
			newServer := func() *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, tc.delta)
				}))
			}

			// 默认（strict 关闭）：容忍截断，返回半截文本且内容不变。
			t.Run("tolerant by default", func(t *testing.T) {
				server := newServer()
				defer server.Close()
				reply, err := tc.build(server.URL).Chat(context.Background(), "m", nil, nil, nil)
				if err != nil {
					t.Fatalf("default chat: %v", err)
				}
				if reply.Content != partial {
					t.Fatalf("content = %q, want %q", reply.Content, partial)
				}
			})

			// strict 开启：有文本但未到达终止符 -> ErrStreamError。
			t.Run("strict returns stream error", func(t *testing.T) {
				server := newServer()
				defer server.Close()
				client := tc.build(server.URL)
				switch c := client.(type) {
				case *llm.Client:
					c.Options.StrictStreamTerminator = true
				case *llm.AnthropicClient:
					c.Options.StrictStreamTerminator = true
				case *llm.ResponsesClient:
					c.Options.StrictStreamTerminator = true
				default:
					t.Fatalf("unexpected chatter type %T", client)
				}
				_, err := client.Chat(context.Background(), "m", nil, nil, nil)
				if !errors.Is(err, llm.ErrStreamError) {
					t.Fatalf("strict chat err = %v, want ErrStreamError", err)
				}
			})
		})
	}
}
