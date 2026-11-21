package tui

import (
	"strings"
	"testing"
	"time"
)

// usageFrameClock 冻结帧时钟，使同一模型在不同调用间产生逐字节一致的帧。
var usageFrameClock = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// deterministicUsageModel 构造起始时间与当前时间都被固定的模型，
// 消除时钟对帧字节的影响。
func deterministicUsageModel(locale string) *Model {
	m := New(Config{Locale: locale})
	m.started = usageFrameClock
	m.now = func() time.Time { return usageFrameClock }
	// 这些用例专门验证用量行的渲染，显式打开 opt-in 门控。
	m.SetUsageVisible(true)
	return m
}

func TestUsageEventSetsCounters(t *testing.T) {
	m := deterministicUsageModel("en")
	if m.usageIn != 0 || m.usageOut != 0 || m.usageCached != 0 {
		t.Fatalf("fresh model must have zero usage, got in=%d out=%d cached=%d",
			m.usageIn, m.usageOut, m.usageCached)
	}
	m.HandleEvent(Event{
		Kind:             TuiEventUsage,
		PromptTokens:     1200,
		CompletionTokens: 300,
		CachedTokens:     900,
	})
	if m.usageIn != 1200 || m.usageOut != 300 || m.usageCached != 900 {
		t.Fatalf("usage event did not set counters, got in=%d out=%d cached=%d",
			m.usageIn, m.usageOut, m.usageCached)
	}
	// 用量经现有渲染门显示：头部与状态行各出现一次。
	text := frameText(m, 110, 24)
	if got := strings.Count(text, "cached 900"); got < 2 {
		t.Fatalf("cached count should appear in header and status, got %d: %s", got, text)
	}
}

func TestUsageEventAbsentLeavesFrameBytesUnchanged(t *testing.T) {
	baseline := frameBytes(deterministicUsageModel("en"), 110, 24)

	m := deterministicUsageModel("en")
	// 事件桥在无用量可转发时的常态：未知事件应被忽略，不改变任何字节。
	m.HandleEvent(Event{Kind: EventKind("other")})
	if m.usageIn != 0 || m.usageOut != 0 || m.usageCached != 0 {
		t.Fatalf("usage must stay zero without a usage event, got in=%d out=%d cached=%d",
			m.usageIn, m.usageOut, m.usageCached)
	}
	if got := frameBytes(m, 110, 24); got != baseline {
		t.Fatalf("frame bytes changed without a usage event\nbaseline=%q\ngot=%q", baseline, got)
	}
}

func TestUsageEventFrameDeterministic(t *testing.T) {
	event := Event{
		Kind:             TuiEventUsage,
		PromptTokens:     999_999,
		CompletionTokens: 42,
		CachedTokens:     512,
	}
	m1, m2 := deterministicUsageModel("en"), deterministicUsageModel("en")
	m1.HandleEvent(event)
	m2.HandleEvent(event)
	if a, b := frameBytes(m1, 100, 24), frameBytes(m2, 100, 24); a != b {
		t.Fatalf("identical usage events produced different frames")
	}
	// SetUsage/SetCachedTokens 写入的是绝对值，重复同一事件不应改变帧。
	before := frameBytes(m1, 100, 24)
	m1.HandleEvent(event)
	if after := frameBytes(m1, 100, 24); after != before {
		t.Fatalf("repeating the same usage event changed the frame")
	}
}

func TestUsageEventCJKAndBoundary(t *testing.T) {
	cases := []struct {
		name, locale, label        string
		prompt, completion, cached int
	}{
		{"zh-label", "zh", "缓存", 1500, 250, 1000},
		{"large-prompt", "en", "cached", 1_234_567, 0, 999},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := deterministicUsageModel(tc.locale)
			m.HandleEvent(Event{
				Kind:             TuiEventUsage,
				PromptTokens:     tc.prompt,
				CompletionTokens: tc.completion,
				CachedTokens:     tc.cached,
			})
			if m.usageIn != tc.prompt || m.usageOut != tc.completion || m.usageCached != tc.cached {
				t.Fatalf("counters mismatch, got in=%d out=%d cached=%d",
					m.usageIn, m.usageOut, m.usageCached)
			}
			want := tc.label + " " + compactCount(tc.cached)
			text := frameText(m, 110, 24)
			if got := strings.Count(text, want); got < 2 {
				t.Fatalf("want %q in header and status, got %d: %s", want, got, text)
			}
		})
	}
}
