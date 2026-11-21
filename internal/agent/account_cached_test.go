package agent

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestAccountAccumulatesCachedTokens 验证累计统计里的缓存细分会随每轮回复累加，
// 同时逐轮用量记录与 account 的返回值仍是当轮值（不重复累加）。
func TestAccountAccumulatesCachedTokens(t *testing.T) {
	var usageRows []sessionUsagePayload
	var statsRows []sessionStatsPayload
	rec := RecorderFunc(func(kind string, payload any) error {
		switch kind {
		case "usage":
			usageRows = append(usageRows, payload.(sessionUsagePayload))
		case "stats":
			statsRows = append(statsRows, payload.(sessionStatsPayload))
		}
		return nil
	})
	s := &runState{model: "m", rec: rec}

	// 内容含 CJK，但缓存 token 完全来自用量报告：CompletionTokens 已为正，
	// 估算路径不会被触发，因此内容语言与缓存字段无关。
	first := llm.Message{Role: "assistant", Content: "你好世界", Usage: &llm.Usage{
		PromptTokens: 100, CompletionTokens: 20, CachedTokens: 40, CacheWriteTokens: 30,
	}}
	second := llm.Message{Role: "assistant", Content: "ok", Usage: &llm.Usage{
		PromptTokens: 200, CompletionTokens: 30, CachedTokens: 50, CacheWriteTokens: 60,
	}}
	// 记录入参原值，确认 account 不改动调用方的 Usage。
	firstBefore := *first.Usage
	secondBefore := *second.Usage

	u1, _ := s.account(first, 999)
	u2, _ := s.account(second, 999)

	// 累计字段：cached 40+50=90，cache_write 30+60=90。
	if s.usage.CachedTokens != 90 {
		t.Fatalf("累计 CachedTokens = %d, want 90", s.usage.CachedTokens)
	}
	if s.usage.CacheWriteTokens != 90 {
		t.Fatalf("累计 CacheWriteTokens = %d, want 90", s.usage.CacheWriteTokens)
	}
	if s.usage.PromptTokens != 300 || s.usage.CompletionTokens != 50 {
		t.Fatalf("累计 prompt/completion = %d/%d, want 300/50", s.usage.PromptTokens, s.usage.CompletionTokens)
	}

	// 返回值仍是当轮值，绝不携带累计结果。
	if u1.CachedTokens != 40 || u1.CacheWriteTokens != 30 {
		t.Fatalf("第一轮返回值缓存 = %d/%d, want 40/30", u1.CachedTokens, u1.CacheWriteTokens)
	}
	if u2.CachedTokens != 50 || u2.CacheWriteTokens != 60 {
		t.Fatalf("第二轮返回值缓存 = %d/%d, want 50/60", u2.CachedTokens, u2.CacheWriteTokens)
	}

	// 逐轮 usage 记录是当轮值，不重复累加。
	if len(usageRows) != 2 {
		t.Fatalf("usage 记录数 = %d, want 2", len(usageRows))
	}
	if usageRows[0].Cached != 40 || usageRows[1].Cached != 50 {
		t.Fatalf("逐轮 usage.Cached = %d/%d, want 40/50", usageRows[0].Cached, usageRows[1].Cached)
	}

	// 运行总计的 stats 记录捕获到非 0 的累计缓存。
	s.recordRunStats()
	if len(statsRows) != 1 {
		t.Fatalf("stats 记录数 = %d, want 1", len(statsRows))
	}
	if statsRows[0].CachedTokens != 90 {
		t.Fatalf("stats.CachedTokens = %d, want 90", statsRows[0].CachedTokens)
	}
	if statsRows[0].PromptTokens != 300 || statsRows[0].CompletionTokens != 50 {
		t.Fatalf("stats prompt/completion = %d/%d, want 300/50", statsRows[0].PromptTokens, statsRows[0].CompletionTokens)
	}

	// account 只读取入参的副本，不改动调用方持有的 Usage。
	if *first.Usage != firstBefore || *second.Usage != secondBefore {
		t.Fatalf("入参 Usage 被改动: first=%+v second=%+v", *first.Usage, *second.Usage)
	}
}

// TestAccountCachedLeavesBudgetAndCostUntouched 验证在 prompt/completion 相同的
// 情况下，缓存字段的有无不会改变花费和预算判定。
func TestAccountCachedLeavesBudgetAndCostUntouched(t *testing.T) {
	prices := map[string]Price{"m": {Input: 1_000_000, Output: 2_000_000}}
	budget := Budget{MaxTotalTokens: 200}

	makeReply := func(cached, cacheWrite int) llm.Message {
		return llm.Message{Usage: &llm.Usage{
			PromptTokens: 100, CompletionTokens: 50,
			CachedTokens: cached, CacheWriteTokens: cacheWrite,
		}}
	}
	newState := func() *runState {
		return &runState{
			model:     "m",
			opts:      Options{Prices: prices, Budget: budget},
			costWatch: NewCostWatcher(0),
		}
	}

	plain := newState()  // 无缓存
	cached := newState() // 有缓存
	plain.account(makeReply(0, 0), 0)
	cached.account(makeReply(80, 20), 0)

	// 价格只按 prompt+completion 计算，缓存不重复计费。
	if plain.costUSD != cached.costUSD {
		t.Fatalf("花费被缓存影响: plain=%v cached=%v", plain.costUSD, cached.costUSD)
	}
	if !plain.costKnown || !cached.costKnown {
		t.Fatal("两套状态都应标记为价格已知")
	}
	// TotalTokens 口径一致：缓存不额外计入可计费 token。
	if plain.usage.TotalTokens() != cached.usage.TotalTokens() {
		t.Fatalf("TotalTokens 被缓存影响: %d vs %d", plain.usage.TotalTokens(), cached.usage.TotalTokens())
	}

	// 第一轮 prompt+completion=150 < 200：两者都不突破。
	r1, _, ok1 := plain.budgetBreach()
	r2, _, ok2 := cached.budgetBreach()
	if r1 != r2 || ok1 != ok2 || ok1 {
		t.Fatalf("第一轮 Breach 不同: (%q,%v) vs (%q,%v)", r1, ok1, r2, ok2)
	}

	// 再累加一轮，prompt+completion=300 >= 200：两者同时突破，缓存不改变结论。
	plain.account(makeReply(0, 0), 0)
	cached.account(makeReply(80, 20), 0)
	r1, _, ok1 = plain.budgetBreach()
	r2, _, ok2 = cached.budgetBreach()
	if r1 != r2 || ok1 != ok2 || !ok1 {
		t.Fatalf("第二轮 Breach 不同: (%q,%v) vs (%q,%v)", r1, ok1, r2, ok2)
	}
	if plain.costUSD != cached.costUSD {
		t.Fatalf("累计花费被缓存影响: plain=%v cached=%v", plain.costUSD, cached.costUSD)
	}
	// 累计缓存字段确实不同，但预算/成本判定相同。
	if plain.usage.CachedTokens != 0 || cached.usage.CachedTokens != 160 {
		t.Fatalf("缓存累计异常: plain=%d cached=%d", plain.usage.CachedTokens, cached.usage.CachedTokens)
	}
}

// TestAccountZeroCacheKeepsLegacyBehavior 验证无缓存/零值路径行为不变：
// 既有 wire 不带缓存字段的回复，以及完全依赖估算的回复，缓存计数都保持为 0。
func TestAccountZeroCacheKeepsLegacyBehavior(t *testing.T) {
	var stats sessionStatsPayload
	rec := RecorderFunc(func(kind string, payload any) error {
		if kind == "stats" {
			stats = payload.(sessionStatsPayload)
		}
		return nil
	})
	s := &runState{model: "m", rec: rec}

	// 旧 wire：用量里没有缓存字段。
	s.account(llm.Message{Usage: &llm.Usage{PromptTokens: 100, CompletionTokens: 50}}, 0)
	s.recordRunStats()
	if s.usage.CachedTokens != 0 || s.usage.CacheWriteTokens != 0 {
		t.Fatalf("无缓存路径累计非零: %+v", s.usage)
	}
	if stats.CachedTokens != 0 {
		t.Fatalf("stats.CachedTokens = %d, want 0", stats.CachedTokens)
	}
	if s.usage.PromptTokens != 100 || s.usage.CompletionTokens != 50 {
		t.Fatalf("无缓存路径 prompt/completion = %d/%d, want 100/50", s.usage.PromptTokens, s.usage.CompletionTokens)
	}

	// 完全估算路径：无用量报告，含 CJK 内容也不应凭空产生缓存字段。
	s2 := &runState{model: "m"}
	u, prompt := s2.account(llm.Message{Content: "中文内容"}, 77)
	if u.PromptTokens != 77 || prompt != 77 {
		t.Fatalf("估算 prompt = %d/%d, want 77/77", u.PromptTokens, prompt)
	}
	if u.CachedTokens != 0 || u.CacheWriteTokens != 0 {
		t.Fatalf("估算路径缓存非零: %+v", u)
	}
	if s2.usage.CachedTokens != 0 || s2.usage.CacheWriteTokens != 0 {
		t.Fatalf("估算路径累计缓存非零: %+v", s2.usage)
	}
}
