package tui

import (
	"fmt"
	"strings"
	"testing"
)

func bodySnapshot(m *Model, width int) string {
	m.mu.Lock()
	lines := m.bodyLinesLocked(width)
	m.mu.Unlock()
	var out strings.Builder
	for _, line := range lines {
		fmt.Fprintf(&out, "%+v\n", line.Spans)
	}
	return out.String()
}

func TestEntryCacheReusesUnchangedEntries(t *testing.T) {
	m := New(Config{})
	m.AppendUser("first")
	m.AppendUser("second")
	m.Frame(80, 24)
	if len(m.entryCache) != 2 {
		t.Fatalf("cache has %d slots, want 2", len(m.entryCache))
	}
	first := &m.entryCache[0].lines[0]
	second := &m.entryCache[1].lines[0]

	// A new streamed entry must not invalidate the two existing ones.
	m.AppendStream("delta")
	m.Frame(80, 24)
	if len(m.entryCache) != 3 {
		t.Fatalf("cache has %d slots after streaming, want 3", len(m.entryCache))
	}
	if &m.entryCache[0].lines[0] != first {
		t.Fatal("the first entry was re-rendered")
	}
	if &m.entryCache[1].lines[0] != second {
		t.Fatal("the second entry was re-rendered")
	}
}

func TestEntryCacheReRendersOnlyStreamingEntry(t *testing.T) {
	m := New(Config{})
	m.AppendUser("question")
	m.AppendStream("hello")
	m.Frame(80, 24)
	userLine := &m.entryCache[0].lines[0]

	m.AppendStream(" world")
	m.Frame(80, 24)
	if &m.entryCache[0].lines[0] != userLine {
		t.Fatal("the user entry was re-rendered while the assistant streamed")
	}
	var text strings.Builder
	for _, line := range m.entryCache[1].lines {
		text.WriteString(line.Text())
	}
	if !strings.Contains(text.String(), "hello world") {
		t.Fatalf("the live assistant entry was not refreshed: %q", text.String())
	}
}

func TestEntryCacheInvalidatesOnRenderingInputs(t *testing.T) {
	cases := []struct {
		name   string
		width  int // 0 keeps the base width
		mutate func(*Model)
	}{
		{"theme", 0, func(m *Model) { _ = m.SetTheme(ThemeLight) }},
		{"no_color", 0, func(m *Model) { m.SetNoColor(true) }},
		{"expanded", 0, func(m *Model) {
			m.mu.Lock()
			m.expanded = true
			m.mu.Unlock()
		}},
		{"width", 40, func(*Model) {}},
		{"entry_text", 0, func(m *Model) { m.AppendUser("extra") }},
		{"entry_failed", 0, func(m *Model) {
			m.mu.Lock()
			m.entries[0].Failed = true
			m.entries[0].Attempt = AttemptFailed
			m.markDirtyLocked()
			m.mu.Unlock()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := New(Config{})
			m.AppendTool("read", `{"path":"a.go"}`, strings.Repeat("word ", 30)+"\none\ntwo\nthree", false)
			before := bodySnapshot(m, 60)
			tc.mutate(m)
			width := tc.width
			if width == 0 {
				width = 60
			}
			after := bodySnapshot(m, width)
			if before == after {
				t.Fatal("the body did not change after a rendering input changed")
			}
		})
	}
}

func TestEntryCacheKeepsEntryOrder(t *testing.T) {
	m := New(Config{})
	for _, text := range []string{"alpha", "beta", "gamma"} {
		m.AppendUser(text)
	}
	bodySnapshot(m, 80) // populate the cache

	m.mu.Lock()
	m.entries[1].Text = "BETA"
	m.mu.Unlock()
	body := bodySnapshot(m, 80)

	a, b, c := strings.Index(body, "alpha"), strings.Index(body, "BETA"), strings.Index(body, "gamma")
	if a < 0 || b < 0 || c < 0 || !(a < b && b < c) {
		t.Fatalf("cached entry order is wrong:\n%s", body)
	}
}

func TestClearCommandDropsEntryCache(t *testing.T) {
	m := New(Config{})
	m.AppendUser("alpha")
	m.Frame(80, 24)
	if len(m.entryCache) != 1 {
		t.Fatalf("cache has %d slots before clear, want 1", len(m.entryCache))
	}
	m.mu.Lock()
	m.commandLocked("/clear")
	m.mu.Unlock()
	if len(m.entryCache) != 0 {
		t.Fatalf("cache has %d slots after clear, want 0", len(m.entryCache))
	}
}
