package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// TestAppendAuthExpiryAdviceWiring 证明 401 路径确实调用了 authExpiryAdvice：
// 无 token 的提供方给出"状态未知"的可执行建议，门控关闭时保持静默。
func TestAppendAuthExpiryAdviceWiring(t *testing.T) {
	err := errors.New("provider returned 401 Unauthorized: invalid api key")
	for _, tc := range []struct {
		name    string
		env     map[string]string
		want    string
		wantAny bool
	}{
		{"enabled", map[string]string{cliHintsEnvVar: "1"}, "凭证状态未知", true},
		{"disabled", map[string]string{cliHintsEnvVar: "0"}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := App{StorageDir: t.TempDir(), IO: IO{Env: func(k string) string { return tc.env[k] }}}
			var buf bytes.Buffer
			app.appendRuntimeAdvice(&buf, err, "openai", false)
			if tc.wantAny {
				if !strings.Contains(buf.String(), tc.want) {
					t.Fatalf("stderr = %q, want %q", buf.String(), tc.want)
				}
				if !strings.Contains(buf.String(), "argentgo auth") {
					t.Fatalf("stderr = %q, want actionable auth command", buf.String())
				}
			} else if buf.Len() != 0 {
				t.Fatalf("stderr = %q, want empty with gate off", buf.String())
			}
		})
	}
}

// TestAppendConfirmAdviceBothBranches 覆盖审批被拒/非交互确认的 TTY 与非 TTY 两条分支。
func TestAppendConfirmAdviceBothBranches(t *testing.T) {
	err := errors.New("tool call failed: " + security.ErrApprovalUnavailable.Error())
	cases := []struct {
		name  string
		isTTY bool
		want  string
	}{
		{"tty", true, "输入 y 确认"},
		{"non-tty", false, confirmNonTTYAdvice},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := App{IO: IO{Env: func(string) string { return "1" }}}
			var buf bytes.Buffer
			app.appendRuntimeAdvice(&buf, err, "", tc.isTTY)
			if !strings.Contains(buf.String(), tc.want) {
				t.Fatalf("stderr = %q, want %q", buf.String(), tc.want)
			}
		})
	}
}

// TestAppendRuntimeAdviceIgnoresUnrelated 守护接线边界：普通错误不追加任何建议。
func TestAppendRuntimeAdviceIgnoresUnrelated(t *testing.T) {
	app := App{StorageDir: t.TempDir(), IO: IO{Env: func(string) string { return "1" }}}
	var buf bytes.Buffer
	app.appendRuntimeAdvice(&buf, errors.New("disk full"), "", false)
	if buf.Len() != 0 {
		t.Fatalf("stderr = %q, want empty for unrelated error", buf.String())
	}
}

// TestContextHealthNearLimitHasBudgetAdvice 证明上下文将满时输出里带上 /drop 建议。
func TestContextHealthNearLimitHasBudgetAdvice(t *testing.T) {
	segs := BreakdownContext(10, 20, 25, 10) // 总和 65
	got := ContextHealth(segs, 70)
	if !strings.Contains(got, "/drop") {
		t.Fatalf("near-limit health = %q, want budget advice with /drop", got)
	}
	// 充足时不带建议，保持既有输出不变。
	if ample := ContextHealth(segs, 120); strings.Contains(ample, "/drop") {
		t.Fatalf("ample health = %q, want no budget advice", ample)
	}
}
