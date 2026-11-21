package tui

import (
	"strings"
	"testing"
)

func overlayModel() *Model {
	m := New(Config{})
	m.SetCommandInfo(commandInfos())
	typeCommandText(m, "/")
	return m
}

// overlayRow 返回浮层中包含 needle 的屏幕行号。
func overlayRow(m *Model, width, height int, needle string) (int, bool) {
	buf := m.Frame(width, height)
	for y := 0; y < buf.Height(); y++ {
		if strings.Contains(buf.RowText(y), needle) {
			return y, true
		}
	}
	return -1, false
}

func overlayCellReverse(m *Model, width, height int, row, col int) bool {
	buf := m.Frame(width, height)
	if row < 0 || row >= buf.Height() || col < 0 || col >= buf.Width() {
		return false
	}
	return buf.cells[row*buf.Width()+col].S.Reverse
}

func TestCompletionOverlayAppearsOnSlashWithHighlightedFirstItem(t *testing.T) {
	m := overlayModel()
	if !m.completionOverlay || m.showCommands {
		t.Fatalf("overlay = %v, side panel = %v; want overlay only", m.completionOverlay, m.showCommands)
	}
	row, ok := overlayRow(m, 100, 24, "/commit")
	if !ok {
		t.Fatal("the floating candidate is missing")
	}
	text := m.Frame(100, 24).RowText(row)
	if !strings.HasPrefix(text, "│ > ") {
		t.Fatalf("the selected candidate must use '> ', got %q", text)
	}
	if !overlayCellReverse(m, 100, 24, row, 5) {
		t.Fatalf("the selected row must be highlighted in reverse: %q", text)
	}
	if _, ok := overlayRow(m, 100, 24, "(1/4)"); !ok {
		t.Fatal("the (1/4) counter line is missing")
	}
	// 侧栏仍由 Ctrl+O 控制，且复用同一条候选行与计数行。
	m.HandleKey(Key{Kind: KindCtrlO})
	panel := panelText(m, 100, 24)
	for _, want := range []string{"COMMANDS", "> /commit", "(1/4)"} {
		if !strings.Contains(panel, want) {
			t.Fatalf("side panel is missing %q:\n%s", want, panel)
		}
	}
}

func TestCompletionOverlayDownMovesCounterAndHighlight(t *testing.T) {
	m := overlayModel()
	m.HandleKey(Key{Kind: KindDown})
	if _, ok := overlayRow(m, 100, 24, "(2/4)"); !ok {
		t.Fatal("Down did not update the counter to (2/4)")
	}
	second, ok := overlayRow(m, 100, 24, "/compile")
	if !ok {
		t.Fatal("the second candidate is missing")
	}
	first, _ := overlayRow(m, 100, 24, "/commit")
	if !overlayCellReverse(m, 100, 24, second, 5) {
		t.Fatal("the second row is not highlighted after Down")
	}
	if overlayCellReverse(m, 100, 24, first, 5) {
		t.Fatal("the first row stayed highlighted after Down")
	}
}

func TestCompletionOverlayClickAcceptsThirdCandidate(t *testing.T) {
	m := overlayModel()
	m.Frame(100, 24)
	var hit *completionHit
	for i := range m.completionHits {
		if m.completionHits[i].index == 2 {
			hit = &m.completionHits[i]
			break
		}
	}
	if hit == nil {
		t.Fatalf("no hit box for the third candidate: %+v", m.completionHits)
	}
	m.MouseDown(5, hit.row)
	if got := m.Input(); got != "/config " {
		t.Fatalf("input after clicking the third candidate = %q, want \"/config \"", got)
	}
	if got := m.TakeSubmitted(); got != "" {
		t.Fatalf("clicking a candidate submitted %q", got)
	}
	if m.completionOverlay {
		t.Fatal("the overlay stayed open after accepting a candidate")
	}
}

func TestCompletionMissStillStartsTextSelection(t *testing.T) {
	m := overlayModel()
	m.Frame(100, 24)
	// 状态行不属于浮层候选。
	m.MouseDown(2, 23)
	if !m.SelectionActive() {
		t.Fatal("a click outside the overlay must start a text selection")
	}
	if got := m.Input(); got != "/" {
		t.Fatalf("a missed click changed the input to %q", got)
	}
}

func TestCompletionOverlayNeedsCommandInfoAndCandidates(t *testing.T) {
	plain := New(Config{})
	typeCommandText(plain, "/")
	if plain.completionOverlay || (plain.completion != nil && plain.completion.Count() != 0) {
		t.Fatal("without CommandInfo there must be no overlay or candidates")
	}

	unmatched := New(Config{})
	unmatched.SetCommandInfo(commandInfos())
	typeCommandText(unmatched, "/zzz")
	if unmatched.completionOverlay {
		t.Fatal("no candidates must not produce an overlay")
	}
	if got := frameBytes(unmatched, 100, 24); strings.Contains(got, "/commit") {
		t.Fatalf("an unmatched query leaked into the frame: %q", got)
	}
}

func TestCompletionSelectClamps(t *testing.T) {
	s := NewCompletionState(sampleCandidates())
	for _, tc := range []struct{ in, want int }{{-5, 0}, {2, 2}, {999, 4}} {
		s.Select(tc.in)
		if s.index != tc.want {
			t.Fatalf("Select(%d) index = %d, want %d", tc.in, s.index, tc.want)
		}
		if _, ok := s.Current(); !ok {
			t.Fatalf("Select(%d) left no current candidate", tc.in)
		}
	}
}
