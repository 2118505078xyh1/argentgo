package memory

import (
	"testing"
	"unicode/utf8"
)

func TestClipGraphemeBytesRespectsClusterBoundaries(t *testing.T) {
	cases := []struct {
		name string
		s    string
		max  int
		want string
	}{
		{"ascii", "abcdef", 3, "abc"},
		{"combining fits", "e\u0301x", 3, "e\u0301"},
		{"combining does not fit", "e\u0301x", 2, ""},
		{"four byte emoji does not fit", "🙂x", 3, ""},
		{"four byte emoji fits", "🙂x", 4, "🙂"},
		{"flag", "\U0001F1E8\U0001F1F3x", 8, "\U0001F1E8\U0001F1F3"},
		{"zero budget", "abc", 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clipGraphemeBytes(tc.s, tc.max)
			if got != tc.want {
				t.Fatalf("clipGraphemeBytes(%q, %d) = %q, want %q", tc.s, tc.max, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("result %q is not valid UTF-8", got)
			}
		})
	}
}
