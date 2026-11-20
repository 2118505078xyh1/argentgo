package llm_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestRetryPolicyNeverReplaysSuccessfulResponse covers a gateway that marks a
// successful 2xx response with x-should-retry: true (a misconfigured or
// copy-forwarded header). A completed request must never be replayed: doing so
// duplicates billing and any side effects the first call already committed.
// The verdict header may only override the status-code heuristic on an error
// response; a success is always terminal with its body intact.
func TestRetryPolicyNeverReplaysSuccessfulResponse(t *testing.T) {
	cases := []struct {
		name   string
		status int
	}{
		{"200 with retry verdict", http.StatusOK},
		{"201 with retry verdict", http.StatusCreated},
		{"204 with retry verdict", http.StatusNoContent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sends := 0
			policy := llm.RetryPolicy{MaxAttempts: 3, Sleep: retryHeaderNoWait}
			resp, err := policy.Do(context.Background(), func(context.Context) (*http.Response, error) {
				sends++
				return &http.Response{
					StatusCode: tc.status,
					Status:     "success",
					Header:     http.Header{"X-Should-Retry": []string{"true"}},
					Body:       io.NopCloser(strings.NewReader("original success body")),
				}, nil
			})
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			if sends != 1 {
				t.Fatalf("sends = %d, want 1: a successful response must never be replayed", sends)
			}
			if resp == nil {
				t.Fatal("Do returned a nil response")
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if string(body) != "original success body" {
				t.Fatalf("body = %q, want the original success body intact", body)
			}
		})
	}
}
