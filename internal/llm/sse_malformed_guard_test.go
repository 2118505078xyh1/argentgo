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

// malformedCases 把三协议的真实客户端构造器与一条"合法文本增量帧"组织在一起，
// 两个测试共用，确保畸形载荷守卫针对的是同一组线上客户端。
var malformedCases = []struct {
	name      string
	newClient func(string) llm.Chatter
	goodFrame string
}{
	{
		name:      "chat-completions",
		newClient: func(u string) llm.Chatter { return llm.New(u, "test-key") },
		goodFrame: `data: {"choices":[{"delta":{"content":"ok"}}]}` + "\n\n",
	},
	{
		name:      "anthropic-messages",
		newClient: func(u string) llm.Chatter { return llm.NewAnthropic(u, "test-key") },
		goodFrame: `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}` + "\n\n",
	},
	{
		name:      "responses",
		newClient: func(u string) llm.Chatter { return llm.NewResponses(u, "test-key") },
		goodFrame: `data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n",
	},
}

// TestMalformedSSEAlwaysStreamError 参数化验证三客户端对畸形 SSE 的统一守卫（协议审计 B/C）：
// 非 JSON、截断 JSON、超过 sseReader 4MB 上限的长行都必须返回 ErrStreamError；
// 畸形载荷不会进入 Content/ToolCalls，也不会被当作半行答复经 onText 泄漏。
func TestMalformedSSEAlwaysStreamError(t *testing.T) {
	oversize := "data: " + strings.Repeat("x", 4*1024*1024+4096) + "\n\n"
	payloads := []struct {
		name string
		wire string
	}{
		{name: "not-json", wire: "data: {not json}\n\n"},
		{name: "truncated-json", wire: "data: {\"choices\":[{\"delta\":{\"content\":\"半行" + "\n\n"},
		{name: "oversize-line", wire: oversize},
	}
	for _, tc := range malformedCases {
		for _, p := range payloads {
			t.Run(tc.name+"/"+p.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					writeSSE(w, p.wire)
				}))
				defer server.Close()

				var deltas strings.Builder
				reply, err := tc.newClient(server.URL).Chat(context.Background(), "m", nil, nil, func(d string) {
					deltas.WriteString(d)
				})
				if !errors.Is(err, llm.ErrStreamError) {
					t.Fatalf("err = %v, want ErrStreamError", err)
				}
				if reply.Content != "" || len(reply.ToolCalls) != 0 {
					t.Fatalf("畸形载荷泄漏进答复: Content=%q ToolCalls=%+v", reply.Content, reply.ToolCalls)
				}
				if deltas.Len() != 0 {
					t.Fatalf("畸形载荷被当作半行答复流出: deltas=%q", deltas.String())
				}
			})
		}
	}
}

// TestMalformedSSEAfterDoneIsIgnored 验证终止符后的垃圾不污染答复：
// 合法增量正常入库，[DONE] 之后的畸形帧被忽略，Content/ToolCalls 只含合法内容。
func TestMalformedSSEAfterDoneIsIgnored(t *testing.T) {
	for _, tc := range malformedCases {
		t.Run(tc.name, func(t *testing.T) {
			wire := tc.goodFrame +
				"data: [DONE]\n\n" +
				"data: {not json}\n\n" +
				"garbage not even an SSE field\n\n"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeSSE(w, wire)
			}))
			defer server.Close()

			var deltas strings.Builder
			reply, err := tc.newClient(server.URL).Chat(context.Background(), "m", nil, nil, func(d string) {
				deltas.WriteString(d)
			})
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if reply.Content != "ok" {
				t.Fatalf("Content = %q, want ok", reply.Content)
			}
			if len(reply.ToolCalls) != 0 {
				t.Fatalf("ToolCalls = %+v, want none", reply.ToolCalls)
			}
			if deltas.String() != "ok" {
				t.Fatalf("deltas = %q, want ok", deltas.String())
			}
		})
	}
}
