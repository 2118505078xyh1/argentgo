package tui

import (
	"reflect"
	"testing"
)

// wrapFirst is the streaming sibling of Wrap: it is used by wrapLimited for
// tool-output preview rows. Like Wrap/Clip/Buffer.WriteString it must measure
// and break on grapheme clusters, not runes, or a ZWJ emoji or regional-indicator
// flag is torn apart and a dangling joiner/modifier is painted on its own row.
func TestWrapFirstKeepsGraphemeClustersIntact(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		width    int
		wantHead string
		wantTail string
	}{
		{
			name:     "ZWJ family fits whole",
			text:     familyZWJ,
			width:    4,
			wantHead: familyZWJ,
			wantTail: "",
		},
		{
			name:     "flag after ASCII is not split",
			text:     "a" + flagCN,
			width:    2,
			wantHead: "a",
			wantTail: flagCN,
		},
		{
			name:     "combining sequence stays with its base",
			text:     combiningE + "x",
			width:    1,
			wantHead: combiningE,
			wantTail: "x",
		},
		{
			name:     "skin-tone modifier stays with its emoji",
			text:     thumbTone + "z",
			width:    2,
			wantHead: thumbTone,
			wantTail: "z",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			head, tail := wrapFirst(tc.text, tc.width)
			if head != tc.wantHead || tail != tc.wantTail {
				t.Fatalf("wrapFirst(%q, %d) = (%q, %q), want (%q, %q)",
					tc.text, tc.width, head, tail, tc.wantHead, tc.wantTail)
			}
			if got := head + tail; got != tc.text {
				t.Fatalf("wrapFirst dropped runes: %q + %q = %q, want %q",
					head, tail, got, tc.text)
			}
		})
	}
}

func TestWrapLimitedKeepsGraphemeClustersIntact(t *testing.T) {
	got := wrapLimited("a"+flagCN+"b", 2, 3)
	want := []string{"a", flagCN, "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wrapLimited = %q, want %q", got, want)
	}
}
