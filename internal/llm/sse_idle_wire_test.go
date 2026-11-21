package llm_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// wireIdleCase 描述一个协议的请求入口、固定 SSE 脚本和期望回复文本。
type wireIdleCase struct {
	name   string
	build  func(url string, timeout time.Duration) llm.Chatter
	script string
	want   string
}

// TestIdleTimeoutLeavesWireBytesUnchanged 检查把 IdleTimeout 从 0 改成正值
// 不会改变任何协议在线路上发送的字节，也不会改变同一份 SSE 字节解析出的回复。
// 共享读取器只改变"何时放弃等待静默连接"，不触碰请求构建和事件解码。
func TestIdleTimeoutLeavesWireBytesUnchanged(t *testing.T) {
	cases := []wireIdleCase{
		{
			name: "chat completions",
			build: func(url string, timeout time.Duration) llm.Chatter {
				c := llm.New(url, "")
				c.IdleTimeout = timeout
				return c
			},
			script: "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\" world\"}}]}\n\n" +
				"data: [DONE]\n\n",
			want: "Hello world",
		},
		{
			name: "anthropic messages",
			build: func(url string, timeout time.Duration) llm.Chatter {
				c := llm.NewAnthropic(url, "")
				c.IdleTimeout = timeout
				return c
			},
			script: "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" world\"}}\n\n" +
				"data: {\"type\":\"message_stop\"}\n\n",
			want: "Hello world",
		},
		{
			name: "responses",
			build: func(url string, timeout time.Duration) llm.Chatter {
				c := llm.NewResponses(url, "")
				c.IdleTimeout = timeout
				return c
			},
			script: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\" world\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n",
			want: "Hello world",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan []byte, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				requests <- body
				writeSSE(w, tc.script)
			}))
			defer server.Close()

			// 0 是旧同步路径；5s 足够长，正常回复不会触发 idle。
			var replies [2]llm.Message
			var requestBytes [2][]byte
			for i, timeout := range []time.Duration{0, 5 * time.Second} {
				reply, err := tc.build(server.URL, timeout).Chat(context.Background(), "m", nil, nil, nil)
				if err != nil {
					t.Fatalf("Chat(timeout=%s): %v", timeout, err)
				}
				replies[i] = reply
				requestBytes[i] = <-requests
			}

			if !bytes.Equal(requestBytes[0], requestBytes[1]) {
				t.Fatalf("request bytes changed:\n sync = %q\n idle = %q", requestBytes[0], requestBytes[1])
			}
			if !reflect.DeepEqual(replies[0], replies[1]) {
				t.Fatalf("reply changed:\n sync = %+v\n idle = %+v", replies[0], replies[1])
			}
			if replies[0].Content != tc.want {
				t.Fatalf("content = %q, want %q", replies[0].Content, tc.want)
			}
		})
	}
}
