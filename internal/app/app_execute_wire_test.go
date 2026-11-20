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

// budgetProbeChatter always asks for one tool call and reports a fixed usage.
// The tool call keeps the loop alive long enough for the declared-budget breach
// check (which runs right after the reply, before tool execution) to fire.
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

// executeWithBudgetProbe runs one prompt with a probing chatter. thinking is
// set after Resolve so its text reaches only the injected dynamic-context
// prefix, never the caller's raw prompt.
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

// TestExecuteFillsDeclaredBudgetFromRawPrompt proves the wiring: a declaration
// in the caller's own words reaches Budget.DeclaredTurnTokens and stops the
// run with the declared-turn-budget reason once usage exceeds it.
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

// TestExecuteIgnoresDeclaredBudgetFromInjectedPrefix proves the raw-prompt
// capture: a "+500k" that arrives only in the injected dynamic-context prefix
// must not become a hidden cap, even though it sits in the text sent to the
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
		if e.Kind == agent.EventNotice && e.Text == text {
			return true
		}
	}
	return false
}

func containsText(texts []string, want string) bool {
	for _, got := range texts {
		if got == want {
			return true
		}
	}
	return false
}

// TestExecuteAnnouncesRecoveryNotice proves the Execute-level wiring: an
// interrupted transcript announces its notice through both existing channels,
// and the run still continues (interrupted history is auto-continuable).
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

// TestAnnounceRecoveryPausesDegradedHistory proves the degraded path: because
// Execute has no confirmation channel, it writes an explicit warning, emits it,
// and returns an error so the agent loop is never entered.
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

// TestRecoveryMessagesForClassificationIgnoresMetadataParentGaps guards the
// false-positive that a resumed session would otherwise hit: metadata records
// (usage/stats/turn) live between messages but are filtered out of
// Session.Messages, so a message's parent can name a record absent from the
// projection. That gap is not degraded history and must not gate the run.
func TestRecoveryMessagesForClassificationIgnoresMetadataParentGaps(t *testing.T) {
	messages := []session.Entry{
		{ID: "n1", Role: "user", Content: "question"},
		{ID: "n4", Parent: "n2", Role: "assistant", Content: "answer"}, // n2 is a filtered metadata record
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

// TestAnnounceRecoveryCleanIsNoOp guards the common path: a clean transcript
// produces no notice, no event and no error.
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
