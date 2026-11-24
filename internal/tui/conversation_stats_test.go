package tui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// convStatsModel 构造一个模型与可推进的冻结时钟，使统计行的时长完全确定。
func convStatsModel(model string) (*Model, *time.Time) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	m := New(Config{Model: model})
	m.started = now
	m.now = func() time.Time { return now }
	return m, &now
}

// convStatsRows 返回帧中统计行的文本（一行一条）。
func convStatsRows(m *Model, width, height int) []string {
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

// submitText 在输入框里写入文本并回车提交，并取走提交内容。
func submitText(m *Model, body string) {
	m.editor.SetText(body)
	m.HandleKey(Key{Kind: KindEnter})
	m.TakeSubmitted()
}

// endConv 复刻 run 循环的收尾：EndTurn 之后标记对话结束。
func endConv(m *Model) {
	m.EndTurn(nil)
	m.MarkConversationDone()
}

// TestConversationStatsAppearsOncePerMessage 验证：一次用户消息里即使
// 有多轮模型调用（多次 Usage），也只出现一行统计。
func TestConversationStatsAppearsOncePerMessage(t *testing.T) {
	m, now := convStatsModel("deepseek-v4.1-flash")
	baseline := frameBytes(m, 100, 24)

	// 未知事件与打点都不改变任何字节。
	m.HandleEvent(Event{Kind: EventKind("other")})
	m.HandleEvent(Event{Kind: TuiEventTurnStart})
	m.HandleEvent(Event{Kind: TuiEventTurnEnd})
	if got := frameBytes(m, 100, 24); got != baseline {
		t.Fatalf("turn markers changed the frame:\ngot:\n%q\nwant:\n%q", got, baseline)
	}
	if rows := convStatsRows(m, 100, 24); len(rows) != 0 {
		t.Fatalf("pending turn produced a stats row: %v", rows)
	}

	// 一轮对话：两次模型调用（两次 Usage），合计 400 输出 token。
	submitText(m, "hello")
	m.HandleEvent(Event{Kind: TuiEventUsage, PromptTokens: 2200, CompletionTokens: 300, CachedTokens: 1000})
	*now = now.Add(8200 * time.Millisecond)
	m.HandleEvent(Event{Kind: TuiEventUsage, PromptTokens: 2200, CompletionTokens: 100, CachedTokens: 1000})
	if rows := convStatsRows(m, 100, 24); len(rows) != 0 {
		t.Fatalf("stats row appeared before the turn ended: %v", rows)
	}
	endConv(m)
	m.HandleEvent(Event{Kind: TuiEventUsage, CompletionTokens: 0})

	rows := convStatsRows(m, 100, 24)
	if len(rows) != 1 {
		t.Fatalf("stats rows = %v, want exactly one", rows)
	}
	want := "deepseek-v4.1-flash · 8.2s · 49 tok/s"
	if !strings.Contains(rows[0], want) {
		t.Fatalf("stats row = %q, want it to contain %q", rows[0], want)
	}
	if got := m.TakeSubmitted(); got != "" {
		t.Fatalf("stats events submitted %q", got)
	}
}

// TestConversationStatsTwoMessagesProduceTwoLines 验证：两次用户消息
// 各出一行统计，互不合并。
func TestConversationStatsTwoMessagesProduceTwoLines(t *testing.T) {
	m, now := convStatsModel("model-x")
	for i := 0; i < 2; i++ {
		submitText(m, "go")
		m.HandleEvent(Event{Kind: TuiEventUsage, CompletionTokens: 20})
		*now = now.Add(time.Second)
		endConv(m)
		m.HandleEvent(Event{Kind: TuiEventUsage, CompletionTokens: 0})
	}
	rows := convStatsRows(m, 100, 24)
	if len(rows) != 2 {
		t.Fatalf("stats rows = %v, want two", rows)
	}
	for _, row := range rows {
		if !strings.Contains(row, "model-x") || !strings.Contains(row, "20 tok/s") {
			t.Fatalf("unexpected stats row %q", row)
		}
	}
}

// TestConversationStatsWithoutUsageStaysAbsent 验证：没有输出 token 时
// 不出统计行（也不泄漏 NaN/Inf）。
func TestConversationStatsWithoutUsageStaysAbsent(t *testing.T) {
	m, _ := convStatsModel("model-x")
	submitText(m, "go")
	m.MarkConversationDone()
	m.HandleEvent(Event{Kind: TuiEventUsage, CompletionTokens: 0})
	text := frameText(m, 100, 24)
	if strings.Contains(text, "NaN") || strings.Contains(text, "Inf") || len(convStatsRows(m, 100, 24)) != 0 {
		t.Fatalf("zero tokens produced a bad stats row:\n%s", text)
	}
}

// TestConversationStatsZeroElapsedStaysAbsent 验证：时钟没有前进时不出行。
func TestConversationStatsZeroElapsedStaysAbsent(t *testing.T) {
	m, _ := convStatsModel("model-x")
	submitText(m, "go")
	m.HandleEvent(Event{Kind: TuiEventUsage, CompletionTokens: 100})
	m.MarkConversationDone()
	m.HandleEvent(Event{Kind: TuiEventUsage, CompletionTokens: 0})
	if rows := convStatsRows(m, 100, 24); len(rows) != 0 {
		t.Fatalf("zero elapsed produced stats: %v", rows)
	}
}

// TestConversationStatsCJKModelNameClipsValidUTF8 验证：中文模型名截断后
// 仍是合法 UTF-8，且不超出终端宽度。
func TestConversationStatsCJKModelNameClipsValidUTF8(t *testing.T) {
	name := strings.Repeat("深度思考模型", 20)
	m, now := convStatsModel(name)
	submitText(m, "go")
	m.HandleEvent(Event{Kind: TuiEventUsage, CompletionTokens: 200})
	*now = now.Add(2 * time.Second)
	m.MarkConversationDone()
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
