package app

import (
	"strings"
	"testing"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/llm"
)

func TestSessionCompactorThresholdResolution(t *testing.T) {
	if got := newSessionCompactor(Resolved{}, nil).CompactThreshold(); got != defaultAutoCompactTokens {
		t.Fatalf("default threshold = %d, want %d", got, defaultAutoCompactTokens)
	}
	res := Resolved{AutoCompactTokens: 5000}
	if got := newSessionCompactor(res, nil).CompactThreshold(); got != 5000 {
		t.Fatalf("configured threshold = %d, want 5000", got)
	}
	res.AutoCompactTokens = -1
	if got := newSessionCompactor(res, nil).CompactThreshold(); got != 0 {
		t.Fatalf("disabled threshold = %d, want 0", got)
	}
}

func TestSessionCompactorMaybeCompactUsesBudgetAndEvidence(t *testing.T) {
	env, _ := testEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	store := testStore(t, res)
	sess, err := store.NewSession("mid-compact")
	if err != nil {
		t.Fatal(err)
	}

	messages := []llm.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "goal"},
		{Role: "assistant", Content: "后续: add the lexer"}, // 只有 CollectCompactionEvidence 能看到这个前缀
		{Role: "tool", ToolCallID: "c1", Content: strings.Repeat("x", 400)},
		{Role: "assistant", Content: "continue"},
		{Role: "tool", ToolCallID: "c2", Content: strings.Repeat("y", 400)},
	}
	for _, m := range messages {
		if err := sess.Append(m.Role, map[string]any{"content": m.Content, "tool_call_id": m.ToolCallID}); err != nil {
			t.Fatal(err)
		}
	}

	compactor := newSessionCompactor(res, sess)
	out, changed := compactor.MaybeCompact(messages, 150)
	if !changed {
		t.Fatal("expected a compaction for a small window")
	}
	if len(out) >= len(messages) {
		t.Fatalf("compacted messages = %d, want fewer than %d", len(out), len(messages))
	}
	if out[0].Role != "system" || out[0].Content == "" {
		t.Fatal("first message must be the generated summary")
	}
	if out[1].Role != "system" || out[1].Content != "rules" {
		t.Fatal("original system prompt must survive compaction")
	}
	// 保留的尾部以最新结果之前的助手消息开头，
	// 绝不会以一条裸工具消息开头。
	if out[2].Role != "assistant" || out[3].Role != "tool" {
		t.Fatalf("cut split a tool pair: %+v", out[2:])
	}
	if !hasCompaction(sess) {
		t.Fatal("compaction record was not appended to the session")
	}
	records, _ := sess.Records()
	var summary string
	for _, record := range records {
		if record.Kind == "compaction" {
			summary = string(record.Data)
		}
	}
	if !strings.Contains(summary, "add the lexer") {
		t.Fatalf("next-step line was not carried into the summary:\n%s", summary)
	}
}

func TestSessionCompactorNoopOnSmallHistory(t *testing.T) {
	env, _ := testEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	store := testStore(t, res)
	sess, err := store.NewSession("no-compact")
	if err != nil {
		t.Fatal(err)
	}
	messages := []llm.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
		{Role: "user", Content: "thanks"},
	}
	out, changed := newSessionCompactor(res, sess).MaybeCompact(messages, 100000)
	if changed || len(out) != len(messages) || hasCompaction(sess) {
		t.Fatal("a small history must not be compacted")
	}
}

// 面向 agent 的接口检查：app 的压缩器满足 agent.Compactor。
var _ agent.Compactor = (*sessionCompactor)(nil)
