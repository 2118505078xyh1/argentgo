package llm_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestRetryPolicyNeverReplaysSuccessfulResponse 覆盖这样的网关：它用
// x-should-retry: true 标记一个成功的 2xx 响应（配置错误或
// 转发而来的头部）。已完成的请求绝不能被重放：那样会
// 重复计费以及第一次调用已经提交的所有副作用。
// 判定头部只能在错误响应上覆盖状态码启发式；
// 成功响应始终是终态，且响应体完整。
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
