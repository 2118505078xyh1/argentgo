package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"syscall"
	"testing"
)

// connTimeoutError 是 Timeout() 返回 true 的 net.Error，
// 用于模拟标准库传输层抛出的超时错误。
type connTimeoutError struct{}

func (connTimeoutError) Error() string   { return "i/o timeout" }
func (connTimeoutError) Timeout() bool   { return true }
func (connTimeoutError) Temporary() bool { return true }

// connNonTimeoutError 是 Timeout() 返回 false 的 net.Error，
// 用于确认仅凭 net.Error 类型还不足以判为可重试。
type connNonTimeoutError struct{}

func (connNonTimeoutError) Error() string   { return "network operation failed" }
func (connNonTimeoutError) Timeout() bool   { return false }
func (connNonTimeoutError) Temporary() bool { return false }

func TestIsRetryableConnectionError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		// —— 空错误 ——
		{"nil", nil, false},

		// —— 可重试：连接层瞬时故障哨兵 ——
		{"connection reset", syscall.ECONNRESET, true},
		{"connection aborted", syscall.ECONNABORTED, true},
		{"connection refused", syscall.ECONNREFUSED, true},
		{"timed out", syscall.ETIMEDOUT, true},
		{"host unreachable", syscall.EHOSTUNREACH, true},
		{"host down", syscall.EHOSTDOWN, true},
		{"network unreachable", syscall.ENETUNREACH, true},
		{"network reset", syscall.ENETRESET, true},
		{"network down", syscall.ENETDOWN, true},
		{"broken pipe", syscall.EPIPE, true},
		{"eof", io.EOF, true},
		{"unexpected eof", io.ErrUnexpectedEOF, true},
		{"context deadline", context.DeadlineExceeded, true},
		{"os deadline", os.ErrDeadlineExceeded, true},

		// —— 可重试：被多层包裹的哨兵 ——
		{"wrapped reset", fmt.Errorf("read frame: %w", syscall.ECONNRESET), true},
		{"double wrapped timeout", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", syscall.ETIMEDOUT)), true},
		{"stdio eof wrapped", fmt.Errorf(`mcp "x": server closed the connection: %w`, io.EOF), true},

		// —— 可重试：net.Error 超时分支 ——
		{"net timeout", connTimeoutError{}, true},
		{"wrapped net timeout", fmt.Errorf("call: %w", connTimeoutError{}), true},
		{"url error timeout", &url.Error{Op: "Get", URL: "http://x", Err: connTimeoutError{}}, true},
		{"wrapped url error timeout", fmt.Errorf("initialize: %w", &url.Error{Op: "Post", URL: "http://x", Err: connTimeoutError{}}), true},

		// —— 可重试：网关类 HTTP 状态（有类型错误）——
		{"http 502", &HTTPStatusError{StatusCode: 502}, true},
		{"http 503 wrapped", fmt.Errorf("mcp: %w", &HTTPStatusError{StatusCode: 503, Message: "unavailable"}), true},
		{"http 504", &HTTPStatusError{StatusCode: 504}, true},

		// —— 不可重试：认证失败 ——
		{"auth sentinel", ErrAuthentication, false},
		{"wrapped auth sentinel", fmt.Errorf("认证失败: %w", ErrAuthentication), false},
		{"os permission", os.ErrPermission, false},
		{"wrapped path permission", fmt.Errorf("open: %w", &os.PathError{Op: "open", Path: "x", Err: os.ErrPermission}), false},
		{"http 401", &HTTPStatusError{StatusCode: 401}, false},
		{"http 403 wrapped", fmt.Errorf("call: %w", &HTTPStatusError{StatusCode: 403, Message: "Forbidden"}), false},
		{"http 500 not transient", &HTTPStatusError{StatusCode: 500}, false},

		// —— 不可重试：配置错误 ——
		{"config sentinel", ErrConfiguration, false},
		{"wrapped config sentinel", fmt.Errorf("配置错误: %w", ErrConfiguration), false},
		{"unsupported transport", ErrUnsupportedTransport, false},
		{"disabled", fmt.Errorf("disabled: %w", ErrDisabled), false},
		{"read only", ErrReadOnly, false},
		{"tool not allowed", ErrToolNotAllowed, false},

		// —— 不可重试：其它永久失败 ——
		{"method not found", &rpcErrorBody{Code: -32601}, false},
		{"wrapped method not found", fmt.Errorf("call: %w", &rpcErrorBody{Code: -32601}), false},
		{"context canceled", context.Canceled, false},
		{"wrapped context canceled", fmt.Errorf("call: %w", context.Canceled), false},
		{"non timeout net error", connNonTimeoutError{}, false},
		{"generic error", errors.New("boom"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRetryableConnectionError(tc.err); got != tc.want {
				t.Fatalf("isRetryableConnectionError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestIsRetryableConnectionErrorIgnoresErrorText 验证分类与措辞无关：
// 纯文本（无论 CJK 还是英文）永远不触发可重试；反过来，一旦错误链上
// 带哨兵，外面的文本包装（含 CJK、英文、大小写变化）都不会改变结论。
func TestIsRetryableConnectionErrorIgnoresErrorText(t *testing.T) {
	plainTexts := []string{
		"connection reset by peer",
		"连接被重置",
		"i/o timeout",
		"连接超时",
		"EOF",
		"unauthorized",
		"认证失败",
	}
	for _, text := range plainTexts {
		if got := isRetryableConnectionError(errors.New(text)); got {
			t.Errorf("纯文本错误 %q 不应判为可重试（文本不得参与分类）", text)
		}
	}

	wrappers := []string{"连接重置", "connection reset", "TIMEOUT", "超时"}
	for _, wrapper := range wrappers {
		err := fmt.Errorf("%s: %w", wrapper, syscall.ECONNRESET)
		if !isRetryableConnectionError(err) {
			t.Errorf("包裹文本 %q 不应改变哨兵的可重试结论", wrapper)
		}
		err = fmt.Errorf("%s: %w", wrapper, ErrAuthentication)
		if isRetryableConnectionError(err) {
			t.Errorf("包裹文本 %q 不应改变哨兵的不可重试结论", wrapper)
		}
	}
}

// TestIsRetryableConnectionErrorPermanentWins 验证"永久优先"：
// 同一错误链里同时含有永久哨兵与瞬时哨兵时，判为不可重试，
// 避免对认证/配置这类注定失败的故障反复退避重连。
func TestIsRetryableConnectionErrorPermanentWins(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"auth and timeout", fmt.Errorf("认证失败: %w", errors.Join(ErrAuthentication, syscall.ETIMEDOUT))},
		{"config and reset", fmt.Errorf("配置错误: %w", errors.Join(ErrConfiguration, syscall.ECONNRESET))},
		{"permission and deadline", fmt.Errorf("权限不足: %w", errors.Join(os.ErrPermission, context.DeadlineExceeded))},
		{"cancel and reset", fmt.Errorf("已取消: %w", errors.Join(context.Canceled, syscall.ECONNRESET))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if isRetryableConnectionError(tc.err) {
				t.Fatalf("永久错误应压过瞬时信号，但 %v 被判为可重试", tc.err)
			}
		})
	}
}

// TestIsRetryableConnectionErrorStable 验证纯函数的确定性：
// 同一错误反复求值始终得到一致结果。
func TestIsRetryableConnectionErrorStable(t *testing.T) {
	errs := []error{
		nil,
		syscall.ECONNRESET,
		ErrAuthentication,
		connTimeoutError{},
		&HTTPStatusError{StatusCode: 503},
		errors.New("boom"),
	}
	for _, err := range errs {
		want := isRetryableConnectionError(err)
		for i := 0; i < 100; i++ {
			if got := isRetryableConnectionError(err); got != want {
				t.Fatalf("isRetryableConnectionError(%v) 第 %d 次求值 = %v，首次为 %v", err, i, got, want)
			}
		}
	}
}
