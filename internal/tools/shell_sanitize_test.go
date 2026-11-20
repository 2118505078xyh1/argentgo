package tools

import (
	"strings"
	"testing"
)

func TestSanitizeShellOutput(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "colored vite build log",
			in:   "\x1b[36mvite v6.4.3\x1b[39m \x1b[32m✓\x1b[39m built\n",
			want: "vite v6.4.3 ✓ built\n",
		},
		{
			name: "vite style simple color",
			in:   "\x1b[36mfoo\x1b[0m",
			want: "foo",
		},
		{
			name: "consecutive CSI sequences",
			in:   "a\x1b[1m\x1b[2mX\x1b[22m\x1b[22mb",
			want: "aXb",
		},
		{
			name: "OSC title terminated by BEL",
			in:   "a\x1b]0;title\x07b",
			want: "ab",
		},
		{
			name: "OSC hyperlink terminated by ESC backslash",
			in:   "a\x1b]8;;http://x\x1b\\b",
			want: "ab",
		},
		{
			name: "CRLF becomes LF",
			in:   "a\r\nb",
			want: "a\nb",
		},
		{
			// CR now overwrites the line instead of being silently dropped.
			name: "bare CR overwrites earlier line content",
			in:   "a\r\nb\rc",
			want: "a\nc",
		},
		{
			name: "interlinear annotation chars",
			in:   "a\ufff9b\ufffac\ufffbd",
			want: "abcd",
		},
		{
			name: "other C0 controls dropped but TAB and LF kept",
			in:   "a\x01b\tc\nd\x0be",
			want: "ab\tc\nde",
		},
		{
			name: "bare ESC at end",
			in:   "hello\x1b",
			want: "hello",
		},
		{
			name: "bare ESC swallows one byte",
			in:   "a\x1bXb",
			want: "ab",
		},
		{
			name: "charset selection",
			in:   "a\x1b(Bb\x1b)0c",
			want: "abc",
		},
		{
			name: "TAB preserved",
			in:   "col1\tcol2\tcol3",
			want: "col1\tcol2\tcol3",
		},
		{
			name: "CJK unchanged",
			in:   "你好，世界！日本語テスト한국어",
			want: "你好，世界！日本語テスト한국어",
		},
		{
			name: "truncated CSI at end is consumed without panic",
			in:   "before\x1b[38;2;255;0;",
			want: "before",
		},
		{
			name: "truncated OSC at end is consumed without panic",
			in:   "before\x1b]0;unfinished title",
			want: "before",
		},
		{
			name: "truncated charset at end is consumed without panic",
			in:   "before\x1b(",
			want: "before",
		},
		{
			name: "empty input",
			in:   "",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeShellOutput(tc.in); got != tc.want {
				t.Fatalf("sanitizeShellOutput(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeShellOutputKeepsTextAroundTruncatedSequence(t *testing.T) {
	// A truncated sequence at the very end must not corrupt text that came
	// before it, nor run past the end of the string.
	in := "ok\x1b[31m-red\x1b[0m-done\x1b[9"
	want := "ok-red-done"
	if got := sanitizeShellOutput(in); got != want {
		t.Fatalf("sanitizeShellOutput(%q) = %q, want %q", in, got, want)
	}
}

func TestProcessCarriageReturnsKeepsLastFrame(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "progress frames collapse to the last one",
			in:   "Downloading 10%\rDownloading 50%\rDownloading 100%",
			want: "Downloading 100%",
		},
		{
			name: "equal length frame fully overwrites",
			in:   "progress: 10%\rprogress: 20%",
			want: "progress: 20%",
		},
		{
			name: "shorter frame overwrites prefix only",
			in:   "abc\rx",
			want: "xbc",
		},
		{
			name: "CR is scoped to its own line",
			in:   "line1\rline2\nplain",
			want: "line2\nplain",
		},
		{
			name: "overwrite does not split CJK runes",
			in:   "你好世界\rAB",
			want: "AB世界",
		},
		{
			name: "overwrite does not split emoji runes",
			in:   "🙂🙂\rAB",
			want: "AB",
		},
		{
			name: "no carriage return is returned unchanged",
			in:   "plain text",
			want: "plain text",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := processCarriageReturns(tc.in); got != tc.want {
				t.Fatalf("processCarriageReturns(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestProcessBackspacesDeletesRunes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "basic deletion", in: "abc\b\bxy", want: "axy"},
		{name: "leading backspaces are no-ops", in: "\b\babc", want: "abc"},
		{name: "backspaces with newlines", in: "abc\b\n123\b\b", want: "ab\n1"},
		{name: "consecutive backspaces", in: "abcdef\b\b\b\bxy", want: "abxy"},
		{name: "backspaces at end", in: "abc\b\b", want: "a"},
		{name: "mixed content", in: "abc\bx\byz\b\b123", want: "ab123"},
		{name: "more backspaces than content", in: "abc\b\b\b\b\b\bdef", want: "def"},
		{name: "spinner frames", in: "Loading[\b\b\b\b\b\b\b\bProgress[\b\b\b\b\b\b\b\b\bStatus: \b\b\b\b\b\b\b\bDone!", want: "Done!"},
		{name: "CJK and emoji are deleted as whole runes", in: "你🙂好\b\bX", want: "你X"},
		{name: "no backspace is returned unchanged", in: "plain text", want: "plain text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := processBackspaces(tc.in); got != tc.want {
				t.Fatalf("processBackspaces(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestApplyRunLengthEncodingOnlyCompressesShorterRuns(t *testing.T) {
	longLine := strings.Repeat("x", 100) + "\n"
	longCJKLine := strings.Repeat("进度更新中", 20) + "\n"

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "short repeated lines stay verbatim",
			in:   "a\na\na\n",
			want: "a\na\na\n",
		},
		{
			name: "long repeated lines are compressed",
			in:   longLine + longLine + longLine,
			want: longLine + "<previous line repeated 2 additional times>\n",
		},
		{
			name: "only the repeated group is compressed",
			in:   "start\n" + longLine + longLine + "end\n",
			want: "start\n" + longLine + "<previous line repeated 1 additional times>\nend\n",
		},
		{
			name: "long CJK repeated lines compress by character count",
			in:   longCJKLine + longCJKLine + longCJKLine,
			want: longCJKLine + "<previous line repeated 2 additional times>\n",
		},
		{
			name: "single line is untouched",
			in:   "only one line\n",
			want: "only one line\n",
		},
		{
			name: "empty input",
			in:   "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := applyRunLengthEncoding(tc.in); got != tc.want {
				t.Fatalf("applyRunLengthEncoding(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeShellOutputProgressWithANSI(t *testing.T) {
	// ANSI colour codes occupy no columns, so the CR overwrite must keep only
	// the last frame and the escape bytes must never leak into the result.
	in := "start\n\x1b[32m\rStep 1/3\x1b[0m\rStep 2/3\rStep 3/3 done\n"
	want := "start\nStep 3/3 done\n"
	if got := sanitizeShellOutput(in); got != want {
		t.Fatalf("sanitizeShellOutput(%q) = %q, want %q", in, got, want)
	}
}

func TestSanitizeShellOutputBackspaceWithANSI(t *testing.T) {
	// The backspace is preserved by ANSI stripping and erased while cleaning.
	in := "\x1b[36mabc\b\bxy\x1b[39m"
	want := "axy"
	if got := sanitizeShellOutput(in); got != want {
		t.Fatalf("sanitizeShellOutput(%q) = %q, want %q", in, got, want)
	}
}
