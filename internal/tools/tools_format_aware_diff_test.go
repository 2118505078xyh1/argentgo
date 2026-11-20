package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFormatAwareDiff(t *testing.T) {
	cases := []struct {
		name      string
		before    string
		after     string
		formatted string
		want      string
	}{
		{
			name:   "no formatting uses after",
			before: "a\nb\nc\n",
			after:  "a\nB\nc\n",
			want:   "+1/-1 行",
		},
		{
			name:      "formatted equal to after is ignored",
			before:    "a\nb\nc\n",
			after:     "a\nB\nc\n",
			formatted: "a\nB\nc\n",
			want:      "+1/-1 行",
		},
		{
			name:      "formatting changes final content",
			before:    "package p\nfunc f(){}\n",
			after:     "package p\nfunc f() { println(1) }\n",
			formatted: "package p\nfunc f() {\n\tprintln(1)\n}\n",
			want:      "+3/-1 行" + formattedMarker,
		},
		{
			name:   "CJK content",
			before: "第一行\n第二行\n",
			after:  "第一行\n修改行\n",
			want:   "+1/-1 行",
		},
		{
			name:      "CJK formatting",
			before:    "第一行\n第二行\n",
			after:     "第一行\n修改行\n",
			formatted: "第一行\n修改行\n新增行\n",
			want:      "+2/-1 行" + formattedMarker,
		},
		{
			name:   "all empty",
			before: "",
			after:  "",
			want:   "+0/-0 行",
		},
		{
			name:   "empty before with content",
			before: "",
			after:  "x\ny\n",
			want:   "+2/-0 行",
		},
		{
			name:   "identical content",
			before: "a\nb\n",
			after:  "a\nb\n",
			want:   "+0/-0 行",
		},
		{
			name:   "crlf normalized away",
			before: "a\r\nb\r\n",
			after:  "a\nb\n",
			want:   "+0/-0 行",
		},
		{
			name:   "long content under LCS limit",
			before: strings.Repeat("line\n", 1000),
			after:  strings.Repeat("line\n", 999) + "changed\n",
			want:   "+1/-1 行",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatAwareDiff(tc.before, tc.after, tc.formatted)
			if got != tc.want {
				t.Fatalf("formatAwareDiff() = %q, want %q", got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("result is not valid UTF-8: %q", got)
			}
		})
	}
}

func TestFormatAwareDiffBoundedOnHugeInput(t *testing.T) {
	before := strings.Repeat("内容\n", 5000)
	after := before + "新增行\n"
	got := formatAwareDiff(before, after, "")
	if !utf8.ValidString(got) {
		t.Fatalf("result is not valid UTF-8: %q", got)
	}
	if len(got) > formatDiffSummaryLimit {
		t.Fatalf("result length = %d, want <= %d (%q)", len(got), formatDiffSummaryLimit, got)
	}
	if !strings.HasPrefix(got, "+") || !strings.Contains(got, "/-") {
		t.Fatalf("result = %q, want a +N/-M summary", got)
	}
}
