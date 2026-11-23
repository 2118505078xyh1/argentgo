package app

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// TestRecoveryDecisionExposesPlanDetails 证明 recoveryDecisionFor 在同一次分类里
// 暴露了 RecoveryPlan 的细节：1 条断链 + 2 个孤儿工具结果。
// 同时锁定安全语义：NeedsConfirmation 与公告仍完全由分类决定。
func TestRecoveryDecisionExposesPlanDetails(t *testing.T) {
	messages := []session.Entry{
		{ID: "n1", Role: "user", Content: "question"},
		{ID: "n2", Parent: "wrong-parent", Role: "assistant", Content: "answer"},         // 断链
		{ID: "r1", Parent: "n2", Role: "tool", ToolCallID: "ghost-1", Content: "orphan"}, // 孤儿结果 1
		{ID: "r2", Parent: "r1", Role: "tool", ToolCallID: "ghost-2", Content: "orphan"}, // 孤儿结果 2
	}
	got := recoveryDecisionFor(messages)

	if got.Category != string(session.RecoveryDegradedHistory) {
		t.Errorf("Category = %q, want %q", got.Category, session.RecoveryDegradedHistory)
	}
	if got.BrokenLinks != 1 {
		t.Errorf("BrokenLinks = %d, want 1", got.BrokenLinks)
	}
	if !reflect.DeepEqual(got.OrphanToolResults, []string{"ghost-1", "ghost-2"}) {
		t.Errorf("OrphanToolResults = %v, want [ghost-1 ghost-2]", got.OrphanToolResults)
	}
	if len(got.OrphanToolCalls) != 0 {
		t.Errorf("OrphanToolCalls = %v, want empty", got.OrphanToolCalls)
	}
	if !got.NeedsConfirmation {
		t.Error("降级历史必须仍要求确认")
	}
	if got.Notice != noticeDegradedHistory {
		t.Errorf("Notice = %q, want %q", got.Notice, noticeDegradedHistory)
	}
}

// withDetailOptIn 在注入的 env 上叠加 ARGENT_RECOVERY_DETAIL 开关。
func withDetailOptIn(env func(string) string, value string) func(string) string {
	return func(key string) string {
		if key == "ARGENT_RECOVERY_DETAIL" {
			return value
		}
		return env(key)
	}
}

// TestRecoveryDetailEnabledGate 覆盖 env 门控：默认开启，仅修剪后等于 0 才关闭。
func TestRecoveryDetailEnabledGate(t *testing.T) {
	cases := []struct {
		name string
		env  func(string) string
		want bool
	}{
		{"nil env 默认开启", nil, true},
		{"未设置默认开启", envFrom(map[string]string{}), true},
		{"等于 1", envFrom(map[string]string{"ARGENT_RECOVERY_DETAIL": "1"}), true},
		{"带空白的 1", envFrom(map[string]string{"ARGENT_RECOVERY_DETAIL": " 1 "}), true},
		{"真值词保持开启", envFrom(map[string]string{"ARGENT_RECOVERY_DETAIL": "true"}), true},
		{"等于 0 关闭", envFrom(map[string]string{"ARGENT_RECOVERY_DETAIL": "0"}), false},
		{"带空白的 0 关闭", envFrom(map[string]string{"ARGENT_RECOVERY_DETAIL": " \t0\n"}), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := recoveryDetailEnabled(tc.env); got != tc.want {
				t.Fatalf("recoveryDetailEnabled() = %t, want %t", got, tc.want)
			}
		})
	}
}

// TestRecoveryDetailSuffixComposesDetails 覆盖后缀内容、CJK 有界性与纯函数性质。
func TestRecoveryDetailSuffixComposesDetails(t *testing.T) {
	longCJK := strings.Repeat("长", 40)
	decision := recoveryDecision{
		Notice:            noticeDegradedHistory,
		NeedsConfirmation: true,
		Category:          string(session.RecoveryDegradedHistory),
		OrphanToolCalls:   []string{"调用-" + longCJK}, // 长 CJK id，应被有界截断
		OrphanToolResults: []string{"幽灵结果-1", "幽灵结果-2"},
		BrokenLinks:       1,
	}
	suffix := recoveryDetailSuffix(decision)
	for _, want := range []string{
		"恢复细节",
		"类别=degraded_history",
		"断链=1",
		"孤儿调用=1",
		"孤儿结果=2",
		"孤儿调用ID=",
		"孤儿结果ID=幽灵结果-1,幽灵结果-2",
	} {
		if !strings.Contains(suffix, want) {
			t.Errorf("后缀 %q 缺少 %q", suffix, want)
		}
	}
	if !utf8.ValidString(suffix) {
		t.Errorf("后缀不是合法 UTF-8：%q", suffix)
	}
	if strings.Contains(suffix, longCJK) {
		t.Errorf("长 CJK id 未被有界截断：%q", suffix)
	}
	if !strings.Contains(suffix, "…") {
		t.Errorf("截断应留省略号：%q", suffix)
	}
	// 确定性 + 不修改入参。
	if again := recoveryDetailSuffix(decision); again != suffix {
		t.Fatalf("后缀不稳定：首次 %q，再次 %q", suffix, again)
	}
	if decision.BrokenLinks != 1 || len(decision.OrphanToolCalls) != 1 || len(decision.OrphanToolResults) != 2 {
		t.Fatalf("后缀修改了入参：%+v", decision)
	}
}

// TestExecuteRecoveryDetailOptInAddsSuffix 证明 Execute 接线：开启时两个用户可见
// 通道都收到"原始公告 + 细节后缀"。
func TestExecuteRecoveryDetailOptInAddsSuffix(t *testing.T) {
	res, sess, env := interruptedWithOrphan(t)
	base := recoveryDecisionFor(recoveryMessagesForClassification(sess.Messages()))
	if base.Notice == "" {
		t.Fatal("fixture 未产生恢复公告")
	}
	want := base.Notice + recoveryDetailSuffix(base)

	var emitted []agent.Event
	ctx := withEventEmit(context.Background(), func(e agent.Event) { emitted = append(emitted, e) })
	var texts []string
	if _, err := Execute(ctx, cli.IO{Env: withDetailOptIn(env, "1")}, res, sess, "do work", func(s string) { texts = append(texts, s) }); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(texts) == 0 || texts[0] != want {
		t.Fatalf("onText 恢复公告 = %q, want %q", texts, want)
	}
	if !strings.Contains(texts[0], "恢复细节") {
		t.Fatalf("开启时公告应含细节：%q", texts[0])
	}
	if !hasNotice(emitted, want) {
		t.Fatalf("EmitEvent 未发布带细节的恢复公告：%v", emitted)
	}
}

// TestExecuteRecoveryDetailOptOutBaseline 守护关闭基线：=0 关闭时公告逐字节
// 等于原始恢复公告，绝不出现细节字段。
func TestExecuteRecoveryDetailOptOutBaseline(t *testing.T) {
	res, sess, env := interruptedWithOrphan(t)
	baseline := recoveryDecisionFor(recoveryMessagesForClassification(sess.Messages())).Notice
	if baseline == "" {
		t.Fatal("fixture 未产生恢复公告")
	}

	var emitted []agent.Event
	ctx := withEventEmit(context.Background(), func(e agent.Event) { emitted = append(emitted, e) })
	var texts []string
	if _, err := Execute(ctx, cli.IO{Env: env}, res, sess, "do work", func(s string) { texts = append(texts, s) }); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(texts) == 0 || texts[0] != baseline {
		t.Fatalf("关闭基线被改变：onText = %q, want %q", texts, baseline)
	}
	if strings.Contains(texts[0], "恢复细节") {
		t.Fatalf("关闭时不得出现细节后缀：%q", texts[0])
	}
	if !hasNotice(emitted, baseline) {
		t.Fatalf("关闭时事件通知应等于基线：%v", emitted)
	}
}

// TestRecoveryDetailKeepsConfirmationGate 证明细节后缀只增信息：
// NeedsConfirmation 完全由分类决定，带上细节后降级历史仍暂停、可自动继续的历史仍继续。
func TestRecoveryDetailKeepsConfirmationGate(t *testing.T) {
	degraded := recoveryDecisionFor(recoveryMessages("degraded_history"))
	enrichedDegraded := degraded
	enrichedDegraded.Notice += recoveryDetailSuffix(degraded)
	if enrichedDegraded.NeedsConfirmation != degraded.NeedsConfirmation || !degraded.NeedsConfirmation {
		t.Fatal("细节改变了降级的 NeedsConfirmation")
	}

	interrupted := recoveryDecisionFor(recoveryMessages("interrupted_prompt"))
	enrichedInterrupted := interrupted
	enrichedInterrupted.Notice += recoveryDetailSuffix(interrupted)
	if enrichedInterrupted.NeedsConfirmation != interrupted.NeedsConfirmation || interrupted.NeedsConfirmation {
		t.Fatal("细节改变了中断的 NeedsConfirmation")
	}

	for _, tc := range []struct {
		name      string
		decision  recoveryDecision
		wantError bool
	}{
		{"降级基线", degraded, true},
		{"降级带细节", enrichedDegraded, true},
		{"中断基线", interrupted, false},
		{"中断带细节", enrichedInterrupted, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var errBuf bytes.Buffer
			ctx := withEventEmit(context.Background(), func(agent.Event) {})
			err := announceRecovery(ctx, cli.IO{Err: &errBuf}, nil, tc.decision)
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "degraded") {
					t.Fatalf("降级路径必须仍返回确认错误，got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("可自动继续的路径不应报错：%v", err)
			}
		})
	}
}
