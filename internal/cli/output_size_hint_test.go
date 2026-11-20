package cli

import (
	"strconv"
	"strings"
	"testing"
)

func TestOutputSizeHintEmpty(t *testing.T) {
	want := "将把约 0 行 / ~0 tokens 的输出加入上下文"
	if got := outputSizeHint(""); got != want {
		t.Fatalf("outputSizeHint(\"\") = %q, want %q", got, want)
	}
}

func TestOutputSizeHintCountsLines(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		lines int
	}{
		{"single line without trailing newline", "hello", 1},
		{"single line with trailing newline", "hello\n", 1},
		{"two lines", "a\nb", 2},
		{"two lines with trailing newline", "a\nb\n", 2},
		{"crlf lines", "a\r\nb\r\n", 2},
		{"blank interior line", "a\n\nb", 3},
		{"only newlines", "\n\n\n", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := outputSizeHint(tc.in)
			if !strings.Contains(got, strconv.Itoa(tc.lines)+" 行") {
				t.Fatalf("outputSizeHint(%q) = %q, want %d 行", tc.in, got, tc.lines)
			}
		})
	}
}

func TestOutputSizeHintCJK(t *testing.T) {
	// 100 个汉字：宽字符每个约一个 token，不能被 chars/4 低估成 25。
	got := outputSizeHint(strings.Repeat("中", 100))
	if !strings.Contains(got, "~100 tokens") {
		t.Fatalf("CJK hint = %q, want ~100 tokens", got)
	}
}

func TestOutputSizeHintEstimateReasonable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"ascii chars over four", strings.Repeat("a", 400), "~100 tokens"},
		{"mixed ascii and han", "abcd中文", "~3 tokens"},
		{"emoji counts one each", "\U0001F600\U0001F600", "~2 tokens"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := outputSizeHint(tc.in)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("outputSizeHint(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestOutputSizeHintLargeContentStaysBounded(t *testing.T) {
	in := strings.Repeat("a fairly long line of output\n", 100000)
	got := outputSizeHint(in)
	// 提示只含两个数字和固定文案，即使输入巨大也应保持很短。
	if len(got) > 128 {
		t.Fatalf("hint length = %d, want <= 128: %q", len(got), got)
	}
	if !strings.Contains(got, "100000 行") {
		t.Fatalf("large hint = %q, want 100000 行", got)
	}
	if !strings.Contains(got, "tokens") {
		t.Fatalf("large hint = %q, want token count", got)
	}
}
