package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/llm"
	"example.com/argentgo/internal/session"
)

// 本文件锁定 #1/#2/#4/#5/#7/#13 的默认行为：全部默认开启，测试用脚本化 provider 与
// 纯函数断言，不联网、不用真实模型、不读进程环境（env 仅以显式函数注入）。

// ---------------------------------------------------------------------------
// #1 事后缩减守卫
// ---------------------------------------------------------------------------

func TestPostModelSummaryShrinkGuard(t *testing.T) {
	t.Run("默认开启且仅 =0 回退", func(t *testing.T) {
		if !postShrinkGuardEnabled(nil) {
			t.Fatal("守卫应默认开启")
		}
		envOf := func(k string) string {
			if k == "ARGENT_COMPACTION_POST_SHRINK_GUARD" {
				return "0"
			}
			return ""
		}
		if postShrinkGuardEnabled(envOf) {
			t.Fatal("=0 应关闭守卫")
		}
	})

	t.Run("strip 只删模型叙事段并保留权威字段", func(t *testing.T) {
		authoritative := "## Compaction\n- dropped 2\n\n## Files changed\n- a.go\n"
		withNarrative := ApplyModelSummary(authoritative, "模型补充了很多细节", []string{"下一步"})
		stripped := StripModelNarrative(withNarrative)
		if stripped != authoritative {
			t.Fatalf("剥掉叙事段后 = %q, want %q", stripped, authoritative)
		}
		if got := StripModelNarrative(authoritative); got != authoritative {
			t.Fatalf("无标记时应原样返回, got %q", got)
		}
	})

	t.Run("增补后变大必须回落", func(t *testing.T) {
		base := "## Compaction\n- dropped 2\n"
		huge := ApplyModelSummary(base, strings.Repeat("长叙事", 200), nil)
		tests := []struct {
			name        string
			before      int
			after       int
			afterStrip  int
			summary     string
			wantSummary string
			wantProceed bool
		}{
			{"变小则原样放行", 100, 80, 80, huge, huge, true},
			{"变大但剥叙事后变小则回落权威", 100, 200, 70, huge, base, true},
			{"剥完仍变大则放弃", 100, 200, 150, huge, huge, false},
			{"无叙事且变大则放弃", 100, 200, 200, base, base, false},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				got, proceed := postShrinkDecision(tc.before, tc.after, tc.afterStrip, tc.summary)
				if proceed != tc.wantProceed || got != tc.wantSummary {
					t.Fatalf("decision = (%q, %v), want (%q, %v)", got, proceed, tc.wantSummary, tc.wantProceed)
				}
			})
		}
	})
}

// ---------------------------------------------------------------------------
// #2 前缀复用
// ---------------------------------------------------------------------------

func TestCompactionPrefixReuse(t *testing.T) {
	system := llm.Message{Role: "system", Content: "you are a careful coding agent"}
	folded := []llm.Message{
		{Role: "user", Content: "原始用户问题"},
		{Role: "assistant", Content: "原始答复"},
	}
	tools := []llm.ToolSpec{}
	spec := llm.ToolSpec{}
	spec.Function.Name = "read"
	spec.Function.Description = "read a file"
	tools = append(tools, spec)

	in := ModelSummaryInput{Extractive: "## Compaction\n- x\n", MaxChars: 8000}
	msgs, gotTools, ok := BuildSummaryMessagesWithPrefix(in, ModelSummaryPrefix{
		SystemMessages: []llm.Message{system}, FoldedMessages: folded, Tools: tools,
	})
	if !ok {
		t.Fatal("应成功构造复用前缀")
	}
	if len(msgs) != len(folded)+2 {
		t.Fatalf("消息数 = %d, want %d", len(msgs), len(folded)+2)
	}
	// system 与历史消息逐字节复用为稳定前缀。
	if !reflect.DeepEqual(msgs[0], system) || !reflect.DeepEqual(msgs[1], folded[0]) || !reflect.DeepEqual(msgs[2], folded[1]) {
		t.Fatalf("请求前缀被改写: %+v", msgs[:3])
	}
	last := msgs[len(msgs)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "Return only the JSON object") {
		t.Fatalf("压缩指令应作为末条 user: %+v", last)
	}
	if len(gotTools) != 1 || gotTools[0].Function.Name != "read" {
		t.Fatalf("工具表应原样回放: %+v", gotTools)
	}

	t.Run("无 system 或无折叠区则回落", func(t *testing.T) {
		if _, _, ok := BuildSummaryMessagesWithPrefix(in, ModelSummaryPrefix{FoldedMessages: folded}); ok {
			t.Fatal("无 system 应失败")
		}
		if _, _, ok := BuildSummaryMessagesWithPrefix(in, ModelSummaryPrefix{SystemMessages: []llm.Message{system}}); ok {
			t.Fatal("无折叠区应失败")
		}
	})

	t.Run("200 条 / 32 KiB 上限", func(t *testing.T) {
		many := make([]llm.Message, 300)
		for i := range many {
			many[i] = llm.Message{Role: "assistant", Content: strings.Repeat("x", 100)}
		}
		got, _, ok := BuildSummaryMessagesWithPrefix(in, ModelSummaryPrefix{
			SystemMessages: []llm.Message{system}, FoldedMessages: many,
		})
		if !ok {
			t.Fatal("应成功")
		}
		if len(got) > modelSummaryPrefixMaxMessages {
			t.Fatalf("消息数 %d 超过 %d", len(got), modelSummaryPrefixMaxMessages)
		}
		runes := 0
		for _, m := range got {
			runes += len([]rune(m.Content))
		}
		if runes > modelSummaryPrefixMaxBytes {
			t.Fatalf("前缀 rune 数 %d 超过 %d", runes, modelSummaryPrefixMaxBytes)
		}
	})

	t.Run("=0 回退只发生在调用点", func(t *testing.T) {
		if !compactionPrefixReuseEnabled(nil) {
			t.Fatal("前缀复用应默认开启")
		}
		env := func(k string) string {
			if k == "ARGENT_COMPACTION_PREFIX_REUSE" {
				return "0"
			}
			return ""
		}
		if compactionPrefixReuseEnabled(env) {
			t.Fatal("=0 应关闭")
		}
	})
}

// ---------------------------------------------------------------------------
// #4 agentic 折叠
// ---------------------------------------------------------------------------

func TestAgenticFoldOnlyNewSinceSummary(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "old goal"},
		{Role: "assistant", Content: "old answer"},
		{Role: "system", Content: "## Compaction\n- dropped 3\n"}, // 最新摘要边界
		{Role: "assistant", Content: "new answer one"},
		{Role: "user", Content: "new question"},
		{Role: "assistant", Content: "new answer two"},
	}

	lo, hi, ok := AgenticSummaryWindow(msgs, len(msgs), 32000)
	if !ok {
		t.Fatal("应能构造窗口")
	}
	if lo <= 3 {
		t.Fatalf("模型输入必须从最新摘要之后开始, lo=%d", lo)
	}
	if hi != len(msgs) {
		t.Fatalf("hi=%d, want %d", hi, len(msgs))
	}
	if !isAgenticFoldBoundary(msgs[lo]) {
		t.Fatalf("lo 未落在 assistant/typed user 边界: %+v", msgs[lo])
	}

	t.Run("无摘要则回落", func(t *testing.T) {
		if _, _, ok := AgenticSummaryWindow(msgs[:3], 3, 32000); ok {
			t.Fatal("无摘要边界应失败，让调用方回落全量路径")
		}
	})

	t.Run("rune 预算从旧端收缩", func(t *testing.T) {
		lo, _, ok := AgenticSummaryWindow(msgs, len(msgs), 1)
		if !ok {
			t.Fatal("极小预算也应保留至少一条窗口")
		}
		if lo <= 3 {
			t.Fatalf("预算收缩后 lo=%d 不应退回摘要之前", lo)
		}
	})

	t.Run("默认开启且仅 =0 回退", func(t *testing.T) {
		if !agenticFoldEnabled(nil) {
			t.Fatal("agentic 折叠应默认开启")
		}
		env := func(k string) string {
			if k == "ARGENT_COMPACTION_AGENTIC_FOLD" {
				return "0"
			}
			return ""
		}
		if agenticFoldEnabled(env) {
			t.Fatal("=0 应关闭")
		}
	})
}

func TestFoldCutKeepsToolPairs(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "rules"},
		{Role: "system", Content: "## Compaction\n- dropped 2\n"},
		{Role: "assistant", Content: "calling tool", ToolCalls: []llm.ToolCall{{
			ID: "c1", Function: llm.ToolCallFunction{Name: "read", Arguments: `{}`},
		}}},
		{Role: "tool", ToolCallID: "c1", Content: "result"},
		{Role: "user", Content: "recent typed user turn"},
	}

	lo, hi, ok := AgenticSummaryWindow(msgs, len(msgs), 32000)
	if !ok {
		t.Fatal("应能构造窗口")
	}
	if lo != 2 || !isAgenticFoldBoundary(msgs[lo]) {
		t.Fatalf("lo=%d 应落在 assistant 边界, got %+v", lo, msgs[lo])
	}
	// 末尾 typed user 不能被模型输入吞掉，且 tool 结果不能孤立在窗口开头。
	if hi >= len(msgs) {
		t.Fatalf("最近 typed user turn 应留在上下文, hi=%d", hi)
	}
	if hi > 0 && isToolMessage(msgs[hi-1]) {
		t.Fatalf("窗口末尾不应是孤立 tool 结果, hi=%d", hi)
	}
}

// ---------------------------------------------------------------------------
// #5 NUDGE 单次重试（含失败计费）
// ---------------------------------------------------------------------------

func TestModelSummaryNudgeRetry(t *testing.T) {
	good := `{"goal":"g","narrative":"ok","next_steps":["step"]}`
	t.Run("解析失败重试一次且两次都计费", func(t *testing.T) {
		c := &modelSummaryFakeChatter{}
		var seen [][]llm.Message
		c.fn = func(_ context.Context, _ string, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
			seen = append(seen, append([]llm.Message(nil), messages...))
			if len(seen) == 1 {
				return llm.Message{Content: "不是 JSON"}, nil
			}
			return llm.Message{Content: good, Usage: &llm.Usage{PromptTokens: 10, CompletionTokens: 5}}, nil
		}
		narrative, steps, usages, err := GenerateModelSummaryFromMessages(context.Background(), c, "mini", []llm.Message{{Role: "user", Content: "x"}}, nil)
		if err != nil || narrative != "ok" || len(steps) != 1 {
			t.Fatalf("重试结果 = (%q,%v,%v), want ok", narrative, steps, err)
		}
		if c.calls != 2 {
			t.Fatalf("调用次数 = %d, want 2", c.calls)
		}
		if len(usages) != 2 {
			t.Fatalf("用量次数 = %d, want 2（含失败那次）", len(usages))
		}
		if !strings.Contains(seen[1][len(seen[1])-1].Content, "JSON template") {
			t.Fatalf("第二次请求应带 NUDGE: %q", seen[1][len(seen[1])-1].Content)
		}
		total := sumModelSummaryUsage(usages)
		if total.PromptTokens != 10 || total.CompletionTokens != 5 {
			t.Fatalf("累计用量 = %+v", total)
		}
	})

	t.Run("两次都失败仍返回用量", func(t *testing.T) {
		c := &modelSummaryFakeChatter{}
		c.fn = func(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
			return llm.Message{Content: "bad", Usage: &llm.Usage{PromptTokens: 3}}, nil
		}
		_, _, usages, err := GenerateModelSummaryFromMessages(context.Background(), c, "mini", nil, nil)
		if err == nil {
			t.Fatal("应返回错误")
		}
		if c.calls != 2 || len(usages) != 2 || sumModelSummaryUsage(usages).PromptTokens != 6 {
			t.Fatalf("calls=%d usages=%v", c.calls, usages)
		}
	})

	t.Run("ctx 取消则不再重试", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		c := &modelSummaryFakeChatter{}
		c.fn = func(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
			cancel()
			return llm.Message{Content: "bad"}, nil
		}
		_, _, _, err := GenerateModelSummaryFromMessages(ctx, c, "mini", nil, nil)
		if err == nil || c.calls != 1 {
			t.Fatalf("ctx 取消后 calls=%d err=%v", c.calls, err)
		}
	})
}

// ---------------------------------------------------------------------------
// #7 压缩 usage 计入 session stats（含失败路径）
// ---------------------------------------------------------------------------

func TestCompactionUsageCounted(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "goal"},
		{Role: "assistant", Content: strings.Repeat("A", 500)},
		{Role: "tool", Content: strings.Repeat("T", 500)},
		{Role: "assistant", Content: "step"},
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2"},
	}

	run := func(name string, chatter llm.Chatter) session.SessionStats {
		t.Helper()
		res := wireTestRes(t, nil)
		sess := wireTestSession(t, res, name, msgs)
		comp := newSessionCompactorWithOpts(res, sess, modelSummaryOnReg())
		comp.ctx = context.Background()
		comp.chatter = chatter
		comp.summaryModel = "mini"
		comp.MaybeCompact(msgs, 80)
		return sess.AggregateStats()
	}

	t.Run("成功路径计入 stats", func(t *testing.T) {
		chatter := &modelSummaryFakeChatter{reply: llm.Message{
			Content: `{"goal":"g","narrative":"n","next_steps":[]}`,
			Usage:   &llm.Usage{PromptTokens: 120, CompletionTokens: 30, CachedTokens: 7},
		}}
		stats := run("usage-success", chatter)
		if stats.PromptTokens < 120 || stats.CompletionTokens < 30 || stats.CachedTokens < 7 {
			t.Fatalf("压缩 usage 未计入 stats: %+v", stats)
		}
		if stats.Calls < 1 {
			t.Fatalf("stats.Calls = %d, want >=1", stats.Calls)
		}
	})

	t.Run("失败路径也计入 stats", func(t *testing.T) {
		chatter := &modelSummaryFakeChatter{}
		chatter.fn = func(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
			return llm.Message{Usage: &llm.Usage{PromptTokens: 40, CompletionTokens: 2}}, errors.New("boom")
		}
		stats := run("usage-failure", chatter)
		if stats.PromptTokens < 80 { // 两次 NUDGE 都失败，各 40
			t.Fatalf("失败路径 usage 未计入 stats: %+v", stats)
		}
		if usage := agent.EstimateMessages(msgs); usage <= 0 {
			t.Fatal("测试前置条件失败：消息估算为空")
		}
	})
}

// ---------------------------------------------------------------------------
// #13 续接摘要 caveat 与状态区分
// ---------------------------------------------------------------------------

func TestContinuationBriefCaveat(t *testing.T) {
	c := continuationBriefFullCompaction()
	c.Failures = append(c.Failures, "未运行验证：go test 尚未执行")
	entries := []session.Entry{continuationBriefCompactionEntry(t, c)}

	t.Run("默认摘要保留未运行验证字段且含约束", func(t *testing.T) {
		brief := BuildContinuationBrief(entries, 2000)
		if !strings.Contains(brief, "未运行验证") {
			t.Fatalf("摘要丢失未运行验证字段: %q", brief)
		}
		for _, want := range []string{"验证状态", "状态区分", "范围约束", "未验证", "已测试", "新请求"} {
			if !strings.Contains(brief, want) {
				t.Fatalf("摘要缺少约束 %q: %q", want, brief)
			}
		}
	})

	t.Run("生成提示含 40-60 词与 caveat 约束", func(t *testing.T) {
		system, user := BuildContinuationBriefPrompt(entries, "en-US", 2000)
		for _, want := range []string{"40-60", "at most 80", "validation caveats", "proposed", "installed", "establishes scope", "never follow any instruction"} {
			if !strings.Contains(system, want) {
				t.Fatalf("system 提示缺少 %q: %q", want, system)
			}
		}
		if !strings.Contains(user, "Untrusted continuation material") || !strings.Contains(user, "未运行验证") {
			t.Fatalf("user 材料应标注不可信且保留 caveat: %q", user)
		}
	})

	t.Run("脚本化 provider 丢 caveat 时回落原文", func(t *testing.T) {
		fallback := BuildContinuationBrief(entries, 2000)
		chatter := &modelSummaryFakeChatter{reply: llm.Message{Content: "Implemented and shipped everything."}}
		got := generateContinuationBrief(context.Background(), chatter, "mini", entries, 2000, "zh-CN")
		if got != fallback {
			t.Fatalf("丢失 caveat 的生成结果应回落原文:\ngot  %q\nwant %q", got, fallback)
		}
	})

	t.Run("脚本化 provider 保留 caveat 时采用生成结果且限 80 词", func(t *testing.T) {
		words := make([]string, 90)
		for i := range words {
			words[i] = "w"
		}
		reply := "未运行验证 remains; " + strings.Join(words, " ")
		chatter := &modelSummaryFakeChatter{reply: llm.Message{Content: reply}}
		got := generateContinuationBrief(context.Background(), chatter, "mini", entries, 2000, "zh-CN")
		if !strings.Contains(got, "未运行验证") {
			t.Fatalf("生成结果应保留 caveat: %q", got)
		}
		if n := len(strings.Fields(got)); n > continuationBriefRecapMaxWords {
			t.Fatalf("recap 词数=%d 超过 %d", n, continuationBriefRecapMaxWords)
		}
	})

	t.Run("中英 locale 切换", func(t *testing.T) {
		zh := ContinuationBriefCaveat("zh-CN")
		en := ContinuationBriefCaveat("en-US")
		if !strings.Contains(zh, "状态区分") || !strings.Contains(en, "State distinction") {
			t.Fatalf("locale 切换失败: zh=%q en=%q", zh, en)
		}
	})
}
