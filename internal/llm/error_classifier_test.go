package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
)

// TestClassifyErrorByClass 覆盖每一类的代表性关键字，确保 ClassifyErrorClass
// 把文本映射到预期的 ErrorClass。
func TestClassifyErrorByClass(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want ErrorClass
	}{
		// rate limit
		{"429 status", "provider returned 429 Too Many Requests", classRateLimit},
		{"rate limit phrase", "rate limit exceeded, retry after 30s", classRateLimit},
		{"too many requests", "too_many_requests", classRateLimit},
		{"throttle", "request was throttled upstream", classRateLimit},
		// auth
		{"401 status", "provider returned 401 Unauthorized", classAuth},
		{"403 status", "provider returned 403 Forbidden", classAuth},
		{"unauthorized", "unauthorized: please log in", classAuth},
		{"unauthorised spelling", "request unauthorised", classAuth},
		{"invalid api key", "invalid api key provided", classAuth},
		{"invalid_api_key", "invalid_api_key", classAuth},
		{"api key invalid suffix", "The API key you provided is invalid", classAuth},
		{"authentication failed", "authentication failed", classAuth},
		// transient
		{"connection reset", "connection reset by peer", classTransient},
		{"timeout", "i/o timeout while reading response", classTransient},
		{"timed out", "request timed out", classTransient},
		{"502 status", "provider returned 502 Bad Gateway", classTransient},
		{"503 status", "provider returned 503 Service Unavailable", classTransient},
		{"504 status", "504 Gateway Timeout", classTransient},
		{"deadline exceeded", "context deadline exceeded", classTransient},
		// invalid request
		{"400 status", "provider returned 400 Bad Request", classInvalidRequest},
		{"invalid request", "invalid request body", classInvalidRequest},
		{"bad request", "bad request", classInvalidRequest},
		// server
		{"500 status", "provider returned 500 Internal Server Error", classServer},
		{"5xx generic", "upstream returned 507", classServer},
		{"server error text", "internal server error", classServer},
		// unknown
		{"plain failure", "request failed", classUnknown},
		{"empty text", "", classUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyErrorClass(errors.New(tc.msg)); got != tc.want {
				t.Fatalf("ClassifyErrorClass(%q) = %q, want %q", tc.msg, got, tc.want)
			}
		})
	}
}

// TestClassifyErrorChinese 确认简体中文关键字与英文等价，覆盖各类。
func TestClassifyErrorChinese(t *testing.T) {
	cases := []struct {
		msg  string
		want ErrorClass
	}{
		{"请求过于频繁，请稍后再试", classRateLimit},
		{"已触发限流", classRateLimit},
		{"认证失败，API 密钥无效", classAuth},
		{"登录已过期，请重新登录", classAuth},
		{"未授权访问", classAuth},
		{"连接被重置", classTransient},
		{"读取响应超时", classTransient},
		{"上游服务暂时不可用", classTransient},
		{"请求参数错误：无效请求", classInvalidRequest},
		{"服务器内部错误", classServer},
		{"服务不可用", classServer},
		{"系统繁忙，请稍后再试", classServer},
	}
	for _, tc := range cases {
		t.Run(tc.msg, func(t *testing.T) {
			if got := ClassifyErrorClass(errors.New(tc.msg)); got != tc.want {
				t.Fatalf("ClassifyErrorClass(%q) = %q, want %q", tc.msg, got, tc.want)
			}
		})
	}
}

// TestClassifyErrorIsCaseInsensitive 确认英文关键字大小写不敏感。
func TestClassifyErrorIsCaseInsensitive(t *testing.T) {
	cases := []struct {
		msg  string
		want ErrorClass
	}{
		{"RATE LIMIT EXCEEDED", classRateLimit},
		{"RaTe LiMiT", classRateLimit},
		{"401 UNAUTHORIZED INVALID API KEY", classAuth},
		{"CONNECTION RESET BY PEER", classTransient},
		{"GATEWAY TIMEOUT", classTransient},
		{"INVALID REQUEST", classInvalidRequest},
		{"INTERNAL SERVER ERROR", classServer},
	}
	for _, tc := range cases {
		t.Run(tc.msg, func(t *testing.T) {
			if got := ClassifyErrorClass(errors.New(tc.msg)); got != tc.want {
				t.Fatalf("ClassifyErrorClass(%q) = %q, want %q", tc.msg, got, tc.want)
			}
		})
	}
}

// TestClassifyErrorPrecedence 锁定类别间的优先级：更具体的信号先赢，
// 例如 502（transient）必须先于通用 5xx（server），auth/limit 先于 timeout。
func TestClassifyErrorPrecedence(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want ErrorClass
	}{
		{"502 before server", "502 Bad Gateway", classTransient},
		{"503 service unavailable is transient", "503 Service Unavailable", classTransient},
		{"429 before auth", "429 Unauthorized rate limit", classRateLimit},
		{"auth before transient", "401 Unauthorized after timeout", classAuth},
		{"transient before invalid request", "400 bad request, connection reset", classTransient},
		{"invalid request before server", "500 internal server error: invalid request", classInvalidRequest},
		{"auth before invalid request", "400 invalid api key", classAuth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyErrorClass(errors.New(tc.msg)); got != tc.want {
				t.Fatalf("ClassifyErrorClass(%q) = %q, want %q", tc.msg, got, tc.want)
			}
		})
	}
}

// TestClassifyErrorWrappedChain 确认 %w 包装链中的内层关键字依然可见，
// 对 errors.Is/errors.As 友好的包装错误能正确分类。
func TestClassifyErrorWrappedChain(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ErrorClass
	}{
		{
			name: "fmt wrap rate limit",
			err:  fmt.Errorf("call provider: %w", errors.New("rate limit exceeded")),
			want: classRateLimit,
		},
		{
			name: "multi layer wrap auth",
			err:  fmt.Errorf("agent: %w", fmt.Errorf("llm: %w", errors.New("invalid api key"))),
			want: classAuth,
		},
		{
			name: "wrap transient",
			err:  fmt.Errorf("stream read: %w", errors.New("connection reset by peer")),
			want: classTransient,
		},
		{
			name: "wrap sentinel deadline",
			err:  fmt.Errorf("request: %w", context.DeadlineExceeded),
			want: classTransient,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyErrorClass(tc.err); got != tc.want {
				t.Fatalf("ClassifyErrorClass(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// statusError 是一个暴露 HTTP 状态码的自定义错误类型，用于验证 errors.As 友好性。
type statusError struct {
	code int
	msg  string
}

func (s *statusError) Error() string   { return s.msg }
func (s *statusError) StatusCode() int { return s.code }

// TestClassifyErrorErrorsAsStatus 确认文本无关键字时，包装链中携带状态码的
// 结构化错误（errors.As）仍能被分类。
func TestClassifyErrorErrorsAsStatus(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ErrorClass
	}{
		{"429", &statusError{code: 429, msg: "provider error"}, classRateLimit},
		{"401", &statusError{code: 401, msg: "provider error"}, classAuth},
		{"400", &statusError{code: 400, msg: "provider error"}, classInvalidRequest},
		{"503", &statusError{code: 503, msg: "provider error"}, classTransient},
		{"500", &statusError{code: 500, msg: "provider error"}, classServer},
		{"404 unknown", &statusError{code: 404, msg: "provider error"}, classUnknown},
		{"wrapped status", fmt.Errorf("call: %w", &statusError{code: 429, msg: "oops"}), classRateLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyErrorClass(tc.err); got != tc.want {
				t.Fatalf("ClassifyErrorClass(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestClassifyErrorStandardSentinels 确认标准库网络/IO 哨兵在文本无关键字时兜底。
func TestClassifyErrorStandardSentinels(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"deadline", context.DeadlineExceeded},
		{"net closed", net.ErrClosed},
		{"eof", io.EOF},
		{"unexpected eof", io.ErrUnexpectedEOF},
		{"wrapped net closed", fmt.Errorf("stream: %w", net.ErrClosed)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyErrorClass(tc.err); got != classTransient {
				t.Fatalf("ClassifyErrorClass(%v) = %q, want %q", tc.err, got, classTransient)
			}
		})
	}
}

// TestClassifyErrorNilAndUnknown 确认 nil、纯空白与无关键字文本返回 classUnknown。
func TestClassifyErrorNilAndUnknown(t *testing.T) {
	if got := ClassifyErrorClass(nil); got != classUnknown {
		t.Fatalf("ClassifyErrorClass(nil) = %q, want %q", got, classUnknown)
	}
	for _, msg := range []string{"   ", "\t\n", "something unexpected happened", "just 4 2 9 separated digits"} {
		t.Run(msg, func(t *testing.T) {
			if got := ClassifyErrorClass(errors.New(msg)); got != classUnknown {
				t.Fatalf("ClassifyErrorClass(%q) = %q, want %q", msg, got, classUnknown)
			}
		})
	}
}

// TestClassifyErrorDoesNotMatchPartialStatus 确认状态码必须完整匹配，
// "4290" / "1400" 之类不应被误判。
func TestClassifyErrorDoesNotMatchPartialStatus(t *testing.T) {
	cases := []struct {
		msg  string
		want ErrorClass
	}{
		{"error code 4290 is unrelated", classUnknown},
		{"http 1400 is weird", classUnknown},
		{"value 5021 overflow", classUnknown},
		{"id 50000", classUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.msg, func(t *testing.T) {
			if got := ClassifyErrorClass(errors.New(tc.msg)); got != tc.want {
				t.Fatalf("ClassifyErrorClass(%q) = %q, want %q", tc.msg, got, tc.want)
			}
		})
	}
}

// TestClassifyErrorIsPureAndDeterministic 确认函数是纯且确定性的：
// 重复调用结果一致，且不修改传入的错误对象（文本与身份不变）。
func TestClassifyErrorIsPureAndDeterministic(t *testing.T) {
	err := fmt.Errorf("provider: %w", errors.New("rate limit exceeded (429)"))
	originalText := err.Error()
	first := ClassifyErrorClass(err)
	for i := 0; i < 5; i++ {
		if got := ClassifyErrorClass(err); got != first {
			t.Fatalf("call %d = %q, want %q (not deterministic)", i, got, first)
		}
	}
	if err.Error() != originalText {
		t.Fatalf("input mutated: %q -> %q", originalText, err.Error())
	}
	if first != classRateLimit {
		t.Fatalf("ClassifyErrorClass = %q, want %q", first, classRateLimit)
	}
}

// TestClassifyErrorExportedAliases 确认导出别名与规范常量一致，
// 供本包之外的调用方比较结果。
func TestClassifyErrorExportedAliases(t *testing.T) {
	pairs := []struct {
		exported  ErrorClass
		canonical ErrorClass
	}{
		{ErrorClassTransient, classTransient},
		{ErrorClassAuth, classAuth},
		{ErrorClassRateLimit, classRateLimit},
		{ErrorClassInvalidRequest, classInvalidRequest},
		{ErrorClassServer, classServer},
		{ErrorClassUnknown, classUnknown},
	}
	for _, p := range pairs {
		if p.exported != p.canonical {
			t.Fatalf("exported %q != canonical %q", p.exported, p.canonical)
		}
	}
	if got := ClassifyErrorClass(errors.New("429 rate limit")); got != ErrorClassRateLimit {
		t.Fatalf("ClassifyErrorClass = %q, want %q", got, ErrorClassRateLimit)
	}
}

// TestClassifyErrorChainTextCapsDepth 确认超长/自引用包装链不会导致死循环。
func TestClassifyErrorChainTextCapsDepth(t *testing.T) {
	// 构造一个环形 Unwrap 错误（病态输入），验证存在深度上限。
	e := &selfWrappingError{msg: "rate limit exceeded"}
	text := errorChainText(e)
	if !strings.Contains(text, "rate limit exceeded") {
		t.Fatalf("errorChainText dropped text: %q", text)
	}
	if ClassifyErrorClass(e) != classRateLimit {
		t.Fatalf("ClassifyErrorClass(self-wrapping) = %q, want %q", ClassifyErrorClass(e), classRateLimit)
	}
}

// selfWrappingError 的 Unwrap 返回自身，用于验证包装链遍历有深度上限。
type selfWrappingError struct{ msg string }

func (s *selfWrappingError) Error() string { return s.msg }
func (s *selfWrappingError) Unwrap() error { return s }
