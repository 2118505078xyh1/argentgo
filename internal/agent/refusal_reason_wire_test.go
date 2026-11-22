package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// TestRefusalReasonFlag 验证 opt-in 开关的默认值与切换语义。
func TestRefusalReasonFlag(t *testing.T) {
	SetRefusalReason(false)
	defer SetRefusalReason(false)
	if refusalReasonEnabled() {
		t.Fatal("默认应关闭拒绝原因分类")
	}
	SetRefusalReason(true)
	if !refusalReasonEnabled() {
		t.Fatal("设置后应开启拒绝原因分类")
	}
}

// refusalTestState 构造一个 runTool 可以直接使用的最小运行状态：
// 提供的工具总是返回指定错误，因此 runTool 必然进入 err != nil 分支。
func refusalTestState(err error) *runState {
	return &runState{tools: &fakeTools{run: func(context.Context, string, string) (string, error) {
		return "", err
	}}}
}

// TestRefusalReasonClassifyAndOutput 表驱动覆盖两个原语接线：
//   - 关闭时 callResult.refusal 必须为零值；
//   - 开启时 refusal 必须按 security.ReasonFromError 的语义正确分类；
//   - 两种状态下 output 字符串都必须与基线逐字节相同（含可恢复计划拦截的后缀）。
func TestRefusalReasonClassifyAndOutput(t *testing.T) {
	SetRefusalReason(false)
	defer SetRefusalReason(false)

	cases := []struct {
		name        string
		err         error
		want        security.RefusalReason
		recoverable bool // 是否为计划模式命令拦截（runTool 会追加固定后缀）
	}{
		{
			name: "RefusalError",
			err:  &security.RefusalError{Tool: "bash", Feedback: "use ls"},
			want: security.RefusalReason{
				Kind: security.RefusalKindToolError, Tool: "bash",
				Detail: "use ls", Recoverable: true,
			},
		},
		{
			name: "FeedbackRefusal",
			err:  &security.FeedbackRefusal{Feedback: "do it differently"},
			want: security.RefusalReason{
				Kind:   security.RefusalKindToolError,
				Detail: "do it differently", Recoverable: true,
			},
		},
		{
			name: "StopError refused",
			err:  &security.StopError{Tool: "bash", Reason: "operator said no", Refused: true},
			want: security.RefusalReason{
				Kind: security.RefusalKindApprovalDeclined, Tool: "bash",
				Detail: "operator said no",
			},
		},
		{
			name: "StopError sandbox",
			err:  &security.StopError{Tool: "bash", Reason: "blocked by sandbox", Refused: false},
			want: security.RefusalReason{
				Kind: security.RefusalKindSandboxDenied, Tool: "bash",
				Detail: "blocked by sandbox",
			},
		},
		{
			name: "StopError policy",
			err:  &security.StopError{Tool: "bash", Reason: "policy says no", Refused: false},
			want: security.RefusalReason{
				Kind: security.RefusalKindPolicyDeny, Tool: "bash",
				Detail: "policy says no",
			},
		},
		{
			name: "unavailable",
			err:  security.ErrApprovalUnavailable,
			want: security.RefusalReason{
				Kind:   security.RefusalKindUnavailable,
				Detail: security.ErrApprovalUnavailable.Error(),
			},
		},
		{
			name:        "recoverable plan block",
			err:         security.ErrRecoverablePlanBlock,
			want:        security.RefusalReason{Kind: security.RefusalKindToolError, Detail: security.ErrRecoverablePlanBlock.Error(), Recoverable: true},
			recoverable: true,
		},
		{
			name: "refused sentinel",
			err:  security.ErrRefused,
			want: security.RefusalReason{
				Kind:   security.RefusalKindApprovalDeclined,
				Detail: security.ErrRefused.Error(),
			},
		},
		{
			name: "denied sentinel",
			err:  security.ErrDenied,
			want: security.RefusalReason{
				Kind:   security.RefusalKindPolicyDeny,
				Detail: security.ErrDenied.Error(),
			},
		},
		{
			name: "ordinary failure stays zero",
			err:  errors.New("ordinary failure"),
			want: security.RefusalReason{},
		},
		{
			name:        "wrapped sentinel is matched through errors.Is",
			err:         fmt.Errorf("ctx: %w", security.ErrRecoverablePlanBlock),
			want:        security.RefusalReason{Kind: security.RefusalKindToolError, Detail: "ctx: " + security.ErrRecoverablePlanBlock.Error(), Recoverable: true},
			recoverable: true,
		},
	}

	call := toolCall("c1", "bash", `{}`)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 与基线逐字节相同的输出文本。
			wantOutput := "error: " + tc.err.Error()
			if tc.recoverable {
				wantOutput += " Continue with a read-only command or a different approach."
			}

			SetRefusalReason(false)
			off := refusalTestState(tc.err).runTool(context.Background(), call, time.Now())
			if off.refusal != (security.RefusalReason{}) {
				t.Fatalf("关闭时 refusal = %+v, want zero", off.refusal)
			}
			if off.output != wantOutput {
				t.Fatalf("关闭时 output 改变:\ngot : %q\nwant: %q", off.output, wantOutput)
			}
			if !off.isError {
				t.Fatal("错误分支必须置 isError")
			}

			SetRefusalReason(true)
			on := refusalTestState(tc.err).runTool(context.Background(), call, time.Now())
			if on.refusal != tc.want {
				t.Fatalf("开启时 refusal = %+v, want %+v", on.refusal, tc.want)
			}
			// 开启后：命中已知拒绝类别且走普通错误分支（dispatch.go:512-513）时，
			// 错误文本末尾追加模型可读提示；可恢复计划拦截走 507 的独立分支，
			// 仍只有原有固定后缀；未命中任何类别（零值）时文本逐字节不变。
			wantOn := wantOutput
			if tc.want != (security.RefusalReason{}) && !tc.recoverable {
				wantOn = wantOutput + "\n" + tc.want.ModelHint()
			}
			if on.output != wantOn {
				t.Fatalf("开启时 output 改变:\ngot : %q\nwant: %q", on.output, wantOn)
			}
			if on.intent != off.intent || on.isError != off.isError || on.invalidArgs != off.invalidArgs {
				t.Fatalf("开启后非拒绝对字段改变: off=%+v on=%+v", off, on)
			}
		})
	}
}

// TestRefusalReasonOffNeverClassifies 验证开关关闭时即便传入可识别的错误，
// callResult.refusal 也保持零值——即 classifyRefusal 在关闭时走最短路径返回。
func TestRefusalReasonOffNeverClassifies(t *testing.T) {
	SetRefusalReason(false)
	defer SetRefusalReason(false)

	errs := []error{
		&security.RefusalError{Tool: "bash", Feedback: "x"},
		security.ErrDenied,
		security.ErrRecoverablePlanBlock,
	}
	for _, err := range errs {
		res := refusalTestState(err).runTool(context.Background(), toolCall("c", "bash", `{}`), time.Now())
		if res.refusal != (security.RefusalReason{}) {
			t.Fatalf("关闭时错误 %v 被分类: %+v", err, res.refusal)
		}
	}
}
