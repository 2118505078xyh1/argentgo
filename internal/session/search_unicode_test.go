package session

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSnippetForCaseFoldKeepsRuneAlignment covers session audit #7. Lowercasing
// a lead character such as İ (2→1 bytes), the Kelvin sign (3→1) or ẞ (3→2)
// shortens the byte string, so using the lowered byte offset to slice the
// original value shifts the snippet window. The rune offset is stable because
// strings.ToLower folds rune by rune.
func TestSnippetForCaseFoldKeepsRuneAlignment(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefix string
	}{
		{"dotted-capital-i", "İ"}, // U+0130 -> i: 2 bytes -> 1
		{"kelvin-sign", "\u212a"}, // U+212A -> k: 3 bytes -> 1
		{"capital-sharp-s", "ẞ"},  // U+1E9E -> ß: 3 bytes -> 2
	} {
		t.Run(tc.name, func(t *testing.T) {
			// One folded rune up front, 60 x's, the needle, then enough y's
			// that the 96-rune tail is fully materialised (and ellipsised).
			value := tc.prefix + strings.Repeat("x", 60) + "needle" + strings.Repeat("y", 200)
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			snippet, ok := snippetFor(data, "needle")
			if !ok {
				t.Fatal("snippetFor found no hit")
			}
			want := "…" + strings.Repeat("x", 48) + "needle" + strings.Repeat("y", 96) + "…"
			if snippet != want {
				t.Fatalf("snippet = %q (%d runes), want %q (%d runes)",
					snippet, utf8.RuneCountInString(snippet), want, utf8.RuneCountInString(want))
			}
		})
	}
}

// TestSnippetForFoldedNeedle checks the symmetric case: the needle itself
// contains a rune whose lowercase form has a different byte length.
func TestSnippetForFoldedNeedle(t *testing.T) {
	value := "prefix " + "İSTANBUL" + " suffix"
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	snippet, ok := snippetFor(data, "istanbul")
	if !ok {
		t.Fatal("snippetFor did not match İSTANBUL with a lowercase needle")
	}
	if !strings.Contains(snippet, "İSTANBUL") {
		t.Fatalf("snippet = %q, want the original (unlowered) text", snippet)
	}
}
