package llm_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestWireClientsKeepRetryPolicyHookWhenItIsTheOnlyField covers the fallout of
// an IsZero that ignored function fields: a caller that configures only an
// OnRetry hook (a natural way to subscribe to retry notifications without
// touching delays) had its whole policy replaced by DefaultRetryPolicy, so the
// hook never fired. The retry itself still happens, which makes the silent
// loss easy to miss. All three wire clients share this pipeline.
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
