package app

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

func TestStagedApprovalEnabled(t *testing.T) {
	if stagedApprovalEnabled(func(string) string { return "" }) {
		t.Fatal("default off")
	}
	if !stagedApprovalEnabled(func(string) string { return "1" }) {
		t.Fatal("env=1 on")
	}
}

func TestStagedApproverAskStageAndRemember(t *testing.T) {
	a := newStagedApprover(strings.NewReader(""), &bytes.Buffer{})

	// once 阶段输入 always → 携带作用域进入确认阶段。
	a2 := newStagedApprover(strings.NewReader("always\n"), &bytes.Buffer{})
	d, err := a2.AskStage(context.Background(), "write", "{}", security.StageOnce, "")
	if err != nil || d.Choice != security.ChoiceAlways || len(d.Scope) != 1 {
		t.Fatalf("always decision = %+v, %v", d, err)
	}
	// 确认阶段输入 y → Confirmed。
	a3 := newStagedApprover(strings.NewReader("y\n"), &bytes.Buffer{})
	d, err = a3.AskStage(context.Background(), "write", "{}", security.StageConfirmScope, "write")
	if err != nil || !d.Confirmed {
		t.Fatalf("confirm decision = %+v, %v", d, err)
	}
	// 反馈阶段带文本 → Confirmed + Feedback。
	a4 := newStagedApprover(strings.NewReader("bad idea\n"), &bytes.Buffer{})
	d, err = a4.AskStage(context.Background(), "write", "{}", security.StageFeedback, "")
	if err != nil || !d.Confirmed || d.Feedback != "bad idea" {
		t.Fatalf("feedback decision = %+v, %v", d, err)
	}
	// RememberScope 后 Ask 直接批准（无 stdin）。
	a.RememberScope([]string{"write"})
	ok, err := a.Ask(context.Background(), "write", "{}")
	if err != nil || !ok {
		t.Fatalf("remembered scope should approve: %v %v", ok, err)
	}
}
