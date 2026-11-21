package security

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// stagedStep 是脚本化分阶段审批器在一轮交互中的回答。
type stagedStep struct {
	choice    string
	scope     []string
	feedback  string
	confirmed bool
	err       error
}

// scriptedStagedApprover 按预置脚本逐轮回答 AskStage，并记录每一步的阶段与
// 摘要。普通 Ask 若被调用会直接报错，确保 opt-in 路径确实走状态机而非旧路径。
type scriptedStagedApprover struct {
	steps     []stagedStep
	loopLast  bool // 脚本用尽后重复最后一步，用于验证轮次上限
	calls     int
	stages    []ApprovalStage
	summaries []string
	remember  [][]string
}

func (a *scriptedStagedApprover) Ask(context.Context, string, string) (bool, error) {
	return false, errors.New("scriptedStagedApprover: plain Ask must not be called in staged mode")
}

func (a *scriptedStagedApprover) AskStage(_ context.Context, _ string, _ string, stage ApprovalStage, summary string) (ApprovalDecision, error) {
	a.stages = append(a.stages, stage)
	a.summaries = append(a.summaries, summary)
	idx := a.calls
	if idx >= len(a.steps) && a.loopLast && len(a.steps) > 0 {
		idx = len(a.steps) - 1
	}
	if idx >= len(a.steps) {
		return ApprovalDecision{}, errors.New("scriptedStagedApprover: no more steps")
	}
	step := a.steps[idx]
	a.calls++
	return ApprovalDecision{
		Choice:    step.choice,
		Scope:     step.scope,
		Feedback:  step.feedback,
		Confirmed: step.confirmed,
	}, step.err
}

func (a *scriptedStagedApprover) RememberScope(scopes []string) {
	a.remember = append(a.remember, append([]string(nil), scopes...))
}

// dualApprover 同时实现普通与分阶段接口：普通 Ask 走原逻辑，AskStage 仅用来
// 检测 opt-in 是否误把控制流切到状态机。
type dualApprover struct {
	allow      bool
	feedback   string
	askCalls   int
	stageCalls int
}

func (a *dualApprover) Ask(context.Context, string, string) (bool, error) {
	a.askCalls++
	if a.allow {
		return true, nil
	}
	if a.feedback != "" {
		return false, &FeedbackRefusal{Feedback: a.feedback}
	}
	return false, nil
}

func (a *dualApprover) AskStage(context.Context, string, string, ApprovalStage, string) (ApprovalDecision, error) {
	a.stageCalls++
	return ApprovalDecision{Choice: ChoiceOnce}, nil
}

func (a *dualApprover) RememberScope([]string) {}

func stagedCheck(t *testing.T, chain *Chain) error {
	t.Helper()
	return chain.Check(context.Background(), "write", json.RawMessage(`{"path":"a.txt"}`))
}

// opt-in 关闭时，即使审批器实现了 StagedApprover，也必须走原始 Ask 路径，
// 结果与旧逻辑逐字节一致（批准 / 纯否决 / 带反馈否决）。
func TestChainStagedOptInOffKeepsBaseline(t *testing.T) {
	for _, tc := range []struct {
		name     string
		allow    bool
		feedback string
		want     string
	}{
		{name: "allow", allow: true, want: "allow"},
		{name: "plain refusal", want: "stop"},
		{name: "feedback refusal", feedback: "use docs", want: "refusal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &dualApprover{allow: tc.allow, feedback: tc.feedback}
			chain := feedbackTestChain(t, a)
			if StagedApprovalEnabled(chain) {
				t.Fatal("staged approval must be off by default")
			}
			err := stagedCheck(t, chain)
			if a.stageCalls != 0 {
				t.Fatalf("AskStage called %d times with opt-in off", a.stageCalls)
			}
			if a.askCalls != 1 {
				t.Fatalf("Ask called %d times, want 1", a.askCalls)
			}
			switch tc.want {
			case "allow":
				if err != nil {
					t.Fatalf("allow: %v", err)
				}
			case "stop":
				var stop *StopError
				if !errors.As(err, &stop) || !stop.Refused {
					t.Fatalf("got %v, want a refusing StopError", err)
				}
			case "refusal":
				var refusal *RefusalError
				if !errors.As(err, &refusal) || refusal.Feedback != tc.feedback {
					t.Fatalf("got %v, want RefusalError with %q", err, tc.feedback)
				}
			}
		})
	}
}

// opt-in 打开但审批器未实现 StagedApprover 时，仍走原始路径（逐字节不变）。
func TestChainStagedOptInOnPlainApproverKeepsBaseline(t *testing.T) {
	a := &feedbackApprover{feedback: "use docs"}
	chain := feedbackTestChain(t, a)
	chain.EnableStagedApproval(true)
	if !StagedApprovalEnabled(chain) {
		t.Fatal("EnableStagedApproval(true) did not enable")
	}
	err := stagedCheck(t, chain)
	var refusal *RefusalError
	if !errors.As(err, &refusal) || refusal.Feedback != "use docs" {
		t.Fatalf("got %v, want the original feedback refusal", err)
	}
}

// always 必须先进入二次确认；确认通过后才记住作用域并放行。
func TestChainStagedAlwaysRequiresConfirmBeforeRemembering(t *testing.T) {
	a := &scriptedStagedApprover{steps: []stagedStep{
		{choice: ChoiceAlways, scope: []string{"write:/项目"}},
		{confirmed: false},
		{confirmed: true},
	}}
	chain := feedbackTestChain(t, a)
	chain.EnableStagedApproval(true)

	if err := stagedCheck(t, chain); err != nil {
		t.Fatalf("staged always: %v", err)
	}
	wantStages := []ApprovalStage{stageOnce, stageConfirmScope, stageConfirmScope}
	if !reflect.DeepEqual(a.stages, wantStages) {
		t.Fatalf("stages = %v, want %v", a.stages, wantStages)
	}
	if a.calls != 3 {
		t.Fatalf("AskStage calls = %d, want 3", a.calls)
	}
	wantRemember := [][]string{{"write:/项目"}}
	if !reflect.DeepEqual(a.remember, wantRemember) {
		t.Fatalf("remember = %v, want %v", a.remember, wantRemember)
	}
	// 二次确认阶段应向审批器展示作用域摘要。
	if a.summaries[1] != "write:/项目" || a.summaries[2] != "write:/项目" {
		t.Fatalf("scope summaries = %v", a.summaries)
	}
	entries, readErr := ReadAudit(chain.Audit.Path())
	if readErr != nil || len(entries) != 1 || entries[0].Decision != "allow" {
		t.Fatalf("audit: %v entries=%+v", readErr, entries)
	}
}

// 未确认的作用域绝不能被记住：一直不确认会在轮次上限后硬性拒绝。
func TestChainStagedAlwaysUnconfirmedNeverRemembers(t *testing.T) {
	a := &scriptedStagedApprover{
		steps:    []stagedStep{{choice: ChoiceAlways, scope: []string{"write:/项目"}}},
		loopLast: true,
	}
	chain := feedbackTestChain(t, a)
	chain.EnableStagedApproval(true)

	err := stagedCheck(t, chain)
	var stop *StopError
	if !errors.As(err, &stop) || stop.Refused || !errors.Is(err, ErrDenied) {
		t.Fatalf("got %v, want a denying StopError", err)
	}
	if len(a.remember) != 0 {
		t.Fatalf("unconfirmed scope was remembered: %v", a.remember)
	}
	if len(a.stages) != maxStagedApprovalRounds {
		t.Fatalf("stages = %d, want %d", len(a.stages), maxStagedApprovalRounds)
	}
}

// reject 收集到的反馈必须复用 RefusalError 通道，成为模型可读的可恢复错误。
func TestChainStagedRejectFeedbackBecomesRefusalError(t *testing.T) {
	a := &scriptedStagedApprover{steps: []stagedStep{
		{choice: ChoiceReject},
		{feedback: "请只改 A 文件", confirmed: true},
	}}
	chain := feedbackTestChain(t, a)
	chain.EnableStagedApproval(true)

	err := stagedCheck(t, chain)
	var refusal *RefusalError
	if !errors.As(err, &refusal) || refusal.Feedback != "请只改 A 文件" {
		t.Fatalf("got %v, want RefusalError with feedback", err)
	}
	if !errors.Is(err, ErrRefused) || errors.Is(err, ErrDenied) {
		t.Fatalf("got %v, want ErrRefused only", err)
	}
	var stop *StopError
	if errors.As(err, &stop) {
		t.Fatalf("a correction must not be a final StopError: %#v", err)
	}
	if !strings.Contains(err.Error(), "Adjust the request and try again") {
		t.Fatalf("tool error lacks the actionable hint: %q", err.Error())
	}
	if len(a.remember) != 0 {
		t.Fatalf("reject must not remember scopes: %v", a.remember)
	}
	wantStages := []ApprovalStage{stageOnce, stageFeedback}
	if !reflect.DeepEqual(a.stages, wantStages) {
		t.Fatalf("stages = %v, want %v", a.stages, wantStages)
	}
	entries, readErr := ReadAudit(chain.Audit.Path())
	if readErr != nil || len(entries) != 1 || entries[0].Decision != "refused" {
		t.Fatalf("audit: %v entries=%+v", readErr, entries)
	}
	if strings.Contains(entries[0].Reason, "请只改") {
		t.Fatalf("audit reason leaked operator feedback: %q", entries[0].Reason)
	}
}

// 分阶段收集的反馈同样复用 clipApprovalFeedback，按 rune 安全裁剪。
func TestChainStagedFeedbackClippedOnRuneBoundary(t *testing.T) {
	a := &scriptedStagedApprover{steps: []stagedStep{
		{choice: ChoiceReject},
		{feedback: strings.Repeat("改", maxApprovalFeedbackRunes+50), confirmed: true},
	}}
	chain := feedbackTestChain(t, a)
	chain.EnableStagedApproval(true)

	err := stagedCheck(t, chain)
	var refusal *RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("got %v, want *RefusalError", err)
	}
	if !utf8.ValidString(refusal.Feedback) || !strings.HasSuffix(refusal.Feedback, "…[feedback clipped]") {
		t.Fatalf("feedback not clipped safely: %q", refusal.Feedback)
	}
}

// Esc 由状态机回退到上一阶段而非关闭审批，回退后重新从根阶段选择。
func TestChainStagedEscGoesBack(t *testing.T) {
	for _, tc := range []struct {
		name  string
		steps []stagedStep
		want  []ApprovalStage
	}{
		{
			name: "from confirm scope",
			steps: []stagedStep{
				{choice: ChoiceAlways, scope: []string{"write:/项目"}},
				{choice: ChoiceEsc},
				{choice: ChoiceOnce},
			},
			want: []ApprovalStage{stageOnce, stageConfirmScope, stageOnce},
		},
		{
			name: "from feedback",
			steps: []stagedStep{
				{choice: ChoiceReject},
				{choice: ChoiceEsc},
				{choice: ChoiceOnce},
			},
			want: []ApprovalStage{stageOnce, stageFeedback, stageOnce},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &scriptedStagedApprover{steps: tc.steps}
			chain := feedbackTestChain(t, a)
			chain.EnableStagedApproval(true)

			if err := stagedCheck(t, chain); err != nil {
				t.Fatalf("staged esc: %v", err)
			}
			if !reflect.DeepEqual(a.stages, tc.want) {
				t.Fatalf("stages = %v, want %v", a.stages, tc.want)
			}
			if len(a.remember) != 0 {
				t.Fatalf("esc must not remember scopes: %v", a.remember)
			}
		})
	}
}

// 分阶段审批中的通道故障与取消：不带终结语句、按 ClassifyApproval 归类。
func TestChainStagedApprovalFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		wantAudit string
		wantIs    error
	}{
		{name: "cancelled", err: context.Canceled, wantAudit: "", wantIs: context.Canceled},
		{name: "unavailable", err: ErrApprovalUnavailable, wantAudit: "unavailable", wantIs: ErrApprovalUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &scriptedStagedApprover{steps: []stagedStep{{err: tc.err}}}
			chain := feedbackTestChain(t, a)
			chain.EnableStagedApproval(true)

			err := stagedCheck(t, chain)
			if err == nil || !errors.Is(err, tc.wantIs) {
				t.Fatalf("got %v, want an error wrapping %v", err, tc.wantIs)
			}
			var stop *StopError
			if errors.As(err, &stop) {
				t.Fatalf("approval failure must not be a final StopError: %#v", err)
			}
			entries, _ := ReadAudit(chain.Audit.Path())
			if tc.wantAudit == "" {
				if len(entries) != 0 {
					t.Fatalf("cancelled must not be audited: %+v", entries)
				}
			} else if len(entries) != 1 || entries[0].Decision != tc.wantAudit {
				t.Fatalf("audit = %+v, want decision %q", entries, tc.wantAudit)
			}
		})
	}
}
