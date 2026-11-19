package app

import (
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/llm"
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
		{Role: "assistant", Content: "后续: add the lexer"}, // only CollectCompactionEvidence sees this prefix
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
	// The kept tail starts with the assistant that precedes the newest result,
	// never with a bare tool message.
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

// agent-facing interface check: the app compactor satisfies agent.Compactor.
var _ agent.Compactor = (*sessionCompactor)(nil)
