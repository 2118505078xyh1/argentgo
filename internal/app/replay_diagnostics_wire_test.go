package app

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/session"
)

// withReplayOptIn 在既有 env 上叠加门控值，返回供 Execute 使用的读取器。
func withReplayOptIn(env func(string) string, value string) func(string) string {
	return func(key string) string {
		if strings.TrimSpace(key) == "ARGENT_RECOVERY_REPLAY" {
			return value
		}
		return env(key)
	}
}

// interruptedWithOrphan 构造一份"末尾用户提问 + 中途未配对工具调用"的会话。
// 原始记录流里 assistant 声明了未被回应的 c1，因此 Replay 会报告不完整；
// 而 Messages() 的结算让投影仍是可自动继续的中断提示，产生恢复公告。
func interruptedWithOrphan(t *testing.T) (Resolved, *session.Session, func(string) string) {
	t.Helper()
	baseEnv, root, _ := wiringEnv(t)
	// 两个恢复公告后缀门控现已默认开启；此夹具显式关闭它们，使测试能单独验证
	// 某一个后缀，调用方再用 withReplayOptIn / withDetailOptIn 逐个打开。
	env := func(key string) string {
		switch strings.TrimSpace(key) {
		case "ARGENT_RECOVERY_REPLAY", "ARGENT_RECOVERY_DETAIL":
			return "0"
		}
		return baseEnv(key)
	}
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	res.Approve = autoApprover{}
	res.Chatter = &scriptedChatter{replies: []string{"done"}}
	store := testStore(t, res)
	sess, err := store.NewSession("replay-diagnostics")
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		kind string
		data any
	}{
		{session.KindUser, "question"},
		{session.KindAssistant, map[string]any{
			"content":    "calling tool",
			"tool_calls": []map[string]any{{"id": "c1", "name": "shell", "arguments": "{}"}},
		}},
		{session.KindUser, "pending question"},
	}
	for _, step := range steps {
		if err := sess.Append(step.kind, step.data); err != nil {
			t.Fatalf("append %s: %v", step.kind, err)
		}
	}
	return res, sess, env
}

// TestReplayDiagnosticsSuffixComposesPrimitives 覆盖组合后缀的全部结构态：
// 完整/空流为空串；未配对调用给出计数、完整位、活跃叶；环额外带显式环提示；
// CJK 叶子 id 始终合法 UTF-8 且有界。
func TestReplayDiagnosticsSuffixComposesPrimitives(t *testing.T) {
	cases := []struct {
		name      string
		records   []session.Record
		wantEmpty bool
		wantParts []string
		wantCycle string
	}{
		{name: "nil 空流", records: nil, wantEmpty: true},
		{
			name: "完整流无后缀",
			records: []session.Record{
				suffixMsg("h", "", session.KindHeader, "根"),
				suffixAssistant("a", "h", "c1"),
				suffixToolResult("t", "a", "c1"),
			},
			wantEmpty: true,
		},
		{
			name: "未配对调用",
			records: []session.Record{
				suffixMsg("h", "", session.KindHeader, "根"),
				suffixAssistant("a", "h", "c1", "c2"),
				suffixMsg("end", "a", session.KindUser, "结束"),
			},
			wantParts: []string{"重放审计", "2 个未完成的工具调用", "完整=false", "叶子=end"},
		},
		{
			name: "parent 环",
			records: []session.Record{
				{ID: "b", Parent: "c", Kind: session.KindAssistant, Data: json.RawMessage(`"b"`)},
				{ID: "c", Parent: "b", Kind: session.KindAssistant, Data: json.RawMessage(`"c"`)},
			},
			wantParts: []string{"0 个未完成的工具调用", "完整=false", "叶子=c"},
			wantCycle: "（检测到 parent 环）",
		},
		{
			name: "CJK 活跃叶",
			records: []session.Record{
				suffixMsg("根", "", session.KindHeader, "根"),
				suffixAssistant("助手", "根", "未回调用"),
				suffixMsg(strings.Repeat("工", 40), "助手", session.KindAssistant, "末尾"),
			},
			wantParts: []string{"重放审计", "1 个未完成的工具调用", "完整=false", strings.Repeat("工", replaySuffixIDRunes) + "…"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := replayDiagnosticsSuffix(tc.records)
			if tc.wantEmpty {
				if got != "" {
					t.Fatalf("replayDiagnosticsSuffix() = %q, want 空串", got)
				}
				return
			}
			for _, want := range tc.wantParts {
				if !strings.Contains(got, want) {
					t.Errorf("后缀 %q 缺少 %q", got, want)
				}
			}
			if tc.wantCycle != "" && !strings.Contains(got, tc.wantCycle) {
				t.Errorf("环后缀 %q 缺少 %q", got, tc.wantCycle)
			}
			if tc.wantCycle == "" && strings.Contains(got, "parent 环") {
				t.Errorf("无环后缀不应带环提示：%q", got)
			}
			if !utf8.ValidString(got) {
				t.Errorf("后缀不是合法 UTF-8：%q", got)
			}
		})
	}
}

// TestReplayDiagnosticsSuffixPure 验证组合后缀确定性且不改入参（含 Data 字节）。
func TestReplayDiagnosticsSuffixPure(t *testing.T) {
	records := []session.Record{
		suffixMsg("h", "", session.KindHeader, "根"),
		suffixAssistant("a", "h", "c1"),
	}
	before := cloneReplayRecords(records)
	first := replayDiagnosticsSuffix(records)
	for i := 0; i < 3; i++ {
		if got := replayDiagnosticsSuffix(records); got != first {
			t.Fatalf("后缀不稳定：首次 %q，第 %d 次 %q", first, i+1, got)
		}
	}
	if !reflect.DeepEqual(records, before) {
		t.Fatalf("组合后缀修改了入参：got %+v, before %+v", records, before)
	}
}

// TestExecuteRecoveryDiagnosticsOptInAddsSuffix 证明 Execute 接线：
// opt-in 开启时，两个用户可见通道都收到"原始公告 + 重放诊断后缀"。
func TestExecuteRecoveryDiagnosticsOptInAddsSuffix(t *testing.T) {
	res, sess, env := interruptedWithOrphan(t)
	baseline := recoveryDecisionFor(recoveryMessagesForClassification(sess.Messages())).Notice
	if baseline == "" {
		t.Fatal("fixture 未产生恢复公告")
	}
	records, err := sess.Records()
	if err != nil {
		t.Fatal(err)
	}
	suffix := replayDiagnosticsSuffix(records)
	if suffix == "" {
		t.Fatal("fixture 未产生重放诊断后缀")
	}
	want := baseline + suffix

	var emitted []agent.Event
	ctx := withEventEmit(context.Background(), func(e agent.Event) { emitted = append(emitted, e) })
	var texts []string
	if _, err := Execute(ctx, cli.IO{Env: withReplayOptIn(env, "1")}, res, sess, "do work", func(s string) { texts = append(texts, s) }); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(texts) == 0 || texts[0] != want {
		t.Fatalf("onText 恢复公告 = %q, want %q", texts, want)
	}
	if !hasNotice(emitted, want) {
		t.Fatalf("EmitEvent 未发布带诊断的恢复公告：%v", emitted)
	}
}

// TestExecuteRecoveryDiagnosticsOptOutBaseline 守护关闭基线：
// =0 关闭时公告逐字节等于原始恢复公告，绝不出现诊断字段。
func TestExecuteRecoveryDiagnosticsOptOutBaseline(t *testing.T) {
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
	if strings.Contains(texts[0], "重放审计") {
		t.Fatalf("关闭时不得出现重放诊断：%q", texts[0])
	}
	if !hasNotice(emitted, baseline) || hasNotice(emitted, baseline+" (重放审计") {
		t.Fatalf("关闭时事件通知应等于基线：%v", emitted)
	}
}

// TestReplayDiagnosticsKeepConfirmationGate 证明诊断只增信息：
// NeedsConfirmation 完全由分类决定；即使公告带上了诊断后缀，
// 降级历史仍返回同样的确认错误，可自动继续的历史仍不返回错误。
func TestReplayDiagnosticsKeepConfirmationGate(t *testing.T) {
	degraded := recoveryDecisionFor(recoveryMessages("degraded_history"))
	if !degraded.NeedsConfirmation || degraded.Notice == "" {
		t.Fatalf("降级 fixture = %+v, want 确认门", degraded)
	}
	enrichedDegraded := degraded
	enrichedDegraded.Notice += replayDiagnosticsSuffix([]session.Record{
		suffixMsg("h", "", session.KindHeader, "根"),
		suffixAssistant("a", "h", "c1"),
	})
	if enrichedDegraded.NeedsConfirmation != degraded.NeedsConfirmation {
		t.Fatal("诊断改变了 NeedsConfirmation")
	}

	interrupted := recoveryDecisionFor(recoveryMessages("interrupted_prompt"))
	if interrupted.NeedsConfirmation {
		t.Fatalf("中断 fixture 不应要求确认：%+v", interrupted)
	}
	enrichedInterrupted := interrupted
	enrichedInterrupted.Notice += replayDiagnosticsSuffix([]session.Record{
		suffixMsg("h", "", session.KindHeader, "根"),
		suffixAssistant("a", "h", "c2"),
	})

	for _, tc := range []struct {
		name      string
		decision  recoveryDecision
		wantError bool
	}{
		{name: "降级基线", decision: degraded, wantError: true},
		{name: "降级带诊断", decision: enrichedDegraded, wantError: true},
		{name: "中断基线", decision: interrupted, wantError: false},
		{name: "中断带诊断", decision: enrichedInterrupted, wantError: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var errBuf bytes.Buffer
			var emitted []agent.Event
			ctx := withEventEmit(context.Background(), func(e agent.Event) { emitted = append(emitted, e) })
			err := announceRecovery(ctx, cli.IO{Err: &errBuf}, nil, tc.decision)
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "degraded") {
					t.Fatalf("降级路径必须仍返回确认错误，got %v", err)
				}
				if !strings.Contains(errBuf.String(), "Session recovery paused") {
					t.Fatalf("暂停警告缺失：%q", errBuf.String())
				}
				return
			}
			if err != nil {
				t.Fatalf("可自动继续的路径不应报错：%v", err)
			}
			if !hasNotice(emitted, tc.decision.Notice) {
				t.Fatalf("恢复公告未发布：%v", emitted)
			}
		})
	}
}
