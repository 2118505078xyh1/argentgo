package app

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// budgetProbeChatter 总是请求一次工具调用，并报告固定的用量。
// 该工具调用让循环保持足够长的存活时间，使声明预算的越界
// 检查（在回复之后、工具执行之前运行）得以触发。
type budgetProbeChatter struct {
	mu      sync.Mutex
	calls   int
	usage   llm.Usage
	prompts []string
}

func (c *budgetProbeChatter) Chat(_ context.Context, _ string, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	for _, m := range messages {
		if m.Role == "user" {
			c.prompts = append(c.prompts, m.Content)
		}
	}
	usage := c.usage
	return llm.Message{
		Role: "assistant",
		ToolCalls: []llm.ToolCall{{
			ID:       "probe-call",
			Type:     "function",
			Function: llm.ToolCallFunction{Name: "read", Arguments: `{}`},
		}},
		Usage: &usage,
	}, nil
}

// executeWithBudgetProbe 用探测型 chatter 运行一个提示。thinking 在
// Resolve 之后设置，使其文本只到达注入的动态上下文
// 前缀，绝不进入调用方的原始提示。
func executeWithBudgetProbe(t *testing.T, thinking, prompt string, usage llm.Usage) (agent.Result, error, *budgetProbeChatter) {
	t.Helper()
	env, root, _ := wiringEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	res.TrustProject = true
	res.Thinking = thinking
	res.MaxTurns = 1
	res.Approve = autoApprover{}
	chatter := &budgetProbeChatter{usage: usage}
	res.Chatter = chatter
	store := testStore(t, res)
	sess, err := store.NewSession("budget-probe")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Execute(context.Background(), cli.IO{Env: env}, res, sess, prompt, nil)
	return result, err, chatter
}

// TestExecuteFillsDeclaredBudgetFromRawPrompt 证明接线生效：调用方原文中的声明
// 会到达 Budget.DeclaredTurnTokens，并在用量超过它时以
// declared-turn-budget 原因停止运行。
func TestExecuteFillsDeclaredBudgetFromRawPrompt(t *testing.T) {
	result, err, chatter := executeWithBudgetProbe(t, "", "use 2m tokens please do the thing", llm.Usage{PromptTokens: 2_100_000})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Stopped != agent.StopDeclaredTurnBudget {
		t.Fatalf("Stopped = %q, want %q (declared budget was not applied)", result.Stopped, agent.StopDeclaredTurnBudget)
	}
	if chatter.calls == 0 {
		t.Fatal("chatter was never called")
	}
}

// TestExecuteIgnoresDeclaredBudgetFromInjectedPrefix 证明原始提示
// 捕获：只出现在注入的动态上下文前缀中的 "+500k"
// 绝不能成为隐藏上限，即使它位于发送给
// model.
func TestExecuteIgnoresDeclaredBudgetFromInjectedPrefix(t *testing.T) {
	result, err, chatter := executeWithBudgetProbe(t, "low +500k", "do the thing", llm.Usage{PromptTokens: 600_000})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(chatter.prompts) == 0 || !strings.Contains(chatter.prompts[0], "+500k") {
		t.Fatalf("injected prefix did not reach the model: %v", chatter.prompts)
	}
	if result.Stopped == agent.StopDeclaredTurnBudget {
		t.Fatalf("injected prefix was parsed as a declaration: Stopped = %q", result.Stopped)
	}
}

func hasNotice(events []agent.Event, text string) bool {
	for _, e := range events {
		// 恢复公告可能带默认开启的 (恢复细节：...) 后缀，用子串匹配锁定核心语义。
		if e.Kind == agent.EventNotice && strings.Contains(e.Text, text) {
			return true
		}
	}
	return false
}

func containsText(texts []string, want string) bool {
	for _, got := range texts {
		// 恢复公告可能带默认开启的 (恢复细节：...) 后缀，用子串匹配锁定核心语义。
		if strings.Contains(got, want) {
			return true
		}
	}
	return false
}

// TestExecuteAnnouncesRecoveryNotice 证明 Execute 层的接线：被中断的记录
// 会通过两个既有通道宣告其通知，
// 且运行仍会继续（被中断的历史可自动继续）。
func TestExecuteAnnouncesRecoveryNotice(t *testing.T) {
	env, root, _ := wiringEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	res.Approve = autoApprover{}
	res.Chatter = &scriptedChatter{replies: []string{"done"}}
	store := testStore(t, res)
	sess, err := store.NewSession("recovery-notice")
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range []struct{ kind, text string }{
		{session.KindUser, "question"},
		{session.KindAssistant, "answer"},
		{session.KindUser, "pending question"},
	} {
		if err := sess.Append(rec.kind, rec.text); err != nil {
			t.Fatalf("append %s: %v", rec.kind, err)
		}
	}

	var emitted []agent.Event
	ctx := withEventEmit(context.Background(), func(e agent.Event) { emitted = append(emitted, e) })
	var texts []string
	if _, err := Execute(ctx, cli.IO{Env: env}, res, sess, "do work", func(s string) { texts = append(texts, s) }); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !containsText(texts, noticeInterruptedPrompt) {
		t.Fatalf("onText did not receive the recovery notice: %v", texts)
	}
	if !hasNotice(emitted, noticeInterruptedPrompt) {
		t.Fatalf("EmitEvent did not publish the recovery notice: %v", emitted)
	}
}

// TestAnnounceRecoveryPausesDegradedHistory 证明降级路径：因为
// Execute 没有确认通道，它会写一条明确的警告、发出它，
// 并返回错误，使 agent 循环永远不会被进入。
func TestAnnounceRecoveryPausesDegradedHistory(t *testing.T) {
	decision := recoveryDecisionFor(recoveryMessages("degraded_history"))
	if !decision.NeedsConfirmation || decision.Notice == "" {
		t.Fatalf("degraded fixture = %+v, want a notice and confirmation gate", decision)
	}

	var errBuf bytes.Buffer
	var emitted []agent.Event
	ctx := withEventEmit(context.Background(), func(e agent.Event) { emitted = append(emitted, e) })
	var texts []string
	err := announceRecovery(ctx, cli.IO{Err: &errBuf}, func(s string) { texts = append(texts, s) }, decision)
	if err == nil || !strings.Contains(err.Error(), "degraded") {
		t.Fatalf("degraded history must pause with an explicit error, got %v", err)
	}
	if !strings.Contains(errBuf.String(), "Session recovery paused") {
		t.Fatalf("stderr warning missing: %q", errBuf.String())
	}
	if !containsText(texts, noticeDegradedHistory) {
		t.Fatalf("onText did not receive the degraded notice: %v", texts)
	}
	if !hasNotice(emitted, noticeDegradedHistory) {
		t.Fatalf("EmitEvent did not publish the degraded notice: %v", emitted)
	}
	if len(texts) < 2 || !strings.Contains(texts[len(texts)-1], "Session recovery paused") {
		t.Fatalf("onText did not receive the pause warning: %v", texts)
	}
}

// TestRecoveryMessagesForClassificationIgnoresMetadataParentGaps 防范恢复会话
// 否则会命中的误报：元数据记录
// （usage/stats/turn）存在于消息之间，但被过滤出
// Session.Messages，因此消息的 parent 可能指向投影中不存在的记录。
// 该缺口不是降级历史，不得阻止运行。
func TestRecoveryMessagesForClassificationIgnoresMetadataParentGaps(t *testing.T) {
	messages := []session.Entry{
		{ID: "n1", Role: "user", Content: "question"},
		{ID: "n4", Parent: "n2", Role: "assistant", Content: "answer"}, // n2 是一条被过滤掉的元数据记录
	}
	if got := recoveryDecisionFor(messages); !got.NeedsConfirmation {
		t.Fatalf("fixture must look degraded before normalization: %+v", got)
	}
	normalized := recoveryMessagesForClassification(messages)
	if got := recoveryDecisionFor(normalized); got.Notice != "" || got.NeedsConfirmation {
		t.Fatalf("metadata parent gap must not degrade a clean transcript: %+v", got)
	}
	if messages[1].Parent != "n2" {
		t.Fatalf("input was mutated: parent = %q, want n2", messages[1].Parent)
	}
}

// TestAnnounceRecoveryCleanIsNoOp 守护常见路径：干净的记录
// 不产生通知、事件或错误。
func TestAnnounceRecoveryCleanIsNoOp(t *testing.T) {
	var emitted []agent.Event
	ctx := withEventEmit(context.Background(), func(e agent.Event) { emitted = append(emitted, e) })
	called := false
	if err := announceRecovery(ctx, cli.IO{}, func(string) { called = true }, recoveryDecisionFor(recoveryMessages("clean"))); err != nil {
		t.Fatalf("clean recovery returned an error: %v", err)
	}
	if called || len(emitted) != 0 {
		t.Fatalf("clean recovery announced something: onText=%v events=%v", called, emitted)
	}
}
