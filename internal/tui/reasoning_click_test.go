package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func reasoningClickModel(text string) *Model {
	m := New(Config{})
	m.AppendReasoning(text)
	return m
}

func TestReasoningCollapsedDefaultsToOneSummaryLine(t *testing.T) {
	raw := "FIRST-LINE-MARKER parser state." + "\n" +
		"SECOND-LINE-MARKER inspect the migration." + "\n" +
		"THIRD-LINE-MARKER run the tests."
	m := reasoningClickModel(raw)
	buf := m.Frame(100, 24)
	var rows []string
	for y := 0; y < buf.Height(); y++ {
		if row := buf.RowText(y); strings.Contains(row, reasonMark) {
			rows = append(rows, row)
		}
	}
	if len(rows) != 1 {
		t.Fatalf("collapsed reasoning rows = %v, want one", rows)
	}
	text := frameText(m, 100, 24)
	for _, hidden := range []string{"SECOND-LINE-MARKER", "THIRD-LINE-MARKER"} {
		if strings.Contains(text, hidden) {
			t.Fatalf("collapsed frame leaked %q:\n%s", hidden, text)
		}
	}
	if len(m.reasoningHits) != 1 || m.reasoningHits[0].bottom-m.reasoningHits[0].top != 1 {
		t.Fatalf("collapsed hit ranges = %+v, want a single row", m.reasoningHits)
	}
}

func TestReasoningClickExpandsFullTextAndCollapsesOnAnyRow(t *testing.T) {
	raw := "FIRST-LINE-MARKER parser state." + "\n" +
		"SECOND-LINE-MARKER inspect the migration." + "\n" +
		"THIRD-LINE-MARKER run the tests."
	m := reasoningClickModel(raw)
	m.Frame(100, 24)
	hit := m.reasoningHits[0]

	// 点击折叠摘要 → 展开全部思考行。
	m.MouseDown(3, hit.top)
	if !m.reasoningExpanded[0] {
		t.Fatal("clicking the collapsed summary did not expand it")
	}
	if m.SelectionActive() {
		t.Fatal("clicking a reasoning line must not start a text selection")
	}
	expanded := frameText(m, 100, 24)
	for _, want := range []string{"SECOND-LINE-MARKER", "THIRD-LINE-MARKER"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expanded frame is missing %q:\n%s", want, expanded)
		}
	}
	m.Frame(100, 24)
	expandedHit := m.reasoningHits[0]
	if expandedHit.bottom-expandedHit.top < 4 {
		t.Fatalf("expanded hit range = %+v, want at least 4 rows", expandedHit)
	}

	// 点击展开后的任意一行（最后一行）→ 折叠回去。
	m.MouseDown(3, expandedHit.bottom-1)
	if m.reasoningExpanded[0] {
		t.Fatal("clicking an expanded reasoning row did not collapse it")
	}
	collapsed := frameText(m, 100, 24)
	if strings.Contains(collapsed, "SECOND-LINE-MARKER") {
		t.Fatalf("collapsed frame still shows full reasoning:\n%s", collapsed)
	}
}

func TestReasoningExpandedCJKClipsValidUTF8(t *testing.T) {
	raw := strings.Repeat("我们需要仔细检查解析器。", 20) + "\n" +
		strings.Repeat("然后再修复失败的测试。", 20)
	m := reasoningClickModel(raw)
	m.Frame(60, 24)
	m.MouseDown(3, m.reasoningHits[0].top)

	const width = 60
	buf := m.Frame(width, 24)
	found := false
	for y := 0; y < buf.Height(); y++ {
		row := buf.RowText(y)
		if !utf8.ValidString(row) {
			t.Fatalf("row %d is invalid UTF-8: %q", y, row)
		}
		if w := TextWidth(row); w > width {
			t.Fatalf("row %d width = %d, want <= %d", y, w, width)
		}
		if strings.Contains(row, reasonMark) {
			found = true
		}
	}
	if !found {
		t.Fatal("the summary header vanished after expanding CJK reasoning")
	}
	if n := m.reasoningHits[0].bottom - m.reasoningHits[0].top; n < 3 {
		t.Fatalf("CJK reasoning did not expand to multiple rows: %+v", m.reasoningHits)
	}
}

func TestReasoningMissStillStartsTextSelection(t *testing.T) {
	m := reasoningClickModel("some reasoning.\nmore reasoning.")
	m.Frame(100, 24)
	// hintY 在输入框下方，不属于任何思考块。
	m.MouseDown(2, 22)
	if !m.SelectionActive() {
		t.Fatal("a click outside the reasoning block must start a text selection")
	}
	if len(m.reasoningExpanded) != 0 {
		t.Fatalf("a missed click changed expansion state: %+v", m.reasoningExpanded)
	}
}
