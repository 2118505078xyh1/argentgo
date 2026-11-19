package security

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReadableArgsPreservesMultilineCommands(t *testing.T) {
	args := "echo first\nprintf second\nls -la"
	got := readableArgs(args)
	if got != args {
		t.Fatalf("readableArgs = %q, want %q", got, args)
	}
	if n := strings.Count(got, "\n"); n != 2 {
		t.Fatalf("got %d newlines, want 2: %q", n, got)
	}
}

func TestReadableArgsExpandsTabsToSpaces(t *testing.T) {
	got := readableArgs("a\tb\tc")
	if strings.Contains(got, "\t") {
		t.Fatalf("tabs were not expanded: %q", got)
	}
	if got != "a    b    c" {
		t.Fatalf("readableArgs = %q, want tabs expanded to spaces", got)
	}
}

func TestReadableArgsStripsANSIAndControlBytes(t *testing.T) {
	args := "\x1b[31mRED\x1b[0m\x00\x07\nnext\x1b]0;title\x07\x1b[2Kdone"
	got := readableArgs(args)
	if strings.Contains(got, "\x1b") {
		t.Fatalf("ANSI escape byte survived: %q", got)
	}
	if strings.Contains(got, "\x00") || strings.Contains(got, "\x07") {
		t.Fatalf("control bytes survived: %q", got)
	}
	const want = "RED\nnextdone"
	if got != want {
		t.Fatalf("readableArgs = %q, want %q", got, want)
	}
}

func TestReadableArgsTruncatesOnRuneBoundary(t *testing.T) {
	// 401 CJK runes (3 bytes each); the cut must not split the 401st rune.
	args := strings.Repeat("界", 401)
	got := readableArgs(args)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated args are not valid UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != 401 { // 400 kept + ellipsis
		t.Fatalf("got %d runes, want 401 (400 + ellipsis)", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated args should end with an ellipsis: %q", got)
	}
}

func TestPromptApproverWritesReadableArgs(t *testing.T) {
	var out bytes.Buffer
	approver := NewPromptApprover(strings.NewReader("n\n"), &out)
	if _, err := approver.Ask(context.Background(), "bash", "echo a\necho b"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	prompt := out.String()
	if !strings.Contains(prompt, "echo a\necho b") {
		t.Fatalf("prompt does not preserve the multiline command: %q", prompt)
	}
}
