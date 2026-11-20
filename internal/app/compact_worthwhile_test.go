package app

import (
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func TestCompactionWorthwhile(t *testing.T) {
	// 门控为 (before-after)/before >= 10%。整数运算对所需节省量
	// 向上取整，因此奇数的 `before` 需要其十分之一的上取整。
	cases := []struct {
		name          string
		before, after int64
		want          bool
	}{
		{"large savings", 1000, 600, true},           // 40%
		{"exactly ten percent", 1000, 900, true},     // 边界：保留
		{"just below ten percent", 1000, 901, false}, // 9.9%: reject
		{"small savings", 1000, 950, false},          // 5%
		{"no savings", 1000, 1000, false},            // 0%
		{"context grows", 1000, 1100, false},         // 负节省
		{"everything removed", 100, 0, true},         // 100%
		{"even before boundary", 10, 9, true},        // 1/10 exactly
		{"odd before below", 11, 10, false},          // 1/11 = 9.09%
		{"odd before above", 5, 4, true},             // 1/5 = 20%
		{"before zero", 0, 0, false},                 // 无比例
		{"before negative", -100, -200, false},       // 无比例
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
	// CJK rune 每个按一个 token 计费，因此相同的字符数会产生比拉丁文本
	// 严格更大的估算值。验证估算器仍能
	// 区分它们，且节省门控对两种计数都有效。
	anchor := usageAnchor{index: 0, tokens: 110}
	head := llm.Message{Role: "assistant", Content: "x", Usage: &llm.Usage{PromptTokens: 100, CompletionTokens: 10}}
	cjk := []llm.Message{head, {Role: "tool", Content: strings.Repeat("加", 8)}}
	narrow := []llm.Message{head, {Role: "tool", Content: strings.Repeat("a", 8)}}

	cjkBefore := estimateContextTokens(cjk, anchor)
	narrowBefore := estimateContextTokens(narrow, anchor)
	if cjkBefore <= narrowBefore {
		t.Fatalf("CJK tail %d must estimate above narrow tail %d", cjkBefore, narrowBefore)
	}

	// 在两种估算器下，10% 的缩减都是值得的；门控取决于
	// 比例，而非内容的文字体系。
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

	// 宽窗口几乎把整段对话都原样保留。把
	// 单条短消息折叠进摘要所花费的 token 比节省的还多，因此
	// 事后缩减检查必须拒绝这次压缩。
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

	// 庞大的工具主体被摘要掉，因此缩减比例远高于
	// 下限，压缩得以进行。
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
