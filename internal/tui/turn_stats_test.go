package tui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// turnStatsModel 构造一个模型与可推进的冻结时钟，使统计行的时长完全确定。
func turnStatsModel(model string) (*Model, *time.Time) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	m := New(Config{Model: model})
	m.started = now
	m.now = func() time.Time { return now }
	return m, &now
}

// turnStatsRows 返回帧中统计行的文本（一行一条）。
func turnStatsRows(m *Model, width, height int) []string {
	buf := m.Frame(width, height)
	var out []string
	for y := 0; y < buf.Height(); y++ {
		row := buf.RowText(y)
		if strings.Contains(row, "tok/s") {
			out = append(out, strings.TrimSpace(row))
		}
	}
	return out
}

func TestTurnStatsDefaultFrameByteExact(t *testing.T) {
	m, now := turnStatsModel("deepseek-v4.1-flash")
	baseline := frameBytes(m, 100, 24)

	// 未知事件与只打点、未结算的回合都不改变任何字节。
	m.HandleEvent(Event{Kind: EventKind("other")})
	m.HandleEvent(Event{Kind: TuiEventTurnStart})
	m.HandleEvent(Event{Kind: TuiEventTurnEnd})
	if got := frameBytes(m, 100, 24); got != baseline {
		t.Fatalf("turn markers changed the frame:\ngot:\n%q\nwant:\n%q", got, baseline)
	}
	if rows := turnStatsRows(m, 100, 24); len(rows) != 0 {
		t.Fatalf("pending turn produced a stats row: %v", rows)
	}

	// 时钟前进也不会结算统计（头部时钟本身会前进，属正常渲染）。
	*now = now.Add(2 * time.Second)
	if rows := turnStatsRows(m, 100, 24); len(rows) != 0 {
		t.Fatalf("clock advance alone produced stats: %v", rows)
	}
	for _, entry := range m.Transcript() {
		if entry.Kind == EntryStats {
			t.Fatalf("clock advance alone appended an EntryStats: %+v", entry)
		}
	}
}

func TestTurnStatsLineHasThreeFields(t *testing.T) {
	m, now := turnStatsModel("deepseek-v4.1-flash")
	m.HandleEvent(Event{Kind: TuiEventTurnStart})
	*now = now.Add(8200 * time.Millisecond)
	// 顺序复刻 agent：Usage → TurnEnd → Usage，统计在最后一条 Usage 结算。
	m.HandleEvent(Event{Kind: TuiEventUsage, PromptTokens: 2200, CompletionTokens: 369, CachedTokens: 1000})
	m.HandleEvent(Event{Kind: TuiEventTurnEnd})
	m.HandleEvent(Event{Kind: TuiEventUsage, PromptTokens: 2200, CompletionTokens: 0, CachedTokens: 1000})

	rows := turnStatsRows(m, 100, 24)
	if len(rows) != 1 {
		t.Fatalf("stats rows = %v, want exactly one", rows)
	}
	want := "deepseek-v4.1-flash · 8.2s · 45 tok/s"
	if !strings.Contains(rows[0], want) {
		t.Fatalf("stats row = %q, want it to contain %q", rows[0], want)
	}
	if got := m.TakeSubmitted(); got != "" {
		t.Fatalf("stats events submitted %q", got)
	}
}

func TestTurnStatsTwoRoundsProduceTwoLines(t *testing.T) {
	m, now := turnStatsModel("model-x")
	for i := 0; i < 2; i++ {
		m.HandleEvent(Event{Kind: TuiEventTurnStart})
		*now = now.Add(time.Second)
		m.HandleEvent(Event{Kind: TuiEventUsage, CompletionTokens: 20})
		m.HandleEvent(Event{Kind: TuiEventTurnEnd})
		m.HandleEvent(Event{Kind: TuiEventUsage, CompletionTokens: 0})
	}
	rows := turnStatsRows(m, 100, 24)
	if len(rows) != 2 {
		t.Fatalf("stats rows = %v, want two", rows)
	}
	for _, row := range rows {
		if !strings.Contains(row, "model-x") || !strings.Contains(row, "20 tok/s") {
			t.Fatalf("unexpected stats row %q", row)
		}
	}
}

func TestTurnStatsWithoutTurnStartStaysAbsent(t *testing.T) {
	m, now := turnStatsModel("model-x")
	*now = now.Add(time.Second)
	m.HandleEvent(Event{Kind: TuiEventTurnEnd})
	m.HandleEvent(Event{Kind: TuiEventUsage, CompletionTokens: 100})
	if rows := turnStatsRows(m, 100, 24); len(rows) != 0 {
		t.Fatalf("a turn without TurnStart produced stats: %v", rows)
	}
}

func TestTurnStatsZeroElapsedOrTokensStaysAbsent(t *testing.T) {
	// Δt=0：没有 NaN / Inf 泄漏。
	m, _ := turnStatsModel("model-x")
	m.HandleEvent(Event{Kind: TuiEventTurnStart})
	m.HandleEvent(Event{Kind: TuiEventTurnEnd})
	m.HandleEvent(Event{Kind: TuiEventUsage, CompletionTokens: 100})
	text := frameText(m, 100, 24)
	if strings.Contains(text, "NaN") || strings.Contains(text, "Inf") || len(turnStatsRows(m, 100, 24)) != 0 {
		t.Fatalf("zero elapsed produced a bad stats row:\n%s", text)
	}

	// 输出 token 为零：同样不出行。
	m2, now2 := turnStatsModel("model-x")
	m2.HandleEvent(Event{Kind: TuiEventTurnStart})
	*now2 = now2.Add(time.Second)
	m2.HandleEvent(Event{Kind: TuiEventTurnEnd})
	m2.HandleEvent(Event{Kind: TuiEventUsage, CompletionTokens: 0})
	if rows := turnStatsRows(m2, 100, 24); len(rows) != 0 {
		t.Fatalf("zero tokens produced stats: %v", rows)
	}
}

func TestTurnStatsCJKModelNameClipsValidUTF8(t *testing.T) {
	name := strings.Repeat("深度思考模型", 20)
	m, now := turnStatsModel(name)
	m.HandleEvent(Event{Kind: TuiEventTurnStart})
	*now = now.Add(2 * time.Second)
	m.HandleEvent(Event{Kind: TuiEventUsage, CompletionTokens: 200})
	m.HandleEvent(Event{Kind: TuiEventTurnEnd})
	m.HandleEvent(Event{Kind: TuiEventUsage, CompletionTokens: 0})

	const width = 40
	var stats []Entry
	for _, entry := range m.Transcript() {
		if entry.Kind == EntryStats {
			stats = append(stats, entry)
		}
	}
	if len(stats) != 1 {
		t.Fatalf("stats entries = %d, want 1", len(stats))
	}
	buf := m.Frame(width, 24)
	found := false
	wantPrefix := Clip(name, 8)
	for y := 0; y < buf.Height(); y++ {
		row := buf.RowText(y)
		if !utf8.ValidString(row) {
			t.Fatalf("row %d is invalid UTF-8: %q", y, row)
		}
		if w := TextWidth(row); w > width {
			t.Fatalf("row %d width = %d, want <= %d", y, w, width)
		}
		if strings.Contains(row, wantPrefix) {
			found = true
		}
	}
	if !found {
		t.Fatal("the clipped CJK stats row is missing")
	}
}
