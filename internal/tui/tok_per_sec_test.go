package tui

import (
	"strings"
	"testing"
	"time"
)

// TestPureStreamDurationExcludesFirstTokenWait 验证纯流式时长只取
// 首 token 到流结束的一段，刻意排除首 token 等待与工具执行时间。
func TestPureStreamDurationExcludesFirstTokenWait(t *testing.T) {
	turnStart := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	firstToken := turnStart.Add(1200 * time.Millisecond) // 首 token 等待 1.2s
	streamed := firstToken.Add(2500 * time.Millisecond)  // 纯流式 2.5s
	completed := streamed.Add(800 * time.Millisecond)    // 结算前的收尾 0.8s

	if got := pureStreamDuration(firstToken, streamed); got != 2500*time.Millisecond {
		t.Fatalf("pureStreamDuration = %v, want 2.5s (wait/tool time excluded)", got)
	}
	// 端点缺失或顺序颠倒都不计入，Δt<=0 由调用方据此跳过。
	for _, tc := range []struct {
		name       string
		first, end time.Time
	}{
		{"missing first", time.Time{}, streamed},
		{"missing end", firstToken, time.Time{}},
		{"reversed", streamed, firstToken},
		{"same instant", firstToken, firstToken},
	} {
		if got := pureStreamDuration(tc.first, tc.end); got != 0 {
			t.Fatalf("pureStreamDuration(%s) = %v, want 0", tc.name, got)
		}
	}
	_ = completed // 完成时刻被记录，但不参与纯流式时长计算。
}

// TestAggregateTokenRateWeightedByDuration 验证先聚合 Σtokens/Σdurations
// 再相除：两步速率 30 与 60 的加权结果为 50，而非算术平均 45。
func TestAggregateTokenRateWeightedByDuration(t *testing.T) {
	const (
		step1Tokens = 60
		step2Tokens = 240
	)
	step1 := 2 * time.Second
	step2 := 4 * time.Second
	got := aggregateTokenRate(step1Tokens+step2Tokens, step1+step2)
	if got != "50 tok/s" {
		t.Fatalf("aggregateTokenRate = %q, want \"50 tok/s\" (weighted, not 45)", got)
	}
	// 单步样本须与 R33 相邻增量同值，保证格式一致。
	if got := aggregateTokenRate(90, 2*time.Second); got != "45 tok/s" {
		t.Fatalf("single-step rate = %q, want \"45 tok/s\"", got)
	}
}

// TestAggregateTokenRateZeroDurationNoNaN 验证 Δt=0/负值、零 token 都
// 返回空字符串，绝不把 NaN/Inf 泄漏到帧。
func TestAggregateTokenRateZeroDurationNoNaN(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tokens   int
		duration time.Duration
	}{
		{"zero duration", 100, 0},
		{"negative duration", 100, -time.Second},
		{"zero tokens", 0, time.Second},
		{"negative tokens", -1, time.Second},
	} {
		if got := aggregateTokenRate(tc.tokens, tc.duration); got != "" {
			t.Fatalf("aggregateTokenRate(%s) = %q, want empty", tc.name, got)
		}
	}
}

// TestUsageRatePrefersStreamTiming 验证带时间点的 Usage 走纯流式聚合：
// 每步只计 (StreamedAt-FirstTokenAt)，且跨步 Σtokens/Σdurations。
func TestUsageRatePrefersStreamTiming(t *testing.T) {
	base := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)
	m := deterministicUsageModel("en")

	m.HandleEvent(Event{
		Kind:             TuiEventUsage,
		PromptTokens:     200,
		CompletionTokens: 60,
		CachedTokens:     150,
		// 首 token 等待 1.2s，纯流式 2s；若误用等待时间会得出更低速率。
		FirstTokenAt: base.Add(1200 * time.Millisecond),
		StreamedAt:   base.Add(3200 * time.Millisecond),
		CompletedAt:  base.Add(3500 * time.Millisecond),
	})
	if m.usageRate != "30 tok/s" {
		t.Fatalf("step1 usageRate = %q, want \"30 tok/s\"", m.usageRate)
	}

	m.HandleEvent(Event{
		Kind:             TuiEventUsage,
		PromptTokens:     300,
		CompletionTokens: 240,
		CachedTokens:     150,
		FirstTokenAt:     base.Add(5 * time.Second),
		StreamedAt:       base.Add(9 * time.Second),
		CompletedAt:      base.Add(10 * time.Second),
	})
	// Σtokens=300, Σdurations=6s → 50 tok/s；不是 (30+60)/2=45。
	if m.usageRate != "50 tok/s" {
		t.Fatalf("aggregated usageRate = %q, want \"50 tok/s\"", m.usageRate)
	}
	if m.usageStreamTokens != 300 || m.usageStreamDuration != 6*time.Second {
		t.Fatalf("aggregation sums = %d tokens / %v, want 300 / 6s",
			m.usageStreamTokens, m.usageStreamDuration)
	}
	// 三个时间点按轮记录（这里为最近一步）。
	if !m.usageFirstTokenAt.Equal(base.Add(5*time.Second)) ||
		!m.usageStreamedAt.Equal(base.Add(9*time.Second)) ||
		!m.usageCompletedAt.Equal(base.Add(10*time.Second)) {
		t.Fatal("the three stream time points were not recorded")
	}
}

// TestUsageRateFallbackMatchesR33 验证时间点缺失时速率与 R33 逐字节一致，
// 且显式传零值时间点与完全不带字段的帧相同。
func TestUsageRateFallbackMatchesR33(t *testing.T) {
	m, now := usageRateModel()
	m.HandleEvent(Event{Kind: TuiEventUsage, PromptTokens: 200, CompletionTokens: 100, CachedTokens: 150})
	if m.usageRate != "" {
		t.Fatalf("first Usage must not invent a rate, got %q", m.usageRate)
	}
	*now = now.Add(2 * time.Second)
	m.HandleEvent(Event{Kind: TuiEventUsage, PromptTokens: 200, CompletionTokens: 190, CachedTokens: 150})
	if m.usageRate != "45 tok/s" {
		t.Fatalf("fallback usageRate = %q, want \"45 tok/s\"", m.usageRate)
	}

	// 显式零值时间点必须与不带字段完全同帧：用一条时钟轨迹相同的模型。
	zeroNow := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	withZero, _ := usageRateModel()
	withZero.now = func() time.Time { return zeroNow }
	zeroFields := []Event{
		{Kind: TuiEventUsage, PromptTokens: 200, CompletionTokens: 100, CachedTokens: 150,
			FirstTokenAt: time.Time{}, StreamedAt: time.Time{}, CompletedAt: time.Time{}},
		{Kind: TuiEventUsage, PromptTokens: 200, CompletionTokens: 190, CachedTokens: 150,
			FirstTokenAt: time.Time{}, StreamedAt: time.Time{}, CompletedAt: time.Time{}},
	}
	withZero.HandleEvent(zeroFields[0])
	zeroNow = zeroNow.Add(2 * time.Second)
	withZero.HandleEvent(zeroFields[1])
	if got, want := frameBytes(withZero, 110, 24), frameBytes(m, 110, 24); got != want {
		t.Fatal("explicit zero time points changed the frame vs. no fields")
	}
}

// TestStreamTimingZeroFieldsKeepDefaultFrameByteExact 验证 opt-in 门关闭时，
// 即便收到带时间点的 Usage，默认帧仍逐字节不变；门开启但无速率样本时
// 也不得凭空出现 ⚡。
func TestStreamTimingZeroFieldsKeepDefaultFrameByteExact(t *testing.T) {
	base := frameBytes(deterministicUsageModel("en"), 110, 24)
	m := deterministicUsageModel("en")
	m.HandleEvent(Event{Kind: TuiEventUsage, PromptTokens: 200, CompletionTokens: 100,
		FirstTokenAt: time.Time{}, StreamedAt: time.Time{}, CompletedAt: time.Time{}})
	withUsage := frameBytes(m, 110, 24)
	if withUsage == base {
		t.Fatal("the usage gate must actually render counters")
	}
	if strings.Contains(withUsage, "⚡") {
		t.Fatalf("zero time points must not invent a rate: %s", withUsage)
	}

	// 门关闭：即便时间点齐全也不得泄漏任何速率字节到默认帧。
	gated := New(Config{})
	gated.started, gated.now = usageFrameClock, func() time.Time { return usageFrameClock }
	gatedBase := frameBytes(gated, 110, 24)
	t0 := usageFrameClock
	gated.HandleEvent(Event{Kind: TuiEventUsage, PromptTokens: 200, CompletionTokens: 100,
		FirstTokenAt: t0, StreamedAt: t0.Add(2 * time.Second), CompletedAt: t0.Add(3 * time.Second)})
	if got := frameBytes(gated, 110, 24); got != gatedBase {
		t.Fatal("stream timing leaked into the default (gated-off) frame")
	}
}
