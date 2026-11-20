package security

import "testing"

// TestWildcardMatchMalformedPatternNeverMatches guards the documented contract
// that a malformed pattern never matches. The '*' prefix fallback used to run
// even when path.Match reported ErrBadPattern, so a typo like "[*" still
// matched every subject starting with "[" - silently turning a rule into an
// unintended allow.
func TestWildcardMatchMalformedPatternNeverMatches(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		subject string
	}{
		{"bad class with trailing star", "[*", "[anything"},
		{"bad class inside pattern with trailing star", "admin[*-*", "admin[*-host"},
		{"bad class matching the literal prefix", "[*", "["},
		{"unclosed class with trailing star", "a[?*", "a[?host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WildcardMatch(tc.pattern, tc.subject); got {
				t.Errorf("WildcardMatch(%q, %q) = true, want false for a malformed pattern", tc.pattern, tc.subject)
			}
		})
	}
}

// TestWildcardMatchWindowsSeparators verifies that rules written with Windows
// path separators match the paths they name. path.Match treats '\' as an escape
// on every platform, so without normalisation an exact rule like
// "C:\Windows\System32" did not even match the identical subject, and a
// "dir\file?.txt" rule could never match "dir\file1.txt".
func TestWildcardMatchWindowsSeparators(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		subject string
		want    bool
	}{
		{"exact path matches itself", `C:\Windows\System32`, `C:\Windows\System32`, true},
		{"star within a windows segment", `dir\file?.txt`, `dir\file1.txt`, true},
		{"star suffix crosses windows separator", `read\*`, `read\file.txt`, true},
		{"forward slash pattern matches backslash subject", `read/*`, `read\file.txt`, true},
		{"backslash pattern matches forward slash subject", `read\*`, `read/file.txt`, true},
		{"non-matching windows path stays non-matching", `C:\Windows\*`, `D:\Data\file`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WildcardMatch(tc.pattern, tc.subject); got != tc.want {
				t.Errorf("WildcardMatch(%q, %q) = %v, want %v", tc.pattern, tc.subject, got, tc.want)
			}
		})
	}
}

// TestWildcardMatchValidPatternsUnchanged is a regression guard: the malformed
// and platform fixes must not alter the behaviour of well-formed patterns,
// including the trailing-'*' prefix fallback that crosses separators.
func TestWildcardMatchValidPatternsUnchanged(t *testing.T) {
	cases := []struct {
		pattern string
		subject string
		want    bool
	}{
		{"read", "read", true},
		{"read", "readme", false},
		{"read*", "readme", true},
		{"read*", "read/file.txt", true},
		{"git push*", "git push --force", true},
		{"src/*.go", "src/main.go", true},
		{"src/*.go", "src/sub/main.go", false},
		{"**", "a/b/c", true},
		{"file[12].txt", "file2.txt", true},
		{"file?.txt", "file1.txt", true},
		{"*", "", true},
	}
	for _, tc := range cases {
		if got := WildcardMatch(tc.pattern, tc.subject); got != tc.want {
			t.Errorf("WildcardMatch(%q, %q) = %v, want %v", tc.pattern, tc.subject, got, tc.want)
		}
	}
}
