package tui

import (
	"fmt"
	"strings"
	"testing"
)

// overlayCandidates 返回当前补全列表（TUI 原生命令 + 宿主注入命令，按字典序）。
func overlayCandidates(m *Model) []CompletionCandidate { return m.visibleCommands() }

// candidateName 返回第 i 个候选的命令名，越界时让测试立即失败。
func candidateName(t *testing.T, m *Model, i int) string {
	t.Helper()
	cands := overlayCandidates(m)
	if i < 0 || i >= len(cands) {
		t.Fatalf("candidate %d is out of range (%d total)", i, len(cands))
	}
	return cands[i].Command
}

func counterLabel(selected, total int) string { return fmt.Sprintf("(%d/%d)", selected, total) }

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
	first := candidateName(t, m, 0)
	total := len(overlayCandidates(m))
	row, ok := overlayRow(m, 100, 24, "/"+first)
	if !ok {
		t.Fatalf("the floating candidate /%s is missing", first)
	}
	text := m.Frame(100, 24).RowText(row)
	if !strings.HasPrefix(text, "│ > ") {
		t.Fatalf("the selected candidate must use '> ', got %q", text)
	}
	if !overlayCellReverse(m, 100, 24, row, 5) {
		t.Fatalf("the selected row must be highlighted in reverse: %q", text)
	}
	if _, ok := overlayRow(m, 100, 24, counterLabel(1, total)); !ok {
		t.Fatalf("the %s counter line is missing", counterLabel(1, total))
	}
	// 侧栏仍由 Alt+C 控制，且复用同一条候选行与计数行。
	m.HandleKey(Key{Kind: KindAltC})
	panel := panelText(m, 100, 24)
	for _, want := range []string{"COMMANDS", "> /" + first, counterLabel(1, total)} {
		if !strings.Contains(panel, want) {
			t.Fatalf("side panel is missing %q: %s", want, panel)
		}
	}
}

func TestCompletionOverlayDownMovesCounterAndHighlight(t *testing.T) {
	m := overlayModel()
	first, second := candidateName(t, m, 0), candidateName(t, m, 1)
	total := len(overlayCandidates(m))
	m.HandleKey(Key{Kind: KindDown})
	if _, ok := overlayRow(m, 100, 24, counterLabel(2, total)); !ok {
		t.Fatalf("Down did not update the counter to %s", counterLabel(2, total))
	}
	secondRow, ok := overlayRow(m, 100, 24, "/"+second)
	if !ok {
		t.Fatalf("the second candidate /%s is missing", second)
	}
	firstRow, _ := overlayRow(m, 100, 24, "/"+first)
	if !overlayCellReverse(m, 100, 24, secondRow, 5) {
		t.Fatal("the second row is not highlighted after Down")
	}
	if overlayCellReverse(m, 100, 24, firstRow, 5) {
		t.Fatal("the first row stayed highlighted after Down")
	}
}

func TestCompletionOverlayClickAcceptsThirdCandidate(t *testing.T) {
	m := overlayModel()
	third := candidateName(t, m, 2)
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
	if want := "/" + third + " "; m.Input() != want {
		t.Fatalf("input after clicking the third candidate = %q, want %q", m.Input(), want)
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
