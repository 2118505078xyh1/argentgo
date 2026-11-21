package security

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestModelHintFor(t *testing.T) {
	wrappedDenied := fmt.Errorf("registry: %w", ErrDenied)
	wrappedRefusal := fmt.Errorf("tool: %w", &RefusalError{Tool: "write", Feedback: "改成中文再提交"})

	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil error", nil, ""},
		{"unmatched error", errors.New("boom"), ""},
		{"context canceled is not a refusal", context.Canceled, ""},
		{"policy deny via ErrDenied", ErrDenied, "该操作被策略禁止，请换一种方式。"},
		{"policy deny via StopError", &StopError{Tool: "bash", Reason: "tool is on the deny list"}, "该操作被策略禁止，请换一种方式。"},
		{"wrapped policy deny", wrappedDenied, "该操作被策略禁止，请换一种方式。"},
		{"sandbox denied", &StopError{Tool: "write", Reason: "read-only file system"}, "该操作被沙箱拦截，请改用沙箱允许的方式。"},
		{"sandbox denied CJK", &StopError{Tool: "写入文件", Reason: "只读文件系统"}, "该操作被沙箱拦截，请改用沙箱允许的方式。"},
		{"approval declined via ErrRefused", ErrRefused, "操作员否决了该操作，请勿以相同方式重试。"},
		{"approval declined via StopError", &StopError{Tool: "write", Reason: "operator refused", Refused: true}, "操作员否决了该操作，请勿以相同方式重试。"},
		{"tool error recoverable via RefusalError", &RefusalError{Tool: "write", Feedback: "改成中文"}, "该工具调用未获通过，请阅读反馈后调整。可以调整请求后重试。"},
		{"tool error recoverable via plan block", ErrRecoverablePlanBlock, "该工具调用未获通过，请阅读反馈后调整。可以调整请求后重试。"},
		{"wrapped tool error", wrappedRefusal, "该工具调用未获通过，请阅读反馈后调整。可以调整请求后重试。"},
		{"tool error via FeedbackRefusal", &FeedbackRefusal{Feedback: "请改写 config"}, "该工具调用未获通过，请阅读反馈后调整。可以调整请求后重试。"},
		{"unavailable", ErrApprovalUnavailable, "审批通道当前不可用，请稍后再试或改用无需审批的方式。"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ModelHintFor(tc.err)
			if got != tc.want {
				t.Fatalf("ModelHintFor(%v) = %q, want %q", tc.err, got, tc.want)
			}
			// 与 ReasonFromError(...).ModelHint() 保持一致。
			if r, ok := ReasonFromError(tc.err); ok && got != r.ModelHint() {
				t.Fatalf("ModelHintFor(%v) = %q, but reason.ModelHint() = %q", tc.err, got, r.ModelHint())
			}
			// 稳定性：同一 err 多次调用逐字节一致。
			if again := ModelHintFor(tc.err); again != got {
				t.Fatalf("ModelHintFor(%v) is unstable: %q then %q", tc.err, got, again)
			}
		})
	}
}

func TestShouldRetry(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"unmatched error", errors.New("boom"), false},
		{"context canceled", context.Canceled, false},
		{"tool error RefusalError", &RefusalError{Tool: "write", Feedback: "改成中文"}, true},
		{"tool error FeedbackRefusal", &FeedbackRefusal{Feedback: "请改写 config"}, true},
		{"tool error plan block", ErrRecoverablePlanBlock, true},
		{"wrapped tool error", fmt.Errorf("tool: %w", &RefusalError{Tool: "bash", Feedback: "feedback"}), true},
		{"joined tool error", errors.Join(errors.New("side effect"), ErrRecoverablePlanBlock), true},
		{"approval declined ErrRefused", ErrRefused, false},
		{"approval declined StopError", &StopError{Tool: "write", Reason: "operator refused", Refused: true}, false},
		{"policy deny ErrDenied", ErrDenied, false},
		{"policy deny StopError", &StopError{Tool: "bash", Reason: "tool is on the deny list"}, false},
		{"sandbox denied", &StopError{Tool: "write", Reason: "read-only file system"}, false},
		{"unavailable", ErrApprovalUnavailable, false},
		{"wrapped unavailable", fmt.Errorf("security: %w", ErrApprovalUnavailable), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldRetry(tc.err); got != tc.want {
				t.Fatalf("ShouldRetry(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestRefusalAuditLine(t *testing.T) {
	tests := []struct {
		name      string
		reason    RefusalReason
		wantKind  string
		wantTool  string
		forbidden string
	}{
		{
			name:      "policy deny",
			reason:    RefusalReason{Kind: RefusalKindPolicyDeny, Tool: "bash", Detail: "原始输出 SECRET-DETAIL"},
			wantKind:  "policy-deny",
			wantTool:  "bash",
			forbidden: "SECRET-DETAIL",
		},
		{
			name:      "CJK tool and CJK detail",
			reason:    RefusalReason{Kind: RefusalKindSandboxDenied, Tool: "写入文件", Detail: "工具返回了敏感文本"},
			wantKind:  "sandbox-denied",
			wantTool:  "写入文件",
			forbidden: "工具返回了敏感文本",
		},
		{
			name:      "empty tool",
			reason:    RefusalReason{Kind: RefusalKindUnavailable, Detail: "审批通道连接失败"},
			wantKind:  "unavailable",
			forbidden: "审批通道连接失败",
		},
		{
			name:      "tool with control characters",
			reason:    RefusalReason{Kind: RefusalKindToolError, Tool: "write\nfile\tok", Detail: "feedback"},
			wantKind:  "tool-error",
			wantTool:  "write",
			forbidden: "feedback",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			line := RefusalAuditLine(tc.reason)
			if !strings.Contains(line, tc.wantKind) {
				t.Errorf("RefusalAuditLine(%+v) = %q, missing kind %q", tc.reason, line, tc.wantKind)
			}
			if tc.wantTool != "" && !strings.Contains(line, tc.wantTool) {
				t.Errorf("RefusalAuditLine(%+v) = %q, missing tool %q", tc.reason, line, tc.wantTool)
			}
			if tc.forbidden != "" && strings.Contains(line, tc.forbidden) {
				t.Errorf("RefusalAuditLine(%+v) = %q must not contain detail %q", tc.reason, line, tc.forbidden)
			}
			// 单行：不含任何 ASCII 换行或 Unicode 行分隔符。
			if strings.ContainsAny(line, "\r\n") || strings.Contains(line, "\u2028") || strings.Contains(line, "\u2029") {
				t.Errorf("RefusalAuditLine(%+v) = %q is not single-line", tc.reason, line)
			}
			// 稳定性：同一结构体多次调用逐字节一致，且不修改入参。
			if again := RefusalAuditLine(tc.reason); again != line {
				t.Fatalf("RefusalAuditLine is unstable: %q then %q", line, again)
			}
			if tc.reason != (RefusalReason{Kind: tc.reason.Kind, Tool: tc.reason.Tool, Detail: tc.reason.Detail, Recoverable: tc.reason.Recoverable}) {
				t.Fatal("RefusalAuditLine mutated its input")
			}
		})
	}
}

func TestRefusalAuditLineCJKAndEscaping(t *testing.T) {
	// CJK 与 emoji 必须逐字节原样保留，不能被 \uXXXX 转义或切坏。
	tool := "删除文件😀"
	line := RefusalAuditLine(RefusalReason{Kind: RefusalKindPolicyDeny, Tool: tool, Detail: "sensitive"})
	if !strings.Contains(line, tool) {
		t.Fatalf("CJK tool not preserved verbatim: %q", line)
	}
	if !utf8.ValidString(line) {
		t.Fatalf("RefusalAuditLine produced invalid UTF-8: %q", line)
	}

	// 控制字符被转义为可见序列，而非原样换行。
	line = RefusalAuditLine(RefusalReason{Kind: RefusalKindToolError, Tool: "a\\b\nc\rd\te\u2028f"})
	for _, escaped := range []string{"\\\\", "\\n", "\\r", "\\t", "\\u2028"} {
		if !strings.Contains(line, escaped) {
			t.Errorf("RefusalAuditLine = %q, missing escape %q", line, escaped)
		}
	}
	if strings.ContainsAny(line, "\r\n\u2028") {
		t.Errorf("RefusalAuditLine = %q must be single-line", line)
	}
}

func TestMergeRefusalsPriority(t *testing.T) {
	policyDeny := ErrDenied
	sandboxDenied := &StopError{Tool: "write", Reason: "read-only file system"}
	approvalDeclined := &StopError{Tool: "write", Reason: "operator refused", Refused: true}
	toolError := &RefusalError{Tool: "bash", Feedback: "改成中文"}
	unavailable := ErrApprovalUnavailable

	tests := []struct {
		name string
		errs []error
		want RefusalKind
	}{
		{"no errors", nil, ""},
		{"only unmatched", []error{errors.New("boom"), context.Canceled}, ""},
		{"single policy deny", []error{policyDeny}, RefusalKindPolicyDeny},
		{"policy deny beats sandbox", []error{sandboxDenied, policyDeny}, RefusalKindPolicyDeny},
		{"sandbox beats approval", []error{approvalDeclined, sandboxDenied}, RefusalKindSandboxDenied},
		{"approval beats tool error", []error{toolError, approvalDeclined}, RefusalKindApprovalDeclined},
		{"tool error beats unavailable", []error{unavailable, toolError}, RefusalKindToolError},
		{"all kinds policy wins", []error{unavailable, toolError, approvalDeclined, sandboxDenied, policyDeny}, RefusalKindPolicyDeny},
		{"unmatched ignored", []error{errors.New("boom"), unavailable, errors.New("nope")}, RefusalKindUnavailable},
		{"wrapped穿透", []error{fmt.Errorf("registry: %w", ErrDenied)}, RefusalKindPolicyDeny},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MergeRefusals(tc.errs...); got.Kind != tc.want {
				t.Fatalf("MergeRefusals(%v).Kind = %q, want %q", tc.errs, got.Kind, tc.want)
			}
		})
	}
}

func TestMergeRefusalsDetailsAndStability(t *testing.T) {
	first := &RefusalError{Tool: "first", Feedback: "first feedback"}
	second := &RefusalError{Tool: "second", Feedback: "second feedback"}
	// 同分：先出现者胜出，且返回的是该错误的完整 RefusalReason。
	got := MergeRefusals(first, second)
	want := RefusalReason{Kind: RefusalKindToolError, Tool: "first", Detail: "first feedback", Recoverable: true}
	if got != want {
		t.Fatalf("MergeRefusals tie = %+v, want %+v", got, want)
	}
	if again := MergeRefusals(first, second); again != got {
		t.Fatalf("MergeRefusals is unstable: %+v then %+v", got, again)
	}

	// 不同分：返回获胜错误的完整字段，而不只是类别。
	winner := &StopError{Tool: "删除文件", Reason: "只读文件系统"}
	got = MergeRefusals(ErrApprovalUnavailable, winner)
	want = RefusalReason{Kind: RefusalKindSandboxDenied, Tool: "删除文件", Detail: "只读文件系统"}
	if got != want {
		t.Fatalf("MergeRefusals detail = %+v, want %+v", got, want)
	}
}

func TestConsumersDoNotMutateErrors(t *testing.T) {
	stop := &StopError{Tool: "bash", Reason: "read-only file system"}
	refusal := &RefusalError{Tool: "write", Feedback: "改成中文"}
	stopBefore, refusalBefore := *stop, *refusal
	stopText, refusalText := stop.Error(), refusal.Error()

	_ = ModelHintFor(stop)
	_ = ShouldRetry(stop)
	_ = MergeRefusals(stop, refusal)
	_ = MergeRefusals(refusal, stop)

	if *stop != stopBefore || stop.Error() != stopText {
		t.Fatalf("StopError was mutated: got %+v / %q", *stop, stop.Error())
	}
	if *refusal != refusalBefore || refusal.Error() != refusalText {
		t.Fatalf("RefusalError was mutated: got %+v / %q", *refusal, refusal.Error())
	}
}
