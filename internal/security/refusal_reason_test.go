package security

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestRefusalKindLiterals(t *testing.T) {
	// 类别字面值是稳定契约：调用方与下游聚合都依赖这些拼写。
	want := map[RefusalKind]string{
		RefusalKindPolicyDeny:       "policy-deny",
		RefusalKindApprovalDeclined: "approval-declined",
		RefusalKindSandboxDenied:    "sandbox-denied",
		RefusalKindToolError:        "tool-error",
		RefusalKindUnavailable:      "unavailable",
	}
	for kind, literal := range want {
		if string(kind) != literal {
			t.Errorf("RefusalKind = %q, want %q", kind, literal)
		}
	}
}

func TestReasonFromErrorMapping(t *testing.T) {
	// 包装链用于验证 errors.Is / errors.As 的穿透语义。
	wrappedDenied := fmt.Errorf("registry: %w", ErrDenied)
	wrappedRefused := fmt.Errorf("registry: %w", ErrRefused)
	wrappedPlanBlock := fmt.Errorf("tool %q rejected: %w", "bash", ErrRecoverablePlanBlock)
	wrappedUnavailable := fmt.Errorf("security: approval for the write tool call failed: %w", ErrApprovalUnavailable)
	wrappedStop := fmt.Errorf("registry: %w", &StopError{Tool: "bash", Reason: "tool is on the deny list"})
	wrappedStopRefused := fmt.Errorf("registry: %w", &StopError{Tool: "write", Reason: "the operator refused the tool call", Refused: true})
	wrappedRefusal := fmt.Errorf("tool: %w", &RefusalError{Tool: "write", Feedback: "改成中文再提交"})
	joinedDenied := errors.Join(errors.New("side effect"), ErrDenied)

	tests := []struct {
		name       string
		err        error
		wantKind   RefusalKind
		wantTool   string
		wantDetail string
		wantRecov  bool
		wantOK     bool
	}{
		{"nil error", nil, "", "", "", false, false},
		{"unmatched error", errors.New("boom"), "", "", "", false, false},
		{"context canceled is not a refusal", context.Canceled, "", "", "", false, false},
		{"bare ErrDenied", ErrDenied, RefusalKindPolicyDeny, "", ErrDenied.Error(), false, true},
		{"wrapped ErrDenied", wrappedDenied, RefusalKindPolicyDeny, "", wrappedDenied.Error(), false, true},
		{"joined ErrDenied", joinedDenied, RefusalKindPolicyDeny, "", joinedDenied.Error(), false, true},
		{"bare ErrRefused", ErrRefused, RefusalKindApprovalDeclined, "", ErrRefused.Error(), false, true},
		{"wrapped ErrRefused", wrappedRefused, RefusalKindApprovalDeclined, "", wrappedRefused.Error(), false, true},
		{"ErrRecoverablePlanBlock", ErrRecoverablePlanBlock, RefusalKindToolError, "", ErrRecoverablePlanBlock.Error(), true, true},
		{"wrapped ErrRecoverablePlanBlock", wrappedPlanBlock, RefusalKindToolError, "", wrappedPlanBlock.Error(), true, true},
		{"ErrApprovalUnavailable", ErrApprovalUnavailable, RefusalKindUnavailable, "", ErrApprovalUnavailable.Error(), false, true},
		{"wrapped ErrApprovalUnavailable", wrappedUnavailable, RefusalKindUnavailable, "", wrappedUnavailable.Error(), false, true},
		{"StopError policy deny", &StopError{Tool: "bash", Reason: "tool is on the deny list"}, RefusalKindPolicyDeny, "bash", "tool is on the deny list", false, true},
		{"wrapped StopError policy deny", wrappedStop, RefusalKindPolicyDeny, "bash", "tool is on the deny list", false, true},
		{"StopError operator refused", &StopError{Tool: "write", Reason: "the operator refused the tool call", Refused: true}, RefusalKindApprovalDeclined, "write", "the operator refused the tool call", false, true},
		{"wrapped StopError operator refused", wrappedStopRefused, RefusalKindApprovalDeclined, "write", "the operator refused the tool call", false, true},
		{"StopError sandbox reason", &StopError{Tool: "write", Reason: "read-only file system"}, RefusalKindSandboxDenied, "write", "read-only file system", false, true},
		{"CJK tool and detail", &StopError{Tool: "删除文件", Reason: "工具在禁用清单中"}, RefusalKindPolicyDeny, "删除文件", "工具在禁用清单中", false, true},
		{"CJK sandbox detail", &StopError{Tool: "写入文件", Reason: "只读文件系统"}, RefusalKindSandboxDenied, "写入文件", "只读文件系统", false, true},
		{"RefusalError with CJK feedback", &RefusalError{Tool: "write", Feedback: "改成中文再提交"}, RefusalKindToolError, "write", "改成中文再提交", true, true},
		{"wrapped RefusalError", wrappedRefusal, RefusalKindToolError, "write", "改成中文再提交", true, true},
		{"FeedbackRefusal", &FeedbackRefusal{Feedback: "请改写 config"}, RefusalKindToolError, "", "请改写 config", true, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ReasonFromError(tc.err)
			if ok != tc.wantOK {
				t.Fatalf("ReasonFromError(%v) ok = %v, want %v", tc.err, ok, tc.wantOK)
			}
			want := RefusalReason{Kind: tc.wantKind, Tool: tc.wantTool, Detail: tc.wantDetail, Recoverable: tc.wantRecov}
			if got != want {
				t.Fatalf("ReasonFromError(%v) = %+v, want %+v", tc.err, got, want)
			}
		})
	}
}

func TestReasonFromErrorDoesNotMutateInput(t *testing.T) {
	stop := &StopError{Tool: "bash", Reason: "denied by policy"}
	refusal := &RefusalError{Tool: "write", Feedback: "改成中文"}
	stopBefore, refusalBefore := *stop, *refusal
	stopText, refusalText := stop.Error(), refusal.Error()

	if _, ok := ReasonFromError(stop); !ok {
		t.Fatal("ReasonFromError(stop) must match")
	}
	if _, ok := ReasonFromError(refusal); !ok {
		t.Fatal("ReasonFromError(refusal) must match")
	}

	if *stop != stopBefore || stop.Error() != stopText {
		t.Fatalf("StopError was mutated: got %+v / %q", *stop, stop.Error())
	}
	if *refusal != refusalBefore || refusal.Error() != refusalText {
		t.Fatalf("RefusalError was mutated: got %+v / %q", *refusal, refusal.Error())
	}
}

func TestModelHint(t *testing.T) {
	tests := []struct {
		name   string
		reason RefusalReason
		want   string
	}{
		{"policy deny", RefusalReason{Kind: RefusalKindPolicyDeny}, "该操作被策略禁止，请换一种方式。"},
		{"approval declined", RefusalReason{Kind: RefusalKindApprovalDeclined}, "操作员否决了该操作，请勿以相同方式重试。"},
		{"sandbox denied", RefusalReason{Kind: RefusalKindSandboxDenied}, "该操作被沙箱拦截，请改用沙箱允许的方式。"},
		{"tool error recoverable", RefusalReason{Kind: RefusalKindToolError, Recoverable: true}, "该工具调用未获通过，请阅读反馈后调整。可以调整请求后重试。"},
		{"unavailable", RefusalReason{Kind: RefusalKindUnavailable}, "审批通道当前不可用，请稍后再试或改用无需审批的方式。"},
		{"zero value", RefusalReason{}, "该操作未能完成，请换一种方式。"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.reason.ModelHint()
			if got != tc.want {
				t.Fatalf("ModelHint() = %q, want %q", got, tc.want)
			}
			// 稳定性：同一结构体的提示必须逐字节一致。
			if again := tc.reason.ModelHint(); again != got {
				t.Fatalf("ModelHint() is unstable: %q then %q", got, again)
			}
		})
	}
}

func TestModelHintRecoverableGuidance(t *testing.T) {
	kinds := []RefusalKind{
		RefusalKindPolicyDeny,
		RefusalKindApprovalDeclined,
		RefusalKindSandboxDenied,
		RefusalKindToolError,
		RefusalKindUnavailable,
	}
	for _, kind := range kinds {
		hint := (RefusalReason{Kind: kind, Recoverable: true}).ModelHint()
		if !strings.Contains(hint, "可以调整请求后重试") {
			t.Errorf("kind %q with Recoverable=true hint %q lacks retry guidance", kind, hint)
		}
	}
}

func TestModelHintIgnoresToolAndDetail(t *testing.T) {
	a := RefusalReason{Kind: RefusalKindPolicyDeny, Tool: "bash", Detail: "English detail"}
	b := RefusalReason{Kind: RefusalKindPolicyDeny, Tool: "删除文件", Detail: "任意中文细节"}
	if a.ModelHint() != b.ModelHint() {
		t.Fatalf("ModelHint must depend only on Kind/Recoverable: %q vs %q", a.ModelHint(), b.ModelHint())
	}
}
