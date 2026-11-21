package security

// 本文件把“结构化拒绝原因”这一组纯函数接到真实的安全链上做守护：
// 用 NewChain + 策略 + 审批器 + 审计日志触发三类拒绝，再把 Chain.Check
// 返回的错误交给 ReasonFromError / ShouldRetry / RefusalAuditLine /
// MergeRefusals，验证分类、fail-closed 重试决策、脱敏与优先级在端到端路径上
// 依然成立。这里只新增测试，不改动 chain.go / refusal_reason.go /
// refusal_consume.go 的任何生产逻辑，也不改动已有测试。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// refusalChainApprover 是一个最小可配置审批器，覆盖同意、纯否决与带反馈否决
// 三种回答；经 Ask(bool, error) 这一所有审批器共用的 API 驱动真实链。
type refusalChainApprover struct {
	allow    bool
	feedback string
	err      error
}

func (a *refusalChainApprover) Ask(ctx context.Context, _ string, _ string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if a.err != nil {
		return false, a.err
	}
	if a.allow {
		return true, nil
	}
	if a.feedback != "" {
		return false, &FeedbackRefusal{Feedback: a.feedback}
	}
	return false, nil
}

// newRefusalChain 用真实的策略、审批器与审计日志构建关卡。
func newRefusalChain(t *testing.T, policy *Policy, approver Approver) *Chain {
	t.Helper()
	dir := t.TempDir()
	return NewChain(policy, approver, NewAuditLog(dir), "refusal-integration")
}

// TestRefusalChainEndToEndReasons 用真实安全链触发三类拒绝，并断言
// ReasonFromError 的分类、工具名、细节与可恢复标志逐项正确。
func TestRefusalChainEndToEndReasons(t *testing.T) {
	const feedback = "改成中文再提交"

	tests := []struct {
		name        string
		chain       func(t *testing.T) *Chain
		tool        string
		args        json.RawMessage
		wantKind    RefusalKind
		wantRecover bool
		wantTool    string
		wantDetail  string
		checkErr    func(t *testing.T, err error)
	}{
		{
			name: "策略拒绝产生 StopError 并归类为 policy-deny",
			chain: func(t *testing.T) *Chain {
				dir := t.TempDir()
				return NewChain(
					NewPolicy(dir, PolicyConfig{DenyTools: []string{"bash"}}),
					&refusalChainApprover{allow: true},
					NewAuditLog(dir), "refusal-integration")
			},
			tool:     "bash",
			args:     json.RawMessage(`{"command":"ls"}`),
			wantKind: RefusalKindPolicyDeny,
			wantTool: "bash",
			checkErr: func(t *testing.T, err error) {
				if !errors.Is(err, ErrDenied) || errors.Is(err, ErrRefused) {
					t.Fatalf("got %v, want ErrDenied only", err)
				}
				var stop *StopError
				if !errors.As(err, &stop) || stop.Refused {
					t.Fatalf("got %#v, want a denying StopError", err)
				}
			},
		},
		{
			name: "操作员否决产生 Refused StopError 并归类为 approval-declined",
			chain: func(t *testing.T) *Chain {
				return newRefusalChain(t, DefaultPolicy(t.TempDir()),
					&refusalChainApprover{allow: false})
			},
			tool:       "write",
			args:       json.RawMessage(`{"path":"a.txt"}`),
			wantKind:   RefusalKindApprovalDeclined,
			wantTool:   "write",
			wantDetail: "the operator refused the tool call",
			checkErr: func(t *testing.T, err error) {
				if !errors.Is(err, ErrRefused) || errors.Is(err, ErrDenied) {
					t.Fatalf("got %v, want ErrRefused only", err)
				}
				var stop *StopError
				if !errors.As(err, &stop) || !stop.Refused {
					t.Fatalf("got %#v, want a refusing StopError", err)
				}
			},
		},
		{
			name: "带反馈否决经 FeedbackRefusal 转成 RefusalError 并归类为 tool-error",
			chain: func(t *testing.T) *Chain {
				return newRefusalChain(t, DefaultPolicy(t.TempDir()),
					&refusalChainApprover{feedback: feedback})
			},
			tool:        "write",
			args:        json.RawMessage(`{"path":"a.txt"}`),
			wantKind:    RefusalKindToolError,
			wantRecover: true,
			wantTool:    "write",
			wantDetail:  feedback,
			checkErr: func(t *testing.T, err error) {
				var refusal *RefusalError
				if !errors.As(err, &refusal) {
					t.Fatalf("got %v, want *RefusalError", err)
				}
				if refusal.Feedback != feedback {
					t.Fatalf("feedback = %q, want %q", refusal.Feedback, feedback)
				}
				if !errors.Is(err, ErrRefused) || errors.Is(err, ErrDenied) {
					t.Fatalf("got %v, want ErrRefused only", err)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.chain(t).Check(context.Background(), tc.tool, tc.args)
			if err == nil {
				t.Fatal("refusal must produce an error")
			}
			tc.checkErr(t, err)

			reason, ok := ReasonFromError(err)
			if !ok {
				t.Fatalf("ReasonFromError(%v) did not match", err)
			}
			if reason.Kind != tc.wantKind {
				t.Fatalf("Kind = %q, want %q", reason.Kind, tc.wantKind)
			}
			if reason.Recoverable != tc.wantRecover {
				t.Fatalf("Recoverable = %v, want %v", reason.Recoverable, tc.wantRecover)
			}
			if tc.wantTool != "" && reason.Tool != tc.wantTool {
				t.Fatalf("Tool = %q, want %q", reason.Tool, tc.wantTool)
			}
			if tc.wantDetail != "" && reason.Detail != tc.wantDetail {
				t.Fatalf("Detail = %q, want %q", reason.Detail, tc.wantDetail)
			}
		})
	}
}

// TestRefusalChainSandboxReasonClassification 守护沙箱分类：安全链没有专门的
// 沙箱 error 类型，ReasonFromError 复用同包分类器从 StopError.Reason 识别。
func TestRefusalChainSandboxReasonClassification(t *testing.T) {
	err := &StopError{Tool: "write", Reason: "read-only file system"}
	reason, ok := ReasonFromError(err)
	if !ok {
		t.Fatal("sandbox StopError must match")
	}
	if reason.Kind != RefusalKindSandboxDenied {
		t.Fatalf("Kind = %q, want %q", reason.Kind, RefusalKindSandboxDenied)
	}
	if reason.Tool != "write" || reason.Detail != "read-only file system" {
		t.Fatalf("reason = %+v, want tool/detail preserved", reason)
	}
	if reason.Recoverable {
		t.Fatal("sandbox denial must not be recoverable")
	}
}

// TestShouldRetryFailClosedOnRefusals 验证三类硬性拒绝与通道不可用一律不重试，
// 只有可恢复的工具错误（含链产出的 RefusalError 与计划模式拦截）才重试。
func TestShouldRetryFailClosedOnRefusals(t *testing.T) {
	const feedback = "改成中文再提交"
	ctx := context.Background()

	denyChain := newRefusalChain(t,
		NewPolicy(t.TempDir(), PolicyConfig{DenyTools: []string{"bash"}}),
		&refusalChainApprover{allow: true})
	policyErr := denyChain.Check(ctx, "bash", json.RawMessage(`{"command":"ls"}`))

	approvalChain := newRefusalChain(t, DefaultPolicy(t.TempDir()),
		&refusalChainApprover{allow: false})
	approvalErr := approvalChain.Check(ctx, "write", json.RawMessage(`{"path":"a.txt"}`))

	feedbackChain := newRefusalChain(t, DefaultPolicy(t.TempDir()),
		&refusalChainApprover{feedback: feedback})
	feedbackErr := feedbackChain.Check(ctx, "write", json.RawMessage(`{"path":"a.txt"}`))

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"策略拒绝（链）", policyErr, false},
		{"操作员否决（链）", approvalErr, false},
		{"沙箱拦截", &StopError{Tool: "write", Reason: "read-only file system"}, false},
		{"审批通道不可用", ErrApprovalUnavailable, false},
		{"未匹配错误", errors.New("boom"), false},
		{"带反馈拒绝（链，可恢复工具错误）", feedbackErr, true},
		{"计划模式拦截（可恢复工具错误）", ErrRecoverablePlanBlock, true},
		{"包装后的可恢复工具错误", fmt.Errorf("registry: %w", feedbackErr), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldRetry(tc.err); got != tc.want {
				t.Fatalf("ShouldRetry(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestRefusalAuditLineRedactsFeedbackAndKeepsCJK 守护审计脱敏：原始反馈/输出
// 原文不得进入单行审计描述，CJK 工具名须逐字节保留且输出仍是合法 UTF-8 单行。
func TestRefusalAuditLineRedactsFeedbackAndKeepsCJK(t *testing.T) {
	const secret = "SECRET-原文-DO-NOT-LEAK-机密反馈"
	ctx := context.Background()

	chain := newRefusalChain(t, DefaultPolicy(t.TempDir()),
		&refusalChainApprover{feedback: secret})
	err := chain.Check(ctx, "write", json.RawMessage(`{"path":"a.txt"}`))
	var refusal *RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("got %v, want *RefusalError", err)
	}
	reason, ok := ReasonFromError(err)
	if !ok {
		t.Fatal("RefusalError must map to a reason")
	}

	line := RefusalAuditLine(reason)
	if strings.Contains(line, secret) {
		t.Fatalf("audit line leaked raw feedback: %q", line)
	}
	if !strings.Contains(line, "tool-error") || !strings.Contains(line, "write") {
		t.Fatalf("audit line = %q, missing kind/tool", line)
	}

	// 审计日志文件本身也不得出现原始反馈。
	entries, readErr := ReadAudit(chain.Audit.Path())
	if readErr != nil {
		t.Fatalf("ReadAudit: %v", readErr)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Reason, secret) || strings.Contains(entry.Args, secret) {
			t.Fatalf("audit entry leaked raw feedback: %+v", entry)
		}
	}

	// CJK 工具名原样保留；Detail 里的原文与换行分隔符均不得泄漏/破坏单行。
	cjk := RefusalReason{Kind: RefusalKindToolError, Tool: "删除文件😀", Detail: secret + "\n第二行"}
	cjkLine := RefusalAuditLine(cjk)
	if !strings.Contains(cjkLine, "删除文件😀") {
		t.Fatalf("CJK tool not preserved: %q", cjkLine)
	}
	if strings.Contains(cjkLine, secret) {
		t.Fatalf("audit line leaked raw detail: %q", cjkLine)
	}
	if !utf8.ValidString(cjkLine) {
		t.Fatalf("invalid UTF-8 in audit line: %q", cjkLine)
	}
	if strings.ContainsAny(cjkLine, "\r\n\u2028\u2029") {
		t.Fatalf("audit line is not single-line: %q", cjkLine)
	}
}

// TestMergeRefusalsWrappedErrorsAndPriority 验证 MergeRefusals 的严重程度优先级，
// 以及 errors.Is / errors.As 对包装与 joined 错误的穿透：被包装的错误仍需被
// 正确识别并按类别参与归并。
func TestMergeRefusalsWrappedErrorsAndPriority(t *testing.T) {
	wrappedDenied := fmt.Errorf("registry: %w", ErrDenied)
	joinedDenied := errors.Join(errors.New("side effect"), ErrDenied)
	wrappedApproval := fmt.Errorf("approval: %w",
		&StopError{Tool: "write", Reason: "the operator refused", Refused: true})
	wrappedSandbox := fmt.Errorf("tool: %w",
		&StopError{Tool: "write", Reason: "read-only file system"})
	wrappedToolErr := fmt.Errorf("tool: %w",
		&RefusalError{Tool: "bash", Feedback: "改成中文"})

	// 包装链不应改变哨兵/类型匹配的语义，这是归并能识别它们的前提。
	if !errors.Is(wrappedApproval, ErrRefused) {
		t.Fatal("errors.Is(wrappedApproval, ErrRefused) must penetrate wrapping")
	}
	var stop *StopError
	if !errors.As(wrappedApproval, &stop) || !stop.Refused {
		t.Fatal("errors.As(wrappedApproval, *StopError) must penetrate wrapping")
	}
	if !errors.Is(joinedDenied, ErrDenied) {
		t.Fatal("errors.Is(joinedDenied, ErrDenied) must penetrate joining")
	}
	var refusal *RefusalError
	if !errors.As(wrappedToolErr, &refusal) {
		t.Fatal("errors.As(wrappedToolErr, *RefusalError) must penetrate wrapping")
	}

	tests := []struct {
		name string
		errs []error
		want RefusalKind
	}{
		{"包装的策略拒绝胜过包装的沙箱拦截", []error{wrappedSandbox, wrappedDenied}, RefusalKindPolicyDeny},
		{"joined 策略拒绝胜过包装的沙箱拦截", []error{wrappedSandbox, joinedDenied}, RefusalKindPolicyDeny},
		{"包装的操作员否决胜过包装的工具错误", []error{wrappedToolErr, wrappedApproval}, RefusalKindApprovalDeclined},
		{"包装的沙箱拦截胜过包装的操作员否决", []error{wrappedApproval, wrappedSandbox}, RefusalKindSandboxDenied},
		{"单独包装的工具错误仍被识别", []error{wrappedToolErr}, RefusalKindToolError},
		{"未匹配错误被忽略，包装工具错误胜出", []error{errors.New("boom"), wrappedToolErr}, RefusalKindToolError},
		{"全类别混合，策略拒绝最严重", []error{
			wrappedToolErr, ErrApprovalUnavailable, wrappedApproval, wrappedSandbox, wrappedDenied,
		}, RefusalKindPolicyDeny},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MergeRefusals(tc.errs...)
			if got.Kind != tc.want {
				t.Fatalf("MergeRefusals(...).Kind = %q, want %q", got.Kind, tc.want)
			}
			// 单个错误时，归并结果应与 ReasonFromError 完全一致（含字段）。
			if len(tc.errs) == 1 {
				direct, ok := ReasonFromError(tc.errs[0])
				if !ok || got != direct {
					t.Fatalf("MergeRefusals(single) = %+v, ReasonFromError = %+v (ok=%v)", got, direct, ok)
				}
			}
		})
	}
}
