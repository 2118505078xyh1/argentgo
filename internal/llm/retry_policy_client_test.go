package llm_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestWireClientsKeepRetryPolicyHookWhenItIsTheOnlyField 覆盖这样的后果：
// 一个忽略函数字段的 IsZero 会让只配置了
// OnRetry 钩子的调用方（这是订阅重试通知而不触碰延迟的自然方式）
// 的整个策略被替换为 DefaultRetryPolicy，导致钩子永不触发。
// 重试本身仍会发生，因此这种静默丢失很容易被忽视。
// 三个线客户端共享这条管线。
func TestWireClientsKeepRetryPolicyHookWhenItIsTheOnlyField(t *testing.T) {
	cases := []struct {
		name      string
		newClient func(url string, policy llm.RetryPolicy) llm.Chatter
	}{
		{
			name: "chat completions",
			newClient: func(url string, policy llm.RetryPolicy) llm.Chatter {
				c := llm.New(url, "")
				c.Retry = policy
				return c
			},
		},
		{
			name: "anthropic messages",
			newClient: func(url string, policy llm.RetryPolicy) llm.Chatter {
				c := llm.NewAnthropic(url, "")
				c.Retry = policy
				return c
			},
		},
		{
			name: "responses",
			newClient: func(url string, policy llm.RetryPolicy) llm.Chatter {
				c := llm.NewResponses(url, "")
				c.Retry = policy
				return c
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				mu.Lock()
				requests++
				n := requests
				mu.Unlock()
				if n == 1 {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = io.WriteString(w, `{"error":{"message":"temporary"}}`)
					return
				}
				writeSSE(w, "data: [DONE]\n\n")
			}))
			defer server.Close()

			var attempts []llm.RetryAttempt
			hook := func(a llm.RetryAttempt) {
				attempts = append(attempts, a)
			}
			ch := tc.newClient(server.URL, llm.RetryPolicy{OnRetry: hook})

			if _, err := ch.Chat(context.Background(), "m", nil, nil, nil); err != nil {
				t.Fatalf("Chat after one retry: %v", err)
			}
			mu.Lock()
			gotRequests := requests
			mu.Unlock()
			if gotRequests != 2 {
				t.Fatalf("server requests = %d, want 2 (one retry)", gotRequests)
			}
			if len(attempts) != 1 {
				t.Fatalf("OnRetry calls = %d, want 1: %+v", len(attempts), attempts)
			}
			if attempts[0].Reason != llm.ReasonStatus || attempts[0].StatusCode != http.StatusInternalServerError {
				t.Fatalf("retry attempt = %+v, want status-retry for 500", attempts[0])
			}
		})
	}
}
