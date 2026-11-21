package security

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAdvanceStageOnceCompletesImmediately(t *testing.T) {
	// once 是"仅此一次"：直接完成，不进入任何后续阶段。
	next, completed := AdvanceStage(stageOnce, ApprovalDecision{Choice: ChoiceOnce})
	if !completed {
		t.Fatalf("AdvanceStage(once) completed = false, want true")
	}
	if next != stageOnce {
		t.Fatalf("AdvanceStage(once) next = %d, want %d (stageOnce)", next, stageOnce)
	}
}

func TestAdvanceStageAlwaysRequiresConfirm(t *testing.T) {
	// 选择 always 先进入二次确认阶段，尚未完成。
	next, completed := AdvanceStage(stageOnce, ApprovalDecision{
		Choice: ChoiceAlways,
		Scope:  []string{"write:/项目"},
	})
	if completed || next != stageConfirmScope {
		t.Fatalf("AdvanceStage(always) = (%d, %v), want (%d, false)", next, completed, stageConfirmScope)
	}

	// 未确认时停留在二次确认阶段。
	next, completed = AdvanceStage(stageConfirmScope, ApprovalDecision{Confirmed: false})
	if completed || next != stageConfirmScope {
		t.Fatalf("AdvanceStage(unconfirmed) = (%d, %v), want (%d, false)", next, completed, stageConfirmScope)
	}

	// 只有 Confirmed 才真正提交并完成。
	next, completed = AdvanceStage(stageConfirmScope, ApprovalDecision{Confirmed: true})
	if !completed {
		t.Fatalf("AdvanceStage(confirmed) completed = false, want true")
	}
	if next != stageConfirmScope {
		t.Fatalf("AdvanceStage(confirmed) next = %d, want %d", next, stageConfirmScope)
	}
}

func TestAdvanceStageRejectCollectsFeedback(t *testing.T) {
	// 选择 reject 进入反馈收集阶段，尚未完成。
	next, completed := AdvanceStage(stageOnce, ApprovalDecision{Choice: ChoiceReject})
	if completed || next != stageFeedback {
		t.Fatalf("AdvanceStage(reject) = (%d, %v), want (%d, false)", next, completed, stageFeedback)
	}

	// 仅填入文本但未确认：继续停留在反馈阶段。
	next, completed = AdvanceStage(stageFeedback, ApprovalDecision{Feedback: "请只改 A 文件"})
	if completed || next != stageFeedback {
		t.Fatalf("AdvanceStage(feedback-unconfirmed) = (%d, %v), want (%d, false)", next, completed, stageFeedback)
	}

	// 确认后完成，反馈文本原样保留在入参中供调用方取用（纯函数不改入参）。
	d := ApprovalDecision{Feedback: "请只改 A 文件", Confirmed: true}
	next, completed = AdvanceStage(stageFeedback, d)
	if !completed {
		t.Fatalf("AdvanceStage(feedback-confirmed) completed = false, want true")
	}
	if next != stageFeedback {
		t.Fatalf("AdvanceStage(feedback-confirmed) next = %d, want %d", next, stageFeedback)
	}
	if d.Feedback != "请只改 A 文件" {
		t.Fatalf("AdvanceStage mutated decision.Feedback = %q", d.Feedback)
	}
}

func TestAdvanceStageEscGoesBackNotClose(t *testing.T) {
	// Esc 表示回退上一阶段而非关闭，因此 completed 恒为 false。
	cases := []struct {
		name string
		from ApprovalStage
		want ApprovalStage
	}{
		{name: "from confirm scope", from: stageConfirmScope, want: stageOnce},
		{name: "from feedback", from: stageFeedback, want: stageOnce},
		{name: "from root stays", from: stageOnce, want: stageOnce},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next, completed := AdvanceStage(tc.from, ApprovalDecision{Choice: ChoiceEsc})
			if completed {
				t.Fatalf("Esc completed = true, want false (Esc is back, not close)")
			}
			if next != tc.want {
				t.Fatalf("Esc next = %d, want %d", next, tc.want)
			}
		})
	}
}

func TestAdvanceStageEscPrecedesConfirm(t *testing.T) {
	// 即便同时带了 Confirmed，Esc 仍应优先回退，不能被误判为提交。
	next, completed := AdvanceStage(stageConfirmScope, ApprovalDecision{Choice: ChoiceEsc, Confirmed: true})
	if completed || next != stageOnce {
		t.Fatalf("Esc+Confirm = (%d, %v), want (%d, false)", next, completed, stageOnce)
	}
}

func TestAdvanceStageUnknownChoiceStays(t *testing.T) {
	// 未识别的选择一律停留且未完成，避免误完成审批。
	stages := []ApprovalStage{stageOnce, stageConfirmScope, stageFeedback, ApprovalStage(99)}
	for _, stage := range stages {
		next, completed := AdvanceStage(stage, ApprovalDecision{Choice: "bogus"})
		if completed || next != stage {
			t.Fatalf("AdvanceStage(stage=%d, bogus) = (%d, %v), want (%d, false)", stage, next, completed, stage)
		}
	}
}

func TestScopeSummaryStableCJKSafe(t *testing.T) {
	// 含 CJK 路径、重复项与空项；期望去重、去空、稳定排序后以顿号连接。
	in := []string{
		"写:/项目/文件",
		"读:/etc",
		"写:/项目/文件", // 重复
		"",         // 空项应被丢弃
		"读:/etc",   // 重复
		"默认",
	}
	const want = "写:/项目/文件、读:/etc、默认"

	got := ScopeSummary(in)
	if got != want {
		t.Fatalf("ScopeSummary = %q, want %q", got, want)
	}
	// CJK 安全：输出必须是合法 UTF-8，且每个非空原始作用域都完整保留。
	if !utf8.ValidString(got) {
		t.Fatalf("ScopeSummary produced invalid UTF-8: %q", got)
	}
	for _, s := range in {
		if s == "" {
			continue
		}
		if strings.Contains(s, "、") {
			continue
		}
		if !strings.Contains(got, s) {
			t.Fatalf("ScopeSummary = %q missing scope %q", got, s)
		}
	}
}

func TestScopeSummaryDeterministicAcrossOrders(t *testing.T) {
	// 相同集合的不同输入顺序必须得到相同摘要。
	a := []string{"写:/x", "读:/y", "默认"}
	b := []string{"默认", "写:/x", "读:/y"}
	if got, want := ScopeSummary(a), ScopeSummary(b); got != want {
		t.Fatalf("ScopeSummary not deterministic: %q vs %q", got, want)
	}
}

func TestScopeSummaryEmpty(t *testing.T) {
	cases := [][]string{nil, {}, {"", ""}}
	for _, in := range cases {
		if got := ScopeSummary(in); got != "" {
			t.Fatalf("ScopeSummary(%v) = %q, want empty", in, got)
		}
	}
}

func TestApprovalStagePrimitivesDoNotMutateInputs(t *testing.T) {
	// 纯函数不得修改调用方持有的切片（含底层数组）与结构体入参。
	scope := []string{"b", "a", "写:/路径"}
	backup := append([]string(nil), scope...)
	decision := ApprovalDecision{
		Choice:   ChoiceAlways,
		Scope:    scope,
		Feedback: "原始文本",
	}

	AdvanceStage(stageOnce, decision)
	_ = ScopeSummary(scope)

	if !reflect.DeepEqual(scope, backup) {
		t.Fatalf("scope mutated: got %v, want %v", scope, backup)
	}
	if !reflect.DeepEqual(decision.Scope, backup) {
		t.Fatalf("decision.Scope mutated: got %v, want %v", decision.Scope, backup)
	}
	if decision.Feedback != "原始文本" {
		t.Fatalf("decision.Feedback mutated: %q", decision.Feedback)
	}
}
