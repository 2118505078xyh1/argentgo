package tui

import (
	"strings"
	"testing"
	"time"
)

// TestScrollByLockedClampsOverscrollAtBothEnds checks that page/wheel input is
// clamped at the oldest line as well as at the newest one: a large session must
// not require undoing dozens of overscrolled ticks before the view moves.
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

// TestScrollBeforeFirstPaintStillMoves covers key input that arrives before the
// first frame: the cached metrics are empty, so scroll falls back to a freshly
// rendered line count instead of being clamped to a stale zero.
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

// TestPaintReclaimsStaleScroll checks that the painter uses the same clamping
// helper as the input path, so a stale offset can never leave a blank viewport.
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

// TestScrollAfterAppendsUsesFreshLineCount covers entries appended between two
// paints: the cached body line count is stale, so scroll must re-derive it.
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
	// The freshly rendered count (not the 10-line cached one) must be used.
	want := transcriptScrollMax(len(m.renderBodyLocked(l.bodyW)), l.bodyH)
	m.mu.Unlock()
	if m.scroll != want {
		t.Fatalf("scroll = %d, want clamp against fresh line count %d", m.scroll, want)
	}
}

// TestStatusSeverityStackWiring checks that SetStatus and SetStatusSeverity
// publish notices into the stack, that a higher-severity notice wins, and that
// SetStatus("") clears every live notice.
func TestStatusSeverityStackWiring(t *testing.T) {
	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	m := New(Config{Locale: "en"})
	m.now = func() time.Time { return base }

	m.SetStatus("plain info")
	if got := m.Frame(100, 24).RowText(23); !strings.Contains(got, "plain info") {
		t.Fatalf("info notice missing from status line: %q", got)
	}

	m.SetStatusSeverity(SeverityError, "disk full")
	got := m.Frame(100, 24).RowText(23)
	if !strings.Contains(got, "disk full") || strings.Contains(got, "plain info") {
		t.Fatalf("error notice should outrank the info notice, status = %q", got)
	}
	if n, ok := m.notices.Peek(); !ok || n.Severity != SeverityError || n.Text != "disk full" {
		t.Fatalf("stack top = %+v (ok=%v), want the error notice", n, ok)
	}

	m.SetStatus("")
	if got := m.Frame(100, 24).RowText(23); strings.Contains(got, "plain info") || strings.Contains(got, "disk full") {
		t.Fatalf("SetStatus(\"\") must clear every notice, status = %q", got)
	}
	if got := m.notices.Len(); got != 0 {
		t.Fatalf("stack still holds %d notices after clear", got)
	}
}

// TestStatusNoticeExpiresOnStatusLine checks TTL wiring: the status line hides
// a notice after its TTL even when no newer notice replaces it.
func TestStatusNoticeExpiresOnStatusLine(t *testing.T) {
	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	m := New(Config{Locale: "en"})
	m.now = func() time.Time { return base }
	m.SetStatus("temporary")

	later := base.Add(DefaultNoticeTTL + time.Second)
	m.now = func() time.Time { return later }
	if got := m.Frame(100, 24).RowText(23); strings.Contains(got, "temporary") {
		t.Fatalf("expired notice still on status line: %q", got)
	}
}

// TestPruneNoticesRetiresExpired checks the run loop's periodic prune hook.
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
