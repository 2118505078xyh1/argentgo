package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStripCursorControlRemovesCursorAndEraseSequences(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{
			name: "cursor movement and erase",
			in:   "\x1b[2J\x1b[1;1H\x1b[31mred\x1b[0m\x1b[Ktail",
			want: "\x1b[31mred\x1b[0mtail",
		},
		{
			name: "all cursor final bytes",
			in:   "a\x1b[Ab\x1b[Bc\x1b[Cd\x1b[De\x1b[Ff\x1b[Gg\x1b[Hh\x1b[fi",
			want: "abcdefghi",
		},
		{
			name: "erase scroll and save restore",
			in:   "a\x1b[Jb\x1b[Kc\x1b[Sd\x1b[Te\x1b[sf\x1b[ug",
			want: "abcdefg",
		},
		{
			name: "mode set reset including DEC private prefix",
			in:   "\x1b[?25lhidden\x1b[?25h\x1b[?1049halt\x1b[?1049l\x1b[1mshown\x1b[0m",
			want: "hiddenalt\x1b[1mshown\x1b[0m",
		},
		{
			name: "ESC save restore cursor",
			in:   "a\x1b7b\x1b8c",
			want: "abc",
		},
		{
			name: "no escape byte is byte identical",
			in:   "汉字😀 no controls\ttab\nnext",
			want: "汉字😀 no controls\ttab\nnext",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripCursorControl(tc.in); got != tc.want {
				t.Fatalf("stripCursorControl(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestStripCursorControlKeepsColorOSCAndUnknownSequences(t *testing.T) {
	// OSC 8 hyperlinks use BEL or ST; SGR uses 'm'; device-status queries use
	// an unknown final byte and must pass through rather than lose data.
	for _, tc := range []struct {
		name, in, want string
	}{
		{
			name: "SGR colours and styles",
			in:   "\x1b[1;31mbold red\x1b[0m \x1b[38;2;1;2;3mrgb\x1b[m",
			want: "\x1b[1;31mbold red\x1b[0m \x1b[38;2;1;2;3mrgb\x1b[m",
		},
		{
			name: "OSC hyperlink with BEL",
			in:   "\x1b]8;;https://example.com\x07link\x1b]8;;\x07 end",
			want: "\x1b]8;;https://example.com\x07link\x1b]8;;\x07 end",
		},
		{
			name: "OSC hyperlink with ST",
			in:   "\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\",
			want: "\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\",
		},
		{
			name: "unknown CSI and charset designator pass through",
			in:   "\x1b[6n\x1b(Bascii",
			want: "\x1b[6n\x1b(Bascii",
		},
		{
			name: "truncated sequences are kept",
			in:   "a\x1b[31b\x1b[c\x1b",
			want: "a\x1b[31b\x1b[c\x1b",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripCursorControl(tc.in); got != tc.want {
				t.Fatalf("stripCursorControl(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSimulateCarriageReturnsKeepsLastFrame(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"progress bar", "building 10%\rbuilding 50%\rbuilding 100%", "building 100%"},
		{"single overwrite", "a\rbc", "bc"},
		{"longer final frame", "abc\rd", "d"},
		{"leading CR", "\rstart", "start"},
		{"trailing CR", "done\r", ""},
		{"multiple lines", "x\ra\ny\rb\nz", "a\nb\nz"},
		{"trailing newline preserved", "a\rb\n", "b\n"},
		{"no CR is byte identical", "汉字😀\nplain", "汉字😀\nplain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := simulateCarriageReturns(tc.in); got != tc.want {
				t.Fatalf("simulateCarriageReturns(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeOutputSpaceExpandsTabsAndCRLF(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"CRLF", "a\r\nb\r\n", "a\nb\n"},
		{"inline tab", "a\tb", "a    b"},
		{"leading indentation preserved", "\t\tcode", "        code"},
		{"mixed", "\r\n\tx\r\n", "\n    x\n"},
		{"bare CR untouched", "a\rb", "a\rb"},
		{"no controls is byte identical", "汉字😀 中文", "汉字😀 中文"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeOutputSpace(tc.in); got != tc.want {
				t.Fatalf("normalizeOutputSpace(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeToolOutputCargoProgress(t *testing.T) {
	// A cargo-style build: two erased progress frames, then a coloured
	// "Finished" line. Only the final frame of the progress row may survive.
	raw := "   Compiling demo v0.1.0\r\x1b[2K   Compiling demo v0.1.0: 25%\r" +
		"\x1b[2K   Compiling demo v0.1.0: 100%\n" +
		"\x1b[32m    Finished\x1b[0m dev [unoptimized]\n"
	want := "   Compiling demo v0.1.0: 100%\n" +
		"\x1b[32m    Finished\x1b[0m dev [unoptimized]\n"
	got := normalizeToolOutput(raw)
	if got != want {
		t.Fatalf("normalizeToolOutput() = %q, want %q", got, want)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("output is not valid UTF-8: %q", got)
	}
}

func TestNormalizeToolOutputKeepsCJKAndEmoji(t *testing.T) {
	raw := "进度 汉😀 50%\r完成 ✅\nsecond 中文行"
	want := "完成 ✅\nsecond 中文行"
	if got := normalizeToolOutput(raw); got != want {
		t.Fatalf("normalizeToolOutput() = %q, want %q", got, want)
	}
}

func TestNormalizeToolOutputIdenticalWithoutControls(t *testing.T) {
	// No ESC, CR or TAB: every byte must survive unchanged, including a final
	// newline and multi-byte runes.
	for _, raw := range []string{"", "plain text", "汉字😀\nsecond 中文\n", "a\nb\nc"} {
		if got := normalizeToolOutput(raw); got != raw {
			t.Fatalf("normalizeToolOutput(%q) = %q, want byte-identical input", raw, got)
		}
	}
}

func TestNormalizeToolOutputHandlesFullANSISequence(t *testing.T) {
	// A git-style status line: SGR + OSC link survive, cursor erase is dropped
	// and the bare CR keeps only the final frame.
	raw := "\x1b[?25l\x1b[2K\rremote: Counting objects  1%\r" +
		"\x1b[2Kremote: Counting objects 100%\x1b[?25h\n" +
		"\x1b]8;;https://example.com/commit\x07done\x1b]8;;\x07\n"
	want := "remote: Counting objects 100%\n" +
		"\x1b]8;;https://example.com/commit\x07done\x1b]8;;\x07\n"
	if got := normalizeToolOutput(raw); got != want {
		t.Fatalf("normalizeToolOutput() = %q, want %q", got, want)
	}
}

func TestStructuredToolResultsSanitisesCursorControl(t *testing.T) {
	p, _ := paletteFor(ThemeDark)
	entry := Entry{
		Kind:   EntryTool,
		Name:   "shell",
		Args:   `{}`,
		Output: "\r\x1b[2Kbuilding 50%\r\x1b[2Kbuild done\n",
	}
	withStructuredToolResults(t, true, func() {
		got := lineTexts(toolLines(entry, 60, false, p))
		want := strings.Join([]string{"● shell()", "  build done"}, "\n")
		if got != want {
			t.Fatalf("structured preview = %q, want %q", got, want)
		}
	})
}

func TestStructuredToolResultsKeepsSGRColor(t *testing.T) {
	p, _ := paletteFor(ThemeDark)
	entry := Entry{
		Kind:   EntryTool,
		Name:   "shell",
		Args:   `{}`,
		Output: "\x1b[2J\x1b[31mred\x1b[0m\n",
	}
	withStructuredToolResults(t, true, func() {
		got := lineTexts(toolLines(entry, 60, false, p))
		if !strings.Contains(got, "\x1b[31mred\x1b[0m") {
			t.Fatalf("SGR colour was stripped: %q", got)
		}
		if n := strings.Count(got, "\x1b["); n != 2 {
			t.Fatalf("unexpected CSI count %d in %q", n, got)
		}
	})
}
