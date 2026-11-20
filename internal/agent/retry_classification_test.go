package agent

import (
	"context"
	"errors"
	"net"
	"testing"
)

// timeoutError 是一个设置了 Timeout() 的 net.Error，但未包装 context.DeadlineExceeded（回退层把它视为最终错误）。
// （接上行）
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// TestIsRetryableProviderError 锁定类型化的 llm 重试分类与旧版提供方文本词汇的并集。期望值被直接写出，
// 使任一信号的变化都能在这里可见。
// （接上行）
func TestIsRetryableProviderError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"429 status", errors.New("HTTP 429: too many requests"), true},
		{"5xx status", errors.New("provider returned 503: service unavailable"), true},
		{"5xx prose without a numeric status", errors.New("upstream is 5xx right now"), true},
		{"quota text", errors.New("quota exceeded for this account"), true},
		{"typed timeout error", timeoutError{}, true},
		{"unreachable host", &net.DNSError{Err: "no such host", Name: "api.example.com", IsNotFound: true}, true},
		{"connection refused text", errors.New("dial tcp: connection refused"), true},
		{"network operation with unknown cause", &net.OpError{Op: "read", Net: "tcp", Err: errors.New("unexpected EOF")}, true},
		{"validation error", errors.New("400 invalid request: unknown role"), false},
		{"unauthorized", errors.New("401 unauthorized: invalid key"), false},
		{"cancellation", context.Canceled, false},
		{"deadline exceeded is final", context.DeadlineExceeded, false},
		{"unknown application error", errors.New("record not found"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryableProviderError(tt.err); got != tt.want {
				t.Fatalf("isRetryableProviderError(%q) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
