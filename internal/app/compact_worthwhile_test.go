package app

import (
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func TestCompactionWorthwhile(t *testing.T) {
	// The gate is (before-after)/before >= 10%. Integer math rounds the
	// required saving up, so an odd `before` needs the ceiling of its tenth.
	cases := []struct {
		name          string
		before, after int64
		want          bool
	}{
		{"large savings", 1000, 600, true},           // 40%
		{"exactly ten percent", 1000, 900, true},     // boundary: keep
		{"just below ten percent", 1000, 901, false}, // 9.9%: reject
		{"small savings", 1000, 950, false},          // 5%
		{"no savings", 1000, 1000, false},            // 0%
		{"context grows", 1000, 1100, false},         // negative saving
		{"everything removed", 100, 0, true},         // 100%
		{"even before boundary", 10, 9, true},        // 1/10 exactly
		{"odd before below", 11, 10, false},          // 1/11 = 9.09%
		{"odd before above", 5, 4, true},             // 1/5 = 20%
		{"before zero", 0, 0, false},                 // no ratio
		{"before negative", -100, -200, false},       // no ratio
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := compactionWorthwhile(tc.before, tc.after); got != tc.want {
				t.Fatalf("compactionWorthwhile(%d, %d) = %v, want %v", tc.before, tc.after, got, tc.want)
			}
		})
	}
}

func TestCompactionWorthwhileCJKEstimateNoRegression(t *testing.T) {
	// CJK runes are charged one token each, so the same char count produces a
	// strictly larger estimate than Latin text. Verify the estimator still
	// distinguishes them and that the savings gate works on either count.
	anchor := usageAnchor{index: 0, tokens: 110}
	head := llm.Message{Role: "assistant", Content: "x", Usage: &llm.Usage{PromptTokens: 100, CompletionTokens: 10}}
	cjk := []llm.Message{head, {Role: "tool", Content: strings.Repeat("加", 8)}}
	narrow := []llm.Message{head, {Role: "tool", Content: strings.Repeat("a", 8)}}

	cjkBefore := estimateContextTokens(cjk, anchor)
	narrowBefore := estimateContextTokens(narrow, anchor)
	if cjkBefore <= narrowBefore {
		t.Fatalf("CJK tail %d must estimate above narrow tail %d", cjkBefore, narrowBefore)
	}

	// A 10% reduction is worthwhile under both estimators; the gate depends on
	// the ratio, not the script of the content.
	cjkAfter := cjkBefore * 9 / 10
	narrowAfter := narrowBefore * 9 / 10
	if !compactionWorthwhile(int64(cjkBefore), int64(cjkAfter)) {
		t.Fatalf("CJK %d -> %d must be worthwhile", cjkBefore, cjkAfter)
	}
	if !compactionWorthwhile(int64(narrowBefore), int64(narrowAfter)) {
		t.Fatalf("narrow %d -> %d must be worthwhile", narrowBefore, narrowAfter)
	}
}

func TestMaybeCompactSkipsWhenSavingsBelowMinimum(t *testing.T) {
	env, _ := testEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	store := testStore(t, res)
	sess, err := store.NewSession("low-savings")
	if err != nil {
		t.Fatal(err)
	}

	// A wide window leaves almost the whole conversation verbatim. Folding a
	// single short message into a summary costs more tokens than it saves, so
	// the post-hoc reduction check must reject the compaction.
	messages := []llm.Message{{Role: "system", Content: "rules"}}
	for i := 0; i < 10; i++ {
		messages = append(messages, llm.Message{Role: "user", Content: "hello world"})
	}
	for _, m := range messages {
		if err := sess.Append(m.Role, map[string]any{"content": m.Content}); err != nil {
			t.Fatal(err)
		}
	}

	out, changed := newSessionCompactor(res, sess).MaybeCompact(messages, 65)
	if changed {
		t.Fatal("compaction with sub-10% estimated savings must be skipped")
	}
	if len(out) != len(messages) {
		t.Fatalf("rejected compaction returned %d messages, want original %d", len(out), len(messages))
	}
	if hasCompaction(sess) {
		t.Fatal("a rejected compaction must not append a record")
	}
}

func TestMaybeCompactStillCompactsWhenSavingsAreLarge(t *testing.T) {
	env, _ := testEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	store := testStore(t, res)
	sess, err := store.NewSession("high-savings")
	if err != nil {
		t.Fatal(err)
	}

	// Big tool bodies are summarised away, so the reduction ratio is far above
	// the floor and compaction proceeds.
	messages := []llm.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "goal"},
		{Role: "assistant", Content: "step one"},
		{Role: "tool", ToolCallID: "c1", Content: strings.Repeat("x", 400)},
		{Role: "assistant", Content: "step two"},
		{Role: "tool", ToolCallID: "c2", Content: strings.Repeat("y", 400)},
	}
	for _, m := range messages {
		if err := sess.Append(m.Role, map[string]any{"content": m.Content, "tool_call_id": m.ToolCallID}); err != nil {
			t.Fatal(err)
		}
	}

	out, changed := newSessionCompactor(res, sess).MaybeCompact(messages, 150)
	if !changed {
		t.Fatal("compaction with large savings must proceed")
	}
	if !compactionWorthwhile(int64(agent.EstimateMessages(messages)), int64(agent.EstimateMessages(out))) {
		t.Fatal("compacted view must clear the savings floor")
	}
	if !hasCompaction(sess) {
		t.Fatal("an accepted compaction must append a record")
	}
}
