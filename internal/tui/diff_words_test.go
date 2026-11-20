package tui

import (
	"reflect"
	"strings"
	"testing"
)

// segString 把 Changed 段渲染为 [方括号]，使期望值可读。
func segString(segs []WordSegment) string {
	var b strings.Builder
	for _, s := range segs {
		if s.Changed {
			b.WriteByte('[')
			b.WriteString(s.Text)
			b.WriteByte(']')
		} else {
			b.WriteString(s.Text)
		}
	}
	return b.String()
}

func joinWordSegs(segs []WordSegment) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.Text)
	}
	return b.String()
}

func TestWordDiff(t *testing.T) {
	longWord := strings.Repeat("y", 20)
	cases := []struct {
		name             string
		oldLine, newLine string
		wantOld, wantNew string
	}{
		{
			name:    "small change marks only the changed words",
			oldLine: "a = left side", newLine: "a = right side",
			wantOld: "a = [left] side", wantNew: "a = [right] side",
		},
		{
			name:    "whole line replacement degrades",
			oldLine: "alpha beta gamma", newLine: "delta epsilon zeta",
			wantOld: "[alpha beta gamma]", wantNew: "[delta epsilon zeta]",
		},
		{
			name:    "added word marks only the insertion",
			oldLine: "one two three", newLine: "one two three four",
			wantOld: "one two three", wantNew: "one two three[ four]",
		},
		{
			name:    "removed word marks only the deletion",
			oldLine: "one two three four", newLine: "one two three",
			wantOld: "one two three[ four]", wantNew: "one two three",
		},
		{
			name:    "CJK per-rune tokens",
			oldLine: "你好世界", newLine: "你好人间",
			wantOld: "你好[世界]", wantNew: "你好[人间]",
		},
		{
			name:    "mixed CJK and ASCII",
			oldLine: "say 你好 world", newLine: "say 再见 world",
			wantOld: "say [你好] world", wantNew: "say [再见] world",
		},
		{
			name:    "punctuation change",
			oldLine: "if (a == b) { return; }", newLine: "if (a != b) { return; }",
			wantOld: "if (a [==] b) { return; }", wantNew: "if (a [!=] b) { return; }",
		},
		{
			name:    "emoji is not split by byte",
			oldLine: "hi 😀 there", newLine: "hi 😃 there",
			wantOld: "hi [😀] there", wantNew: "hi [😃] there",
		},
		{
			name:    "large token churn degrades",
			oldLine: "completely different text here", newLine: "nothing alike at all",
			wantOld: "[completely different text here]", wantNew: "[nothing alike at all]",
		},
		{
			name:    "length skew degrades",
			oldLine: "x y z", newLine: "x " + longWord + " z",
			wantOld: "[x y z]", wantNew: "[x " + longWord + " z]",
		},
		{
			name:    "empty against text",
			oldLine: "", newLine: "hello",
			wantOld: "", wantNew: "[hello]",
		},
		{
			name:    "text against empty",
			oldLine: "hello", newLine: "",
			wantOld: "[hello]", wantNew: "",
		},
		{
			name:    "both empty",
			oldLine: "", newLine: "",
			wantOld: "", wantNew: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldSegs, newSegs := WordDiff(tc.oldLine, tc.newLine)
			if got := segString(oldSegs); got != tc.wantOld {
				t.Errorf("old segments = %q, want %q (%+v)", got, tc.wantOld, oldSegs)
			}
			if got := segString(newSegs); got != tc.wantNew {
				t.Errorf("new segments = %q, want %q (%+v)", got, tc.wantNew, newSegs)
			}
		})
	}
}

func TestWordDiffSegmentBoundaries(t *testing.T) {
	wantOld := []WordSegment{
		{Text: "a = "},
		{Text: "left", Changed: true},
		{Text: " side"},
	}
	wantNew := []WordSegment{
		{Text: "a = "},
		{Text: "right", Changed: true},
		{Text: " side"},
	}
	oldSegs, newSegs := WordDiff("a = left side", "a = right side")
	if !reflect.DeepEqual(oldSegs, wantOld) {
		t.Errorf("old segments = %+v, want %+v", oldSegs, wantOld)
	}
	if !reflect.DeepEqual(newSegs, wantNew) {
		t.Errorf("new segments = %+v, want %+v", newSegs, wantNew)
	}
}

func TestWordDiffEmptyLinesReturnNoSegments(t *testing.T) {
	oldSegs, newSegs := WordDiff("", "")
	if oldSegs != nil || newSegs != nil {
		t.Fatalf("empty lines returned %+v / %+v, want nil / nil", oldSegs, newSegs)
	}
}

func TestWordDiffSegmentsReconstructLines(t *testing.T) {
	pairs := [][2]string{
		{"a = left side", "a = right side"},
		{"你好世界", "你好人间"},
		{"one two three", "one two three four"},
		{"say 你好 world", "say 再见 world"},
		{"if (a == b) { return; }", "if (a != b) { return; }"},
		{"completely different text here", "nothing alike at all"},
		{"", "new text"},
	}
	for _, p := range pairs {
		oldSegs, newSegs := WordDiff(p[0], p[1])
		if got := joinWordSegs(oldSegs); got != p[0] {
			t.Errorf("old segments of %q reconstruct to %q", p[0], got)
		}
		if got := joinWordSegs(newSegs); got != p[1] {
			t.Errorf("new segments of %q reconstruct to %q", p[1], got)
		}
	}
}

func TestWordDiffDeterministic(t *testing.T) {
	oldLine, newLine := "let result = left value;", "let result = right value;"
	wantOld, wantNew := WordDiff(oldLine, newLine)
	for i := 0; i < 16; i++ {
		gotOld, gotNew := WordDiff(oldLine, newLine)
		if !reflect.DeepEqual(gotOld, wantOld) || !reflect.DeepEqual(gotNew, wantNew) {
			t.Fatalf("call %d differed: got %+v / %+v, want %+v / %+v",
				i, gotOld, gotNew, wantOld, wantNew)
		}
	}
}
