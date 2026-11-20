package cli

import (
	"errors"
	"strings"
	"testing"
)

// TestRunPromptShowsActionableProviderError verifies that provider failures
// surfaced by the runner are classified at the CLI error output point: the
// stderr keeps the original terse "error: ..." line and gains the family
// explanation plus one concrete next step.
func TestRunPromptShowsActionableProviderError(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantMsg  string
		wantNext string
	}{
		{
			name:     "not configured",
			err:      errors.New("provider not configured: no api key"),
			wantMsg:  "提供方尚未配置",
			wantNext: "先运行 login 登录或配置 API Key，再重试",
		},
		{
			name:     "auth",
			err:      errors.New("provider returned 401 Unauthorized: invalid api key"),
			wantMsg:  "认证失败",
			wantNext: "重新登录（login / /login）后重试",
		},
		{
			name:     "quota exhausted",
			err:      errors.New("insufficient_quota: out of credit"),
			wantMsg:  "额度或配额已用尽",
			wantNext: "等待配额重置后重试，或用 /model 换一个模型",
		},
		{
			name:     "rate limit",
			err:      errors.New("provider returned 429 Too Many Requests: rate limit exceeded"),
			wantMsg:  "请求过于频繁",
			wantNext: "稍后重试（客户端会自动退避），或换模型重试",
		},
		{
			name:     "content filter",
			err:      errors.New("content filter blocked the request"),
			wantMsg:  "内容被安全策略拦截",
			wantNext: "修改提示词内容后重试",
		},
		{
			name:     "model not found",
			err:      errors.New("404 model not found"),
			wantMsg:  "模型不存在或不可用",
			wantNext: "用 /model 选择可用模型后重试",
		},
		{
			name:     "network",
			err:      errors.New("dial tcp: connection refused"),
			wantMsg:  "网络连接失败",
			wantNext: "检查网络或代理设置后重试",
		},
		{
			name:     "server",
			err:      errors.New("provider returned 500 Internal Server Error"),
			wantMsg:  "提供方服务端暂时不可用",
			wantNext: "稍后重试；若持续失败可换模型",
		},
		{
			name:     "unknown",
			err:      errors.New("something unexpected"),
			wantMsg:  "请求失败（未识别的错误类型）",
			wantNext: "查看原始错误信息后重试，或用 /model 换模型",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			factory := func(Options) (Runner, error) {
				return &stubRunner{fail: tc.err}, nil
			}
			ioState, out, errOut := newTestIO("", nil)
			code := RunWith(App{IO: ioState, NewRunner: factory}, []string{"hi"})
			if code != 1 {
				t.Fatalf("exit = %d, want 1; stderr = %q", code, errOut.String())
			}
			got := errOut.String()
			// The original line must survive unchanged (additive behaviour).
			if !strings.Contains(got, "error: "+tc.err.Error()) {
				t.Fatalf("original error line missing from stderr: %q", got)
			}
			if !strings.Contains(got, tc.wantMsg) {
				t.Fatalf("explanation %q missing from stderr: %q", tc.wantMsg, got)
			}
			if !strings.Contains(got, tc.wantNext) {
				t.Fatalf("next step %q missing from stderr: %q", tc.wantNext, got)
			}
			if out.Len() != 0 {
				t.Fatalf("stdout = %q, want empty on error", out.String())
			}
		})
	}
}

// TestRunPromptActionableHintKeepsSuccessPath guards the "no change on
// success" requirement: a successful run must not emit any hint on stderr.
func TestRunPromptActionableHintKeepsSuccessPath(t *testing.T) {
	factory := func(Options) (Runner, error) {
		return &stubRunner{reply: "answer"}, nil
	}
	ioState, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState, NewRunner: factory}, []string{"hi"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, errOut.String())
	}
	if got := out.String(); got != "answer\n" {
		t.Fatalf("stdout = %q", got)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", errOut.String())
	}
}

// TestWriteActionableErrorNil is the seam-level guard: classification must be
// a no-op for a nil error and never write a blank hint.
func TestWriteActionableErrorNil(t *testing.T) {
	ioState, _, errOut := newTestIO("", nil)
	writeActionableError(ioState.Err, nil)
	if got := errOut.String(); got != "" {
		t.Fatalf("stderr = %q, want empty", got)
	}
}
