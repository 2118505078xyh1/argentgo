package llm_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// idleChatterCase 用公共 Chat 入口覆盖三个协议：
// 该字段经各自客户端接线，一个已死但保持打开的连接会变成 stream_error
// 尝试（可重试类别），而不是一直挂起到 context 过期。
type idleChatterCase struct {
	name   string
	build  func(url string, timeout time.Duration, hook func(llm.AttemptRecord)) llm.Chatter
	script string
}

func idleChatterCases() []idleChatterCase {
	return []idleChatterCase{
		{
			name: "chat completions",
			build: func(url string, timeout time.Duration, hook func(llm.AttemptRecord)) llm.Chatter {
				c := llm.New(url, "")
				c.IdleTimeout = timeout
				c.Options.AttemptHook = hook
				return c
			},
			script: `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n",
		},
		{
			name: "anthropic messages",
			build: func(url string, timeout time.Duration, hook func(llm.AttemptRecord)) llm.Chatter {
				c := llm.NewAnthropic(url, "")
				c.IdleTimeout = timeout
				c.Options.AttemptHook = hook
				return c
			},
			script: `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n",
		},
		{
			name: "responses",
			build: func(url string, timeout time.Duration, hook func(llm.AttemptRecord)) llm.Chatter {
				c := llm.NewResponses(url, "")
				c.IdleTimeout = timeout
				c.Options.AttemptHook = hook
				return c
			},
			script: `data: {"type":"response.output_text.delta","delta":"hi"}` + "\n\n",
		},
	}
}

func TestChatIdleTimeoutSurfacedAsStreamError(t *testing.T) {
	for _, tc := range idleChatterCases() {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, tc.script)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				<-r.Context().Done() // 保持连接但静默
			}))
			defer server.Close()

			var records []llm.AttemptRecord
			client := tc.build(server.URL, 80*time.Millisecond, func(rec llm.AttemptRecord) {
				records = append(records, rec)
			})

			_, err := client.Chat(context.Background(), "m", nil, nil, nil)
			if !errors.Is(err, llm.ErrStreamError) {
				t.Fatalf("err = %v, want ErrStreamError", err)
			}
			if got := llm.ClassifyAPIError(err); got == nil || got.Family != llm.FamilyNetwork {
				t.Fatalf("ClassifyAPIError = %+v, want FamilyNetwork", got)
			}
			if len(records) != 1 || records[0].Kind != llm.AttemptStreamError {
				t.Fatalf("attempt records = %+v, want one stream_error", records)
			}
			if !strings.Contains(records[0].ErrorClass, "idle") {
				t.Fatalf("error class = %q, want the idle reason", records[0].ErrorClass)
			}
		})
	}
}
