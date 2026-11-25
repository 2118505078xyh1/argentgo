package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"example.com/argentgo/internal/llm"
)

// retryOmitNotices 收集事件流里所有 Notice 文本，供断言省略失败提示。
func retryOmitNotices(s *eventSink) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, event := range s.events {
		if event.Kind == EventNotice {
			out = append(out, event.Text)
		}
	}
	return out
}

// TestRetryOmitEnabledGate 锁定独立门控：默认开启，仅修剪空白后精确等于 "0" 才关闭。
func TestRetryOmitEnabledGate(t *testing.T) {
	tests := []struct {
		name string
		val  string
		want bool
	}{
		{name: "空值开启", val: "", want: true},
		{name: "0 关闭", val: "0", want: false},
		{name: "true 开启", val: "true", want: true},
		{name: "01 开启", val: "01", want: true},
		{name: "1 开启", val: "1", want: true},
		{name: "首尾空白的 1 开启", val: " \t1\n ", want: true},
		{name: "首尾空白的 0 关闭", val: " \t0\n ", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ARGENT_RETRY_OMIT", tc.val)
			if got := retryOmitEnabled(); got != tc.want {
				t.Fatalf("retryOmitEnabled() = %t, want %t", got, tc.want)
			}
		})
	}
}

// TestAbortableBackoff 验证退避是 ctx 感知的：取消/超时必须立即返回，
// 正常延时才等待，零延时不等待。
func TestAbortableBackoff(t *testing.T) {
	tests := []struct {
		name      string
		delay     time.Duration
		cancel    bool
		timeout   time.Duration
		wantErr   error
		wantQuick bool
	}{
		{name: "零延时直接返回", delay: 0, wantErr: nil},
		{name: "短延时正常完成", delay: time.Millisecond, wantErr: nil},
		{name: "已取消立即返回", delay: time.Hour, cancel: true, wantErr: context.Canceled, wantQuick: true},
		{name: "等待中超时立即返回", delay: time.Hour, timeout: 10 * time.Millisecond, wantErr: context.DeadlineExceeded, wantQuick: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var cancel context.CancelFunc
			if tc.cancel {
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			} else if tc.timeout > 0 {
				ctx, cancel = context.WithTimeout(ctx, tc.timeout)
			}
			if cancel != nil {
				defer cancel()
			}
			start := time.Now()
			err := abortableBackoff(ctx, tc.delay)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("abortableBackoff() error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantQuick && time.Since(start) > 500*time.Millisecond {
				t.Fatalf("abortableBackoff() did not return promptly on cancellation")
			}
		})
	}
}

// TestRetryOmitDelay 锁定指数退避与封顶，且失败序号从 1 开始。
func TestRetryOmitDelay(t *testing.T) {
	origBase, origMax := retryOmitBaseDelay, retryOmitMaxDelay
	defer func() { retryOmitBaseDelay, retryOmitMaxDelay = origBase, origMax }()
	retryOmitBaseDelay = 100 * time.Millisecond
	retryOmitMaxDelay = 300 * time.Millisecond

	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 0, want: 100 * time.Millisecond},
		{attempt: 1, want: 100 * time.Millisecond},
		{attempt: 2, want: 200 * time.Millisecond},
		{attempt: 3, want: 300 * time.Millisecond},
		{attempt: 4, want: 300 * time.Millisecond},
		{attempt: 10, want: 300 * time.Millisecond},
	}
	for _, tc := range tests {
		if got := retryOmitDelay(tc.attempt); got != tc.want {
			t.Errorf("retryOmitDelay(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

// TestChatWithFallbackRetryOmit 端到端覆盖：=0 关闭路径不省略且零延时（即使配置了
// 极长退避也在 ctx 超时前完成），默认开启后回调按轮次被调用一次，回调报错也不改变重试结果。
func TestChatWithFallbackRetryOmit(t *testing.T) {
	tests := []struct {
		name       string
		env        string
		baseDelay  time.Duration
		hookErr    error
		wantHook   int
		wantNotice bool
	}{
		{name: "=0 关闭不省略不退避", env: "0", baseDelay: 2 * time.Second, wantHook: 0},
		{name: "默认开启省略并退避", env: "", baseDelay: time.Millisecond, wantHook: 1},
		{name: "回调报错仍继续重试", env: "", baseDelay: time.Millisecond, hookErr: errors.New("edit failed"), wantHook: 1, wantNotice: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ARGENT_RETRY_OMIT", tc.env)
			origBase := retryOmitBaseDelay
			defer func() { retryOmitBaseDelay = origBase }()
			retryOmitBaseDelay = tc.baseDelay

			var turns []int
			SetRetryOmitHook(func(turn int) error {
				turns = append(turns, turn)
				return tc.hookErr
			})
			defer SetRetryOmitHook(nil)

			chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
				if n == 1 {
					return llm.Message{}, errors.New("HTTP 429: too many requests")
				}
				return textReply("ok from backup"), nil
			}}
			sink := &eventSink{}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			runner := &Runner{Opts: Options{
				Model:          "primary",
				FallbackModels: []string{"primary", "backup"},
				Chatter:        chat,
				Emit:           sink.emit,
			}}

			res, err := runner.Run(ctx, "hi")
			if err != nil {
				t.Fatalf("run: %v (ctx err: %v)", err, ctx.Err())
			}
			if res.Text != "ok from backup" {
				t.Fatalf("result text = %q, want backup answer", res.Text)
			}
			if got := strings.Join(chat.modelsSeen(), ","); got != "primary,backup" {
				t.Fatalf("models = %q, want primary,backup", got)
			}
			if len(turns) != tc.wantHook {
				t.Fatalf("omit hook calls = %d, want %d", len(turns), tc.wantHook)
			}
			if tc.wantHook > 0 && turns[0] != 0 {
				t.Fatalf("omit hook turn = %d, want 0", turns[0])
			}
			notices := retryOmitNotices(sink)
			hasOmitNotice := false
			for _, text := range notices {
				if strings.Contains(text, "省略失败上下文失败") {
					hasOmitNotice = true
				}
			}
			if hasOmitNotice != tc.wantNotice {
				t.Fatalf("has omit-failure notice = %t, want %t (notices: %q)", hasOmitNotice, tc.wantNotice, notices)
			}
		})
	}
}

// TestChatWithFallbackRetryOmitAbortOnCancel 验证退避可被 ctx 取消：在回调里取消
// ctx 后，退避立即返回且不再发起第二次模型请求。
func TestChatWithFallbackRetryOmitAbortOnCancel(t *testing.T) {
	t.Setenv("ARGENT_RETRY_OMIT", "")
	origBase := retryOmitBaseDelay
	defer func() { retryOmitBaseDelay = origBase }()
	// 极长退避：若取消未生效，测试会挂到 go test 超时而不是快速失败。
	retryOmitBaseDelay = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	SetRetryOmitHook(func(int) error {
		cancel()
		return nil
	})
	defer SetRetryOmitHook(nil)

	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n != 1 {
			t.Fatalf("chatter called again after cancel during backoff (n=%d)", n)
		}
		return llm.Message{}, errors.New("HTTP 429: too many requests")
	}}
	runner := &Runner{Opts: Options{
		Model:          "primary",
		FallbackModels: []string{"primary", "backup"},
		Chatter:        chat,
	}}

	_, err := runner.Run(ctx, "hi")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if chat.count() != 1 {
		t.Fatalf("requests = %d, want 1 (no retry after cancel)", chat.count())
	}
}
