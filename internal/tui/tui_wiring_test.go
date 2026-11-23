package tui

import (
	"strings"
	"testing"
	"time"
)

// TestScrollByLockedClampsOverscrollAtBothEnds 检查翻页/滚轮输入在
// 最旧行和最新行都被钳制：大型会话不得
// 在视图移动前要求撤销几十次过度滚动的 tick。
func TestScrollByLockedClampsOverscrollAtBothEnds(t *testing.T) {
	m := New(Config{})
	for i := 0; i < 100; i++ {
		m.AppendNotice("line")
	}
	m.Frame(80, 24)

	total, viewport := m.bodyLines, m.bodyH
	max := transcriptScrollMax(total, viewport)
	if max <= 3 {
		t.Fatalf("need a scrollable range for this test, got total=%d viewport=%d", total, viewport)
	}

	m.Scroll(max + 5000)
	if m.scroll != max {
		t.Fatalf("overscrolling toward the top gave scroll=%d, want clamped %d", m.scroll, max)
	}

	m.Scroll(-3)
	if want := max - 3; m.scroll != want {
		t.Fatalf("one step back from the clamped top gave scroll=%d, want %d", m.scroll, want)
	}

	m.Scroll(-(max + 5000))
	if m.scroll != 0 {
		t.Fatalf("overscrolling toward the bottom gave scroll=%d, want 0", m.scroll)
	}
}

// TestScrollBeforeFirstPaintStillMoves 覆盖第一帧之前到达的按键输入：
// 缓存的度量为空，因此滚动回退到新
// 渲染的行数，而不是被钳制到陈旧的零。
func TestScrollBeforeFirstPaintStillMoves(t *testing.T) {
	m := New(Config{})
	for i := 0; i < 50; i++ {
		m.AppendNotice("line")
	}
	m.Scroll(3)
	if m.scroll <= 0 {
		t.Fatalf("scroll before the first paint = %d, want a positive offset", m.scroll)
	}
}

// TestPaintReclaimsStaleScroll 检查绘制器使用与输入路径相同的
// 钳制辅助函数，使陈旧偏移永不留下空白视口。
func TestPaintReclaimsStaleScroll(t *testing.T) {
	m := New(Config{})
	for i := 0; i < 50; i++ {
		m.AppendNotice("line")
	}
	m.Frame(80, 24)
	want := transcriptScrollMax(m.bodyLines, m.bodyH)
	if want <= 0 {
		t.Fatal("expected a scrollable transcript")
	}

	m.mu.Lock()
	m.scroll = want + 1000
	m.mu.Unlock()
	m.Frame(80, 24)
	if m.scroll != want {
		t.Fatalf("paint left scroll=%d, want clamped %d", m.scroll, want)
	}
}

// TestScrollAfterAppendsUsesFreshLineCount 覆盖两次绘制之间
// 追加的条目：缓存的正文行数陈旧，因此滚动必须重新推导它。
func TestScrollAfterAppendsUsesFreshLineCount(t *testing.T) {
	m := New(Config{})
	for i := 0; i < 10; i++ {
		m.AppendNotice("line")
	}
	m.Frame(80, 24)
	for i := 0; i < 90; i++ {
		m.AppendNotice("line")
	}

	m.Scroll(5000)
	m.mu.Lock()
	l := m.layoutLocked(80, 24)
	// 必须使用新渲染的计数（而非缓存的 10 行）。
	want := transcriptScrollMax(len(m.renderBodyLocked(l.bodyW)), l.bodyH)
	m.mu.Unlock()
	if m.scroll != want {
		t.Fatalf("scroll = %d, want clamp against fresh line count %d", m.scroll, want)
	}
}

// TestStatusSeverityStackWiring 检查 SetStatus 和 SetStatusSeverity
// 把通知发布到栈中、较高严重度的通知胜出，且
// SetStatus("") 清除所有存活通知。
func TestStatusSeverityStackWiring(t *testing.T) {
	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	m := New(Config{Locale: "en"})
	m.now = func() time.Time { return base }

	m.SetStatus("plain info")
	if got := m.Frame(100, 24).RowText(m.layoutLocked(100, 24).statusY); !strings.Contains(got, "plain info") {
		t.Fatalf("info notice missing from status line: %q", got)
	}

	m.SetStatusSeverity(SeverityError, "disk full")
	got := m.Frame(100, 24).RowText(m.layoutLocked(100, 24).statusY)
	if !strings.Contains(got, "disk full") || strings.Contains(got, "plain info") {
		t.Fatalf("error notice should outrank the info notice, status = %q", got)
	}
	if n, ok := m.notices.Peek(); !ok || n.Severity != SeverityError || n.Text != "disk full" {
		t.Fatalf("stack top = %+v (ok=%v), want the error notice", n, ok)
	}

	m.SetStatus("")
	if got := m.Frame(100, 24).RowText(m.layoutLocked(100, 24).statusY); strings.Contains(got, "plain info") || strings.Contains(got, "disk full") {
		t.Fatalf("SetStatus(\"\") must clear every notice, status = %q", got)
	}
	if got := m.notices.Len(); got != 0 {
		t.Fatalf("stack still holds %d notices after clear", got)
	}
}

// TestStatusNoticeExpiresOnStatusLine 检查 TTL 接线：状态行在
// 通知 TTL 后隐藏它，即使没有较新的通知取代它。
func TestStatusNoticeExpiresOnStatusLine(t *testing.T) {
	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	m := New(Config{Locale: "en"})
	m.now = func() time.Time { return base }
	m.SetStatus("temporary")

	later := base.Add(DefaultNoticeTTL + time.Second)
	m.now = func() time.Time { return later }
	if got := m.Frame(100, 24).RowText(m.layoutLocked(100, 24).statusY); strings.Contains(got, "temporary") {
		t.Fatalf("expired notice still on status line: %q", got)
	}
}

// TestPruneNoticesRetiresExpired 检查 run 循环的定期清理钩子。
func TestPruneNoticesRetiresExpired(t *testing.T) {
	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	m := New(Config{Locale: "en"})
	m.now = func() time.Time { return base }
	m.SetStatusSeverity(SeverityWarn, "careful")
	if got := m.notices.Len(); got != 1 {
		t.Fatalf("stack len = %d, want 1", got)
	}

	m.PruneNotices(base.Add(DefaultNoticeTTL + time.Second))
	if got := m.notices.Len(); got != 0 {
		t.Fatalf("prune left %d notices, want 0", got)
	}
	if !m.Dirty() {
		t.Fatal("removing a live notice must mark the model dirty")
	}
}
