package tui

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestShortHelpShowsAllBindings(t *testing.T) {
	bindings := []KeyBinding{
		{Key: "enter", Desc: "send", Enabled: true},
		{Key: "alt+enter", Desc: "newline", Enabled: true},
		{Key: "ctrl+c", Desc: "cancel", Enabled: true},
		{Key: "/help", Enabled: true},
	}
	want := "enter send · alt+enter newline · ctrl+c cancel · /help"
	got := ShortHelp(bindings, 200, TextWidth)
	if got != want {
		t.Fatalf("ShortHelp() = %q, want %q", got, want)
	}
	if w := TextWidth(got); w > 200 {
		t.Fatalf("ShortHelp() width = %d, want <= 200", w)
	}
}

func TestShortHelpNarrowAppendsEllipsisWithoutHalfKeys(t *testing.T) {
	bindings := []KeyBinding{
		{Key: "enter", Desc: "send", Enabled: true},
		{Key: "alt+enter", Desc: "newline", Enabled: true},
		{Key: "ctrl+c", Desc: "cancel", Enabled: true},
	}
	full := "enter send · alt+enter newline · ctrl+c cancel"
	for _, width := range []int{1, 8, 12, 20, 30, 45} {
		got := ShortHelp(bindings, width, TextWidth)
		if !strings.HasSuffix(got, "…") {
			t.Errorf("width %d: ShortHelp() = %q, want a trailing ellipsis", width, got)
		}
		if w := TextWidth(got); w > width {
			t.Errorf("width %d: ShortHelp() width = %d (%q), want <= %d", width, w, got, width)
		}
		body := strings.TrimSuffix(got, "…")
		if body != "" && !strings.HasPrefix(full, body) {
			t.Errorf("width %d: shown text %q is not a prefix of %q", width, body, full)
		}
		if strings.HasSuffix(body, " ") || strings.HasSuffix(body, "·") {
			t.Errorf("width %d: shown text %q ends mid-separator", width, body)
		}
	}

	// 单个放不下的条目也只产生省略号，绝不会是
	// 半个写出的按键。
	got := ShortHelp([]KeyBinding{{Key: "alt+enter", Desc: "newline", Enabled: true}}, 6, TextWidth)
	if got != "…" {
		t.Fatalf("ShortHelp() = %q, want %q", got, "…")
	}
}

func TestShortHelpSkipsDisabledBindings(t *testing.T) {
	bindings := []KeyBinding{
		{Key: "enter", Desc: "send", Enabled: true},
		{Key: "secret", Desc: "hidden", Enabled: false},
		{Key: "ctrl+c", Desc: "cancel", Enabled: true},
	}
	want := "enter send · ctrl+c cancel"
	got := ShortHelp(bindings, 200, TextWidth)
	if got != want {
		t.Fatalf("ShortHelp() = %q, want %q", got, want)
	}
	if strings.Contains(got, "secret") || strings.Contains(got, "hidden") {
		t.Fatalf("disabled binding leaked into %q", got)
	}
	narrow := ShortHelp(bindings, 18, TextWidth)
	if strings.Contains(narrow, "secret") {
		t.Fatalf("disabled binding leaked into narrow output %q", narrow)
	}

	// 禁用所有条目等同于没有任何绑定。
	allOff := []KeyBinding{
		{Key: "enter", Desc: "send"},
		{Key: "ctrl+c", Desc: "cancel"},
	}
	if got := ShortHelp(allOff, 200, TextWidth); got != "" {
		t.Fatalf("ShortHelp() with all entries disabled = %q, want empty", got)
	}
}

func TestShortHelpCJKWidth(t *testing.T) {
	bindings := []KeyBinding{
		{Key: "enter", Desc: "发送", Enabled: true},
		{Key: "alt+enter", Desc: "换行", Enabled: true},
		{Key: "ctrl+c", Desc: "取消", Enabled: true},
	}
	full := ShortHelp(bindings, 1000, TextWidth)
	want := "enter 发送 · alt+enter 换行 · ctrl+c 取消"
	if full != want {
		t.Fatalf("ShortHelp() = %q, want %q", full, want)
	}
	// TextWidth 把每个 CJK 字形算作两个单元格；按 rune 计数会是 35。
	if got := TextWidth(full); got != 41 {
		t.Fatalf("TextWidth(%q) = %d, want 41", full, got)
	}

	got := ShortHelp(bindings, 16, TextWidth)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("narrow CJK ShortHelp() = %q, want a trailing ellipsis", got)
	}
	if w := TextWidth(got); w > 16 {
		t.Fatalf("narrow CJK ShortHelp() width = %d (%q), want <= 16", w, got)
	}
	if body := strings.TrimSuffix(got, "…"); body != "enter 发送" {
		t.Fatalf("narrow CJK ShortHelp() shown text = %q, want %q", body, "enter 发送")
	}
}

func TestShortHelpEmptyBindings(t *testing.T) {
	cases := map[string][]KeyBinding{
		"nil":           nil,
		"empty":         []KeyBinding{},
		"empty entries": {{}, {Key: "", Desc: "", Enabled: true}},
	}
	for name, bindings := range cases {
		for _, width := range []int{-1, 0, 40} {
			if got := ShortHelp(bindings, width, TextWidth); got != "" {
				t.Errorf("%s at width %d: ShortHelp() = %q, want empty", name, width, got)
			}
		}
	}
}

func TestShortHelpDeterministicAndOrdered(t *testing.T) {
	bindings := []KeyBinding{
		{Key: "enter", Desc: "send", Enabled: true},
		{Key: "ctrl+c", Desc: "cancel", Enabled: true},
		{Key: "/help", Enabled: true},
	}
	want := "enter send · ctrl+c cancel · /help"
	first := ShortHelp(bindings, 200, TextWidth)
	if first != want {
		t.Fatalf("ShortHelp() = %q, want %q", first, want)
	}
	for i := 0; i < 100; i++ {
		if got := ShortHelp(bindings, 200, TextWidth); got != first {
			t.Fatalf("call %d = %q, want the deterministic result %q", i, got, first)
		}
	}

	reversed := []KeyBinding{
		{Key: "/help", Enabled: true},
		{Key: "ctrl+c", Desc: "cancel", Enabled: true},
		{Key: "enter", Desc: "send", Enabled: true},
	}
	if got := ShortHelp(reversed, 200, TextWidth); got != "/help · ctrl+c cancel · enter send" {
		t.Fatalf("binding order not preserved: %q", got)
	}
}

func TestShortHelpUsesInjectedTextWidth(t *testing.T) {
	// 把每个 rune 算作两个单元格：在这个模型下，即使 "a one" 单独也
	// 放不下，因此只显示省略号。默认的 TextWidth 模型
	// 在相同宽度下会显示 "a one…"。
	double := func(s string) int { return 2 * len([]rune(s)) }
	bindings := []KeyBinding{
		{Key: "a", Desc: "one", Enabled: true},
		{Key: "b", Desc: "two", Enabled: true},
	}
	if got := ShortHelp(bindings, 8, double); got != "…" {
		t.Fatalf("ShortHelp() with injected width = %q, want %q", got, "…")
	}
	if got := ShortHelp(bindings, 8, nil); got != "a one…" {
		t.Fatalf("ShortHelp() with default width = %q, want %q", got, "a one…")
	}
}

func TestHintBindingsParsesLocalisedHints(t *testing.T) {
	cases := []struct {
		name string
		hint string
		want []KeyBinding
	}{
		{
			name: "english",
			hint: "enter send · alt+enter newline · /help",
			want: []KeyBinding{
				{Key: "enter", Desc: "send", Enabled: true},
				{Key: "alt+enter", Desc: "newline", Enabled: true},
				{Key: "/help", Enabled: true},
			},
		},
		{
			name: "chinese modal",
			hint: "y 允许 · n 拒绝 · a 仅本次调用允许 · esc 拒绝",
			want: []KeyBinding{
				{Key: "y", Desc: "允许", Enabled: true},
				{Key: "n", Desc: "拒绝", Enabled: true},
				{Key: "a", Desc: "仅本次调用允许", Enabled: true},
				{Key: "esc", Desc: "拒绝", Enabled: true},
			},
		},
		{name: "empty", hint: "", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := HintBindings(tc.hint)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("HintBindings(%q) = %#v, want %#v", tc.hint, got, tc.want)
			}
		})
	}
}

func TestHintBindingsRoundTripsExistingLocaleHints(t *testing.T) {
	for _, locale := range []string{"en", "zh"} {
		for _, key := range []string{"hint", "hint_modal"} {
			hint := text(locale, key)
			got := ShortHelp(HintBindings(hint), 1000, TextWidth)
			if got != hint {
				t.Errorf("locale %s/%s: round trip = %q, want %q", locale, key, got, hint)
			}
		}
	}
}

// helpTestAction 是一个空的动作钩子，仅用于让 helpStateEnabled 看到"已接线"。
func helpTestAction(context.Context, string) error { return nil }

func TestHelpStateEnabled(t *testing.T) {
	bindings := []KeyBinding{
		{Key: "enter", Desc: "sends"},
		{Key: "alt+u", Desc: "undo"},
		{Key: "alt+r", Desc: "retry"},
	}
	m := New(Config{})
	helpStateEnabled(m, bindings)
	if !bindings[0].Enabled {
		t.Fatal("无状态按键应当始终可见")
	}
	if bindings[1].Enabled || bindings[2].Enabled {
		t.Fatalf("钩子未接线时撤销/重试应隐藏，得到 %+v", bindings)
	}
	m.SetTurnActions(helpTestAction, helpTestAction, nil)
	helpStateEnabled(m, bindings)
	if !bindings[1].Enabled || !bindings[2].Enabled {
		t.Fatalf("钩子接线后撤销/重试应可见，得到 %+v", bindings)
	}
}

func TestHelpNoticeLinesNarrowKeepsWholeBindings(t *testing.T) {
	bindings := []KeyBinding{
		{Key: "alt+enter", Desc: "newline", Enabled: true},
		{Key: "ctrl+c", Desc: "cancel", Enabled: true},
	}
	// 单个条目都放不下时只能得到省略号，绝不会是 "alt+ente" 这种半个键。
	if got := helpNoticeLines(bindings, 6); !reflect.DeepEqual(got, []string{"…", "…"}) {
		t.Fatalf("narrow help lines = %q, want %q", got, []string{"…", "…"})
	}
	// 放得下单个但放不下两个时，每条各占一行且键保持完整。
	if got := helpNoticeLines(bindings, 18); !reflect.DeepEqual(got, []string{"alt+enter newline", "ctrl+c cancel"}) {
		t.Fatalf("wrapped help lines = %q, want two whole bindings", got)
	}
}

func TestHelpNoticeLinesCJK(t *testing.T) {
	bindings := []KeyBinding{
		{Key: "enter", Desc: "发送", Enabled: true},
		{Key: "alt+enter", Desc: "换行", Enabled: true},
		{Key: "ctrl+c", Desc: "取消", Enabled: true},
	}
	for _, width := range []int{8, 12, 16, 40} {
		lines := helpNoticeLines(bindings, width)
		if len(lines) < 2 {
			t.Fatalf("width %d: got %d lines, want >= 2", width, len(lines))
		}
		for _, line := range lines {
			if w := TextWidth(line); w > width {
				t.Errorf("width %d: line %q spans %d cells", width, line, w)
			}
		}
	}
}

func TestHelpCommandEmitsStructuredNotices(t *testing.T) {
	for _, locale := range []string{"en", "zh"} {
		m := New(Config{Locale: locale})
		m.Frame(100, 24) // 确定正文宽度
		m.mu.Lock()
		m.commandLocked("/help")
		m.mu.Unlock()

		var lines []string
		for _, entry := range m.entries {
			if entry.Kind != EntryNotice {
				continue
			}
			if w := TextWidth(entry.Text); w > 100 {
				t.Errorf("locale %s: notice %q spans %d cells, want <= 100", locale, entry.Text, w)
			}
			lines = append(lines, entry.Text)
		}
		if len(lines) < 2 {
			t.Fatalf("locale %s: /help produced %d notice lines, want >= 2", locale, len(lines))
		}
	}
}
