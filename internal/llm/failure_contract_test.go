package llm_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestIsContextOverflow 覆盖上下文溢出的各类文案、相邻概念（输出长度、
// 字节级溢出）以及哨兵优先的边界，确认导出门面与权威分类器一致。
func TestIsContextOverflow(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"anthropic", errors.New("prompt is too long: 250000 tokens > 200000 tokens"), true},
		{"openai", errors.New("provider returned 400 Bad Request: context_length_exceeded"), true},
		{"window exceeded", errors.New("context window exceeded"), true},
		{"zh", errors.New("上下文窗口已用尽"), true},
		{"actionable", llm.ClassifyAPIError(errors.New("context window exceeded")), true},
		{"wrapped", fmt.Errorf("chat: %w", errors.New("context window exceeded")), true},
		{"byte size overflow", errors.New("request body too large"), false},
		{"output token limit", errors.New("max_tokens must be less than 4096"), false},
		{"auth", errors.New("provider returned 401 Unauthorized"), false},
		{"cancel", context.Canceled, false},
		// 哨兵先于文本：即便措辞像溢出，DNS 失败仍是 transient。
		{"dns wins over overflow wording", fmt.Errorf("context window exceeded: %w", &net.DNSError{Err: "no such host", Name: "api.example.com"}), false},
		{"deadline wins over overflow wording", fmt.Errorf("context window exceeded: %w", context.DeadlineExceeded), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := llm.IsContextOverflow(tc.err); got != tc.want {
				t.Errorf("IsContextOverflow(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestIsByteSizeOverflowExported 确认导出门面与包内识别器行为一致。
func TestIsByteSizeOverflowExported(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"request body too large", true},
		{"请求体过大", true},
		{"", false},
		{"context window exceeded", false},
		{"max_tokens must be less than 4096", false},
	}
	for _, tc := range cases {
		t.Run(tc.msg, func(t *testing.T) {
			if got := llm.IsByteSizeOverflow(tc.msg); got != tc.want {
				t.Errorf("IsByteSizeOverflow(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}

// TestIsRecoverableStreamErrorExported 确认导出门面覆盖明确流中断与误报边界。
func TestIsRecoverableStreamErrorExported(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"stream interrupted", true},
		{"响应被截断", true},
		{"", false},
		{"connection refused", false},
		{"context window exceeded", false},
	}
	for _, tc := range cases {
		t.Run(tc.msg, func(t *testing.T) {
			if got := llm.IsRecoverableStreamError(tc.msg); got != tc.want {
				t.Errorf("IsRecoverableStreamError(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}

// TestIsRetryableError 覆盖可重试错误门面的各类传输信号。
func TestIsRetryableError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"cancel", context.Canceled, false},
		{"deadline", context.DeadlineExceeded, true},
		{"dns", &net.DNSError{Err: "no such host", Name: "api.example.com"}, true},
		{"op error", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, true},
		{"stream", llm.ErrStreamError, true},
		{"context overflow", errors.New("context window exceeded"), false},
		{"plain failure", errors.New("some unrelated failure"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := llm.IsRetryableError(tc.err); got != tc.want {
				t.Errorf("IsRetryableError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestRetryReasonForResponseStatusCategories 逐类覆盖 HTTP 状态：2xx、3xx、
// 4xx（含硬性配额、终态、溢出）、5xx 与网关码，确认终态判定先于状态码。
func TestRetryReasonForResponseStatusCategories(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   llm.Reason
	}{
		{"no response", 0, "", llm.ReasonNone},
		{"200 ok", 200, "", llm.ReasonNone},
		{"301 redirect", 301, "", llm.ReasonNone},
		{"400 bad request", 400, "", llm.ReasonNone},
		{"401 auth", 401, "", llm.ReasonNone},
		{"404 model not found", 404, "", llm.ReasonNone},
		{"413 request body too large", 413, "request body too large", llm.ReasonNone},
		{"408 request timeout", 408, "", llm.ReasonStatus},
		{"409 conflict", 409, "", llm.ReasonStatus},
		{"425 too early", 425, "", llm.ReasonStatus},
		{"429 rate limit", 429, "rate limit exceeded", llm.ReasonStatus},
		{"429 hard quota", 429, "insufficient_quota: your quota has been exhausted", llm.ReasonNone},
		{"402 payment required", 402, "", llm.ReasonNone},
		{"500 internal", 500, "", llm.ReasonStatus},
		{"502 bad gateway", 502, "", llm.ReasonStatus},
		{"503 unavailable", 503, "", llm.ReasonStatus},
		{"504 gateway timeout", 504, "", llm.ReasonStatus},
		{"522 cloudflare", 522, "", llm.ReasonStatus},
		{"524 cloudflare", 524, "", llm.ReasonStatus},
		{"529 overloaded", 529, "", llm.ReasonStatus},
		{"500 terminal session budget", 500, "session_budget_exceeded", llm.ReasonNone},
		{"503 terminal hook denied", 503, "inference_hook_denied", llm.ReasonNone},
		{"500 context overflow body", 500, "context window exceeded", llm.ReasonNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := llm.RetryReasonForResponse(tc.status, []byte(tc.body))
			if got != tc.want {
				t.Errorf("RetryReasonForResponse(%d, %q) = %q, want %q", tc.status, tc.body, got, tc.want)
			}
		})
	}
}

// TestClassifyRetryCombined 确认统一入口在错误信号与响应信号同时存在时的
// 优先级：取消恒不重试，传输类别优先于响应，响应用于纯 HTTP 失败。
func TestClassifyRetryCombined(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		body   string
		want   llm.Reason
	}{
		{"nothing", nil, 0, "", llm.ReasonNone},
		{"cancel beats status", context.Canceled, 503, "", llm.ReasonNone},
		{"deadline beats status", context.DeadlineExceeded, 503, "", llm.ReasonTimeout},
		{"dns beats status", &net.DNSError{Err: "no such host", Name: "api.example.com"}, 503, "", llm.ReasonUnreachable},
		{"overflow beats status", errors.New("context window exceeded"), 503, "", llm.ReasonNone},
		{"status only", nil, 503, "", llm.ReasonStatus},
		{"hard quota only", nil, 429, "insufficient_quota", llm.ReasonNone},
		{"terminal only", nil, 500, "billing_hard_limit_reached", llm.ReasonNone},
		{"success only", nil, 200, "", llm.ReasonNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := llm.ClassifyRetry(tc.err, tc.status, []byte(tc.body))
			if got != tc.want {
				t.Errorf("ClassifyRetry(%v, %d, %q) = %q, want %q", tc.err, tc.status, tc.body, got, tc.want)
			}
		})
	}
}
