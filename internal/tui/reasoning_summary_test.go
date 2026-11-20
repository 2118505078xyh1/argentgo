package tui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func summaryTestPalette(t *testing.T) Palette {
	t.Helper()
	p, err := paletteFor(ThemeDark)
	if err != nil {
		t.Fatalf("paletteFor: %v", err)
	}
	return p
}

func TestReasoningSummaryLineIsAlwaysOneRow(t *testing.T) {
	p := summaryTestPalette(t)
	cases := []struct {
		name string
		text string
	}{
		{"short", "checking the parser"},
		{"long single line", strings.Repeat("we need to inspect the migration carefully ", 200)},
		{"long multiline", strings.Repeat("line of reasoning\n", 300)},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := reasoningSummaryLine(tc.text, 1500*time.Millisecond, 60, true, "en", p)
			if strings.Contains(line.Text(), "\n") {
				t.Fatalf("summary spans more than one row: %q", line.Text())
			}
			if got := line.Width(); got > 60 {
				t.Fatalf("summary width = %d, want <= 60: %q", got, line.Text())
			}
		})
	}

	// 接线复用了同一个函数，因此很长的推理 Entry 仍然
	// 只贡献一条记录行。
	m := New(Config{})
	m.AppendReasoning(strings.Repeat("thinking hard ", 500))
	m.mu.Lock()
	lines := m.reasoningLines(m.entries[0], 60)
	m.mu.Unlock()
	if len(lines) != 1 {
		t.Fatalf("reasoningLines returned %d rows, want 1", len(lines))
	}
}

func TestReasoningSummaryExtractsFirstSentence(t *testing.T) {
	p := summaryTestPalette(t)
	line := reasoningSummaryLine(
		"We need to check the parser. Then we will fix the failing test.",
		time.Second, 80, true, "en", p,
	)
	got := line.Text()
	if !strings.Contains(got, "We need to check the parser") {
		t.Fatalf("first sentence missing: %q", got)
	}
	if strings.Contains(got, "Then we will fix") {
		t.Fatalf("summary leaked past the first sentence: %q", got)
	}
}

func TestReasoningSummaryIgnoresTooShortLead(t *testing.T) {
	p := summaryTestPalette(t)
	line := reasoningSummaryLine(
		"Ok. Need to inspect the database migration carefully before touching it.",
		time.Second, 80, true, "en", p,
	)
	got := line.Text()
	if !strings.Contains(got, "Need to inspect") {
		t.Fatalf("short lead was not skipped: %q", got)
	}
}

func TestReasoningSummaryIsCJKAndUTF8Safe(t *testing.T) {
	p := summaryTestPalette(t)
	short := reasoningSummaryLine("我们需要检查解析器。然后再修复失败的测试。", time.Second, 40, true, "zh", p)
	if !strings.Contains(short.Text(), "我们需要检查解析器") {
		t.Fatalf("CJK first sentence missing: %q", short.Text())
	}
	if !strings.Contains(short.Text(), "思考") {
		t.Fatalf("zh-CN label missing: %q", short.Text())
	}

	long := reasoningSummaryLine(strings.Repeat("思考很长很长的内容", 200), time.Second, 20, true, "zh", p)
	if got := long.Width(); got > 20 {
		t.Fatalf("CJK summary width = %d, want <= 20: %q", got, long.Text())
	}
	if !utf8.ValidString(long.Text()) {
		t.Fatalf("CJK summary contains invalid UTF-8: %q", long.Text())
	}
}

func TestReasoningSummaryFormatsDuration(t *testing.T) {
	p := summaryTestPalette(t)
	cases := []struct {
		elapsed time.Duration
		want    string
	}{
		{250 * time.Millisecond, "250ms"},
		{1500 * time.Millisecond, "1.5s"},
		{2 * time.Minute, "2m"},
	}
	for _, tc := range cases {
		line := reasoningSummaryLine("thinking", tc.elapsed, 80, true, "en", p)
		if !strings.Contains(line.Text(), tc.want) {
			t.Fatalf("elapsed %v: missing %q in %q", tc.elapsed, tc.want, line.Text())
		}
	}
}

func TestReasoningSummaryStates(t *testing.T) {
	p := summaryTestPalette(t)
	live := reasoningSummaryLine("checking the parser", 240*time.Millisecond, 80, false, "en", p)
	done := reasoningSummaryLine("checking the parser", 1500*time.Millisecond, 80, true, "en", p)

	for _, line := range []Line{live, done} {
		if !strings.Contains(line.Text(), "◇ thinking") {
			t.Fatalf("reasoning header missing: %q", line.Text())
		}
		if !strings.Contains(line.Text(), "checking the parser") {
			t.Fatalf("summary missing: %q", line.Text())
		}
	}
	if !strings.Contains(live.Text(), "…") {
		t.Fatalf("in-progress row missing ellipsis: %q", live.Text())
	}
	if spinnerFrame(2) == "" || !strings.ContainsAny(live.Text(), "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		t.Fatalf("in-progress row missing spinner: %q", live.Text())
	}
	if strings.Contains(done.Text(), "…") {
		t.Fatalf("completed row still has ellipsis: %q", done.Text())
	}
	if !strings.Contains(done.Text(), "·") || !strings.Contains(done.Text(), "1.5s") {
		t.Fatalf("completed row missing duration: %q", done.Text())
	}
}

func TestReasoningSummaryDegradesToPlainText(t *testing.T) {
	base, err := paletteFor(ThemeDark)
	if err != nil {
		t.Fatalf("paletteFor: %v", err)
	}
	noColor := base
	noColor.noColor = true
	degraded := monochrome(base)

	for name, p := range map[string]Palette{"no_color": noColor, "degraded": degraded} {
		t.Run(name, func(t *testing.T) {
			line := reasoningSummaryLine("checking the parser", time.Second, 80, true, "en", p)
			for _, span := range line.Spans {
				if span.Style.Fg != (Color{}) || span.Style.Bg != (Color{}) {
					t.Fatalf("%s emitted a colour: %+v", name, span)
				}
				if name == "no_color" && span.Style != (Style{}) {
					t.Fatalf("no_color emitted styling: %+v", span)
				}
			}
		})
	}
}

func TestReasoningLinesReflectsWorkingState(t *testing.T) {
	m := New(Config{Locale: "en"})
	m.AppendReasoning("checking the parser")
	m.mu.Lock()
	m.working = true
	live := m.reasoningLines(m.entries[0], 80)[0].Text()
	m.working = false
	done := m.reasoningLines(m.entries[0], 80)[0].Text()
	m.mu.Unlock()

	if !strings.Contains(live, "…") {
		t.Fatalf("working reasoning should be in progress: %q", live)
	}
	if !strings.Contains(done, "·") {
		t.Fatalf("finished reasoning should show elapsed: %q", done)
	}
}
