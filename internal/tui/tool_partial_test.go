package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func toolPartialPalette(t *testing.T) Palette {
	t.Helper()
	p, err := paletteFor(ThemeDark)
	if err != nil {
		t.Fatalf("paletteFor: %v", err)
	}
	return p
}

func TestPartialArgsSummaryCompleteJSON(t *testing.T) {
	got := partialArgsSummary(`{"path":"a.go","command":"ls -la","timeout":5000,"ok":true,"meta":{"x":1},"list":[1,2]}`)
	want := []argField{
		{Key: "path", Value: "a.go"},
		{Key: "command", Value: "ls -la"},
		{Key: "timeout", Value: "5000"},
		{Key: "ok", Value: "true"},
		{Key: "meta", Value: "{…}"},
		{Key: "list", Value: "[…]"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("partialArgsSummary = %+v, want %+v", got, want)
	}
}

func TestPartialArgsSummaryTruncatedJSON(t *testing.T) {
	cases := []struct {
		name string
		args string
		want []argField
	}{
		{"after_comma", `{"path":"a.go",`, []argField{{Key: "path", Value: "a.go"}}},
		{"partial_key", `{"path":"a.go","comm`, []argField{{Key: "path", Value: "a.go"}}},
		{"partial_string", `{"path":"a.go","command":"ls -l`, []argField{
			{Key: "path", Value: "a.go"}, {Key: "command", Value: "ls -l"},
		}},
		{"after_colon", `{"path":"a.go","cmd":`, []argField{{Key: "path", Value: "a.go"}}},
		{"partial_scalar", `{"n":123`, []argField{{Key: "n", Value: "123"}}},
		{"partial_container", `{"meta":{"x":1`, []argField{{Key: "meta", Value: "{…}"}}},
		{"no_closing_brace", `{"a":1,"b":2`, []argField{
			{Key: "a", Value: "1"}, {Key: "b", Value: "2"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := partialArgsSummary(tc.args); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("partialArgsSummary(%q) = %+v, want %+v", tc.args, got, tc.want)
			}
		})
	}
}

func TestPartialArgsSummaryTruncatedEscape(t *testing.T) {
	cases := []struct {
		name string
		args string
		want []argField
	}{
		{"trailing_backslash", `{"cmd":"abc\`, []argField{{Key: "cmd", Value: "abc"}}},
		{"trailing_unicode", `{"cmd":"\u4e2`, []argField{{Key: "cmd", Value: ""}}},
		{"complete_then_truncated_unicode", `{"cmd":"\u4e2d\u`, []argField{{Key: "cmd", Value: "中"}}},
		{"escapes", `{"cmd":"say \"hi\"\n"}`, []argField{{Key: "cmd", Value: "say \"hi\"\n"}}},
		{"unknown_escape_kept", `{"cmd":"a\q`, []argField{{Key: "cmd", Value: "aq"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := partialArgsSummary(tc.args); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("partialArgsSummary(%q) = %+v, want %+v", tc.args, got, tc.want)
			}
		})
	}
}

func TestPartialArgsSummaryNonObjectAndEmpty(t *testing.T) {
	for _, args := range []string{"", "   ", "[1,2,3]", `"just a string"`, "123", "true", "null", "{", `{"a"}`, `{"a":`} {
		if got := partialArgsSummary(args); len(got) != 0 {
			t.Errorf("partialArgsSummary(%q) = %+v, want no fields", args, got)
		}
	}
}

func TestPartialArgsSummaryCJK(t *testing.T) {
	cases := []struct {
		name string
		args string
		want []argField
	}{
		{"raw", `{"path":"中文.go","query":"搜索"}`, []argField{
			{Key: "path", Value: "中文.go"}, {Key: "query", Value: "搜索"},
		}},
		{"escaped", `{"q":"\u4e2d\u6587"}`, []argField{{Key: "q", Value: "中文"}}},
		{"emoji_surrogate", `{"e":"\ud83d\ude00"}`, []argField{{Key: "e", Value: "😀"}}},
		{"truncated_escaped_cjk", `{"q":"\u4e2d\u65`, []argField{{Key: "q", Value: "中"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := partialArgsSummary(tc.args)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("partialArgsSummary(%q) = %+v, want %+v", tc.args, got, tc.want)
			}
			for _, f := range got {
				if !utf8.ValidString(f.Value) {
					t.Fatalf("value %q is not valid UTF-8", f.Value)
				}
			}
		})
	}
}

func TestFormatPartialArgs(t *testing.T) {
	cases := []struct {
		args string
		want string
	}{
		{`{"path":"a.go","command":"ls"}`, "path=a.go, command=ls"},
		{`{"a":1,"b":2,"c":3,"d":4}`, "a=1, b=2, c=3, …"},
		{`{}`, ""},
		{`{"path":"a.go","cmd`, "path=a.go"},
	}
	for _, tc := range cases {
		if got := formatPartialArgs(tc.args); got != tc.want {
			t.Errorf("formatPartialArgs(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

func TestAdaptiveElapsed(t *testing.T) {
	threshold := 2 * time.Second
	cases := []struct {
		tool, turn, want time.Duration
	}{
		{500 * time.Millisecond, 3 * time.Second, 3 * time.Second},
		{3 * time.Second, 9 * time.Second, 3 * time.Second},
		{2 * time.Second, 9 * time.Second, 2 * time.Second},
	}
	for _, tc := range cases {
		if got := adaptiveElapsed(tc.tool, tc.turn, threshold); got != tc.want {
			t.Errorf("adaptiveElapsed(%v,%v) = %v, want %v", tc.tool, tc.turn, got, tc.want)
		}
	}
}

func TestToolPendingLineStates(t *testing.T) {
	p := toolPartialPalette(t)
	partial := `{"path":"a.go","command":"ls -l`

	live := toolPendingLine("read", partial, 500*time.Millisecond, 3*time.Second, 80, false, false, false, "en", p)
	liveText := live.Text()
	if !strings.Contains(liveText, "◐ ") {
		t.Fatalf("live row missing running glyph: %q", liveText)
	}
	if !strings.Contains(liveText, "read(path=a.go, command=ls -l)") {
		t.Fatalf("live row missing streaming args preview: %q", liveText)
	}
	if !strings.ContainsAny(liveText, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		t.Fatalf("live row missing spinner: %q", liveText)
	}
	if !strings.Contains(liveText, "…") || !strings.Contains(liveText, "3.0s") {
		t.Fatalf("live row should show turn elapsed and ellipsis: %q", liveText)
	}

	done := toolPendingLine("read", `{"path":"a.go"}`, 1500*time.Millisecond, 9*time.Second, 80, true, false, false, "en", p)
	doneText := done.Text()
	if !strings.Contains(doneText, toolMark) || !strings.Contains(doneText, "1.5s") || strings.Contains(doneText, "…") {
		t.Fatalf("done row wrong: %q", doneText)
	}

	failed := toolPendingLine("read", `{"path":"a.go"}`, 900*time.Millisecond, 0, 80, false, true, false, "en", p)
	if text := failed.Text(); !strings.Contains(text, "✖ ") || !strings.Contains(text, "900ms") {
		t.Fatalf("failed row wrong: %q", text)
	}

	cancelled := toolPendingLine("read", partial, time.Second, 0, 80, true, false, true, "zh", p)
	cancelText := cancelled.Text()
	if !strings.Contains(cancelText, toolCanceledMark) || !strings.Contains(cancelText, "已取消") {
		t.Fatalf("cancelled row wrong: %q", cancelText)
	}
	if strings.ContainsAny(cancelText, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		t.Fatalf("cancelled row should not animate: %q", cancelText)
	}

	narrow := toolPendingLine("read", partial, time.Second, time.Second, 20, false, false, false, "en", p)
	if narrow.Width() > 20 {
		t.Fatalf("narrow row width = %d, want <= 20: %q", narrow.Width(), narrow.Text())
	}
}

func TestLiveToolEntryWiresStreamingPreview(t *testing.T) {
	m := New(Config{Locale: "en"})
	fixed := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return fixed }
	m.started = fixed.Add(-3 * time.Second)
	m.tasks = []Task{{
		Name: "read", Status: TaskRunning, Started: fixed.Add(-500 * time.Millisecond),
	}}
	m.entries = []Entry{{
		Kind: EntryTool, Name: "read",
		Args: `{"path":"a.go","command":"ls -l`, Running: true,
	}}

	lines := m.bodyLinesLocked(80)
	var body strings.Builder
	for _, line := range lines {
		body.WriteString(line.Text())
		body.WriteString("\n")
	}
	got := body.String()
	if !strings.Contains(got, "read(path=a.go, command=ls -l)") {
		t.Fatalf("body missing streaming args preview: %q", got)
	}
	if !strings.Contains(got, "3.0s") {
		t.Fatalf("body should use the whole-turn elapsed below threshold: %q", got)
	}

	// A live row must not be served from the cache: advancing the clock must
	// change the rendered duration.
	advanced := fixed.Add(2 * time.Second)
	m.now = func() time.Time { return advanced }
	again := m.bodyLinesLocked(80)
	var body2 strings.Builder
	for _, line := range again {
		body2.WriteString(line.Text())
	}
	// Once the tool's own elapsed passes the 2s threshold the row switches to
	// it (2.5s), proving both the adaptive switch and the cache bypass.
	if !strings.Contains(body2.String(), "2.5s") {
		t.Fatalf("cache served a stale elapsed: %q", body2.String())
	}
}
