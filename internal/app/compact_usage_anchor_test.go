package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func TestEstimateContextTokensWithoutAnchorUsesFullEstimate(t *testing.T) {
	messages := []llm.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "hello world"},
		{Role: "assistant", Content: "hi there"},
	}
	want := agent.EstimateMessages(messages)
	if got := estimateContextTokens(messages, usageAnchor{}); got != want {
		t.Fatalf("no anchor = %d, want full estimate %d", got, want)
	}
}

func TestEstimateContextTokensAnchorsPrefixAndEstimatesTail(t *testing.T) {
	messages := []llm.Message{
		{Role: "user", Content: strings.Repeat("a", 4000)},
		{Role: "assistant", Content: "ok", Usage: &llm.Usage{PromptTokens: 400, CompletionTokens: 20}},
		{Role: "tool", Content: strings.Repeat("b", 200), ToolCallID: "c1"},
	}
	anchor := usageAnchor{index: 1, tokens: 420}
	want := 420 + agent.EstimateMessages(messages[2:])
	if got := estimateContextTokens(messages, anchor); got != want {
		t.Fatalf("anchored = %d, want %d", got, want)
	}
	// 原生 totalTokens（420）远低于长前缀按字符数/4 的估算值，
	// 因此锚定值必须远小于完整
	// 重新估算——否则说明锚点被忽略了。
	if full := agent.EstimateMessages(messages); want >= full {
		t.Fatalf("anchored %d must be below full estimate %d", want, full)
	}
}

func TestEstimateContextTokensInvalidatesAnchorAfterCompaction(t *testing.T) {
	messages := []llm.Message{
		{Role: "assistant", Content: "before", Usage: &llm.Usage{PromptTokens: 10, CompletionTokens: 5}},
		{Role: "system", Content: "## Compaction\n- dropped 3 message(s), kept the last 2\n"},
		{Role: "user", Content: strings.Repeat("z", 120)},
	}
	anchor := usageAnchor{index: 0, tokens: 15}
	want := agent.EstimateMessages(messages)
	if got := estimateContextTokens(messages, anchor); got != want {
		t.Fatalf("after compaction = %d, want full estimate %d", got, want)
	}
}

func TestEstimateContextTokensKeepsAnchorMeasuredAfterCompaction(t *testing.T) {
	messages := []llm.Message{
		{Role: "system", Content: "## Compaction\n- dropped 9 message(s), kept the last 2\n"},
		{Role: "assistant", Content: "after", Usage: &llm.Usage{PromptTokens: 200, CompletionTokens: 40}},
		{Role: "tool", Content: "result"},
	}
	anchor := usageAnchor{index: 1, tokens: 240}
	want := 240 + agent.EstimateMessages(messages[2:])
	if got := estimateContextTokens(messages, anchor); got != want {
		t.Fatalf("post-compaction anchor = %d, want %d", got, want)
	}
}

func TestEstimateContextTokensUsesWideRuneRuleForTail(t *testing.T) {
	anchor := usageAnchor{index: 0, tokens: 110}
	head := llm.Message{Role: "assistant", Content: "x", Usage: &llm.Usage{PromptTokens: 100, CompletionTokens: 10}}

	cjk := []llm.Message{head, {Role: "tool", Content: strings.Repeat("加", 8)}}
	narrow := []llm.Message{head, {Role: "tool", Content: strings.Repeat("a", 8)}}

	cjkGot := estimateContextTokens(cjk, anchor)
	narrowGot := estimateContextTokens(narrow, anchor)
	if cjkGot <= narrowGot {
		t.Fatalf("CJK tail %d must exceed narrow tail %d", cjkGot, narrowGot)
	}
	if want := 110 + agent.EstimateMessages(cjk[1:]); cjkGot != want {
		t.Fatalf("CJK tail = %d, want %d", cjkGot, want)
	}
}

func TestEstimateContextTokensIgnoresInvalidAnchor(t *testing.T) {
	messages := []llm.Message{{Role: "user", Content: "hello"}}
	full := agent.EstimateMessages(messages)
	cases := []struct {
		name   string
		anchor usageAnchor
	}{
		{"zero value", usageAnchor{}},
		{"non-positive tokens", usageAnchor{index: 0, tokens: 0}},
		{"negative tokens", usageAnchor{index: 0, tokens: -5}},
		{"negative index", usageAnchor{index: -1, tokens: 10}},
		{"out of range index", usageAnchor{index: 5, tokens: 10}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := estimateContextTokens(messages, tc.anchor); got != full {
				t.Fatalf("estimate = %d, want full estimate %d", got, full)
			}
		})
	}
}

func TestAssistantNativeUsageFromRecordPayload(t *testing.T) {
	reply := llm.Message{
		Role:    "assistant",
		Content: "ok",
		Usage:   &llm.Usage{PromptTokens: 500, CompletionTokens: 75, CachedTokens: 20},
	}
	data, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	usage, ok := assistantNativeUsage(data)
	if !ok {
		t.Fatal("native usage not read from assistant payload")
	}
	if usage.TotalTokens() != 575 {
		t.Fatalf("totalTokens = %d, want 575", usage.TotalTokens())
	}

	// 较旧的记录和仅含元数据的负载不携带用量，因此不得
	// 产生锚点。
	for _, raw := range []string{`{"role":"assistant","content":"older"}`, `{}`, `not json`} {
		if _, ok := assistantNativeUsage([]byte(raw)); ok {
			t.Fatalf("payload %q unexpectedly yielded a usage anchor", raw)
		}
	}
	if _, ok := assistantNativeUsage(nil); ok {
		t.Fatal("empty payload unexpectedly yielded a usage anchor")
	}
}

func TestMaybeAutoCompactUsesNativeUsageAnchor(t *testing.T) {
	env, _ := testEnv(t)
	root := t.TempDir()
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	// 一个略低于膨胀后原生 totalTokens（1000 * 1.3）的阈值。
	// 这段短记录按字符数/4 的普通估算远低于它，因此
	// 压缩只能来自用量锚点。
	res.AutoCompactTokens = 1200
	res.Cfg.ContextWindow = 0
	res.Cfg.AutoCondenseContextPercent = 0
	store := testStore(t, res)
	sess, err := store.NewSession("anchor-wiring")
	if err != nil {
		t.Fatal(err)
	}

	reply := llm.Message{
		Role:    "assistant",
		Content: "ok",
		Usage:   &llm.Usage{PromptTokens: 900, CompletionTokens: 100},
	}
	for i := 0; i < 10; i++ {
		if i == 4 {
			if err := sess.Append("assistant", reply); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := sess.Append("user", map[string]any{"content": "hi"}); err != nil {
			t.Fatal(err)
		}
	}

	entries := sess.Messages()
	plain := make([]llm.Message, 0, len(entries))
	for _, entry := range entries {
		plain = append(plain, llm.Message{Role: entry.Role, Content: entry.Content})
	}
	window, configured, percent := compactionInputs(res)
	if _, compact := compactionDecision(agent.EstimateMessages(plain), window, configured, percent); compact {
		t.Fatal("test setup: the plain estimate should be below the threshold")
	}

	maybeAutoCompact(res, sess, agent.CompactionEvidence{}, nil, nil)
	if !hasCompaction(sess) {
		t.Fatal("native usage anchor should trip the compaction trigger")
	}
}
