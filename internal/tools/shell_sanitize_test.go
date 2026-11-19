package tools

import "testing"

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
			name: "bare CR is stripped",
			in:   "a\r\nb\rc",
			want: "a\nbc",
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
