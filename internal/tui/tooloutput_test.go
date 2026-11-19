package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAppendToolTruncatesHugeOutputWithHeadAndTail(t *testing.T) {
	head := strings.Repeat("A", 100)
	tail := strings.Repeat("Z", 100)
	huge := head + strings.Repeat("x", 200<<10) + "\n" + tail

	m := New(Config{})
	m.AppendTool("read", `{}`, huge, false)
	out := m.Transcript()[0].Output

	if len(out) > maxToolOutputBytes {
		t.Fatalf("stored output is %d bytes, want at most %d", len(out), maxToolOutputBytes)
	}
	if !strings.HasPrefix(out, head) {
		t.Fatal("the head window was dropped")
	}
	if !strings.HasSuffix(out, tail) {
		t.Fatal("the tail window was dropped")
	}
	if !strings.Contains(out, "字节省略") {
		t.Fatalf("the omission marker is missing: %q", out[len(out)-80:])
	}
}

func TestAppendToolTruncationNeverSplitsRunes(t *testing.T) {
	unit := "汉😀" // 3 + 4 bytes: exercise both 3- and 4-byte boundaries.
	for _, tc := range []struct {
		name   string
		output string
	}{
		{"aligned", strings.Repeat(unit, 20000)},
		{"offset", "x" + strings.Repeat(unit, 20000)},
		{"line_based", strings.Repeat("汉字\n", 5000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := New(Config{})
			m.AppendTool("read", `{}`, tc.output, false)
			out := m.Transcript()[0].Output
			if !utf8.ValidString(out) {
				t.Fatal("truncation split a UTF-8 rune")
			}
			if len(out) > maxToolOutputBytes {
				t.Fatalf("stored output is %d bytes, want at most %d", len(out), maxToolOutputBytes)
			}
			if !strings.Contains(out, "字节省略") {
				t.Fatal("the omission marker is missing")
			}
		})
	}
}

func TestAppendToolEnforcesLineLimit(t *testing.T) {
	// 10 KB total: under the byte cap, over the line cap.
	m := New(Config{})
	m.AppendTool("read", `{}`, strings.Repeat("x\n", 5000), false)
	out := m.Transcript()[0].Output
	if lines := strings.Count(out, "\n") + 1; lines > maxToolOutputLines {
		t.Fatalf("stored output has %d lines, want at most %d", lines, maxToolOutputLines)
	}
	if !strings.Contains(out, "字节省略") {
		t.Fatal("the omission marker is missing")
	}
}

func TestPlainDoesNotFloodOnHugeToolOutput(t *testing.T) {
	m := New(Config{})
	m.AppendTool("read", `{"path":"a.go"}`, strings.Repeat("x", 1<<20), false)
	plain := m.Plain()
	if len(plain) > 4096 {
		t.Fatalf("Plain wrote %d bytes, want a bounded preview", len(plain))
	}
	if !strings.Contains(plain, "read(a.go)") {
		t.Fatalf("the tool header is missing: %q", plain)
	}
	if !strings.Contains(plain, "output omitted") {
		t.Fatalf("the preview marker is missing: %q", plain)
	}
	if !utf8.ValidString(plain) {
		t.Fatal("the preview split a UTF-8 rune")
	}
}

func TestPlainKeepsShortToolOutput(t *testing.T) {
	m := New(Config{})
	m.AppendTool("read", `{"path":"a.go"}`, "contents", false)
	plain := m.Plain()
	if !strings.Contains(plain, "contents") {
		t.Fatalf("short output should survive the preview: %q", plain)
	}
	if strings.Contains(plain, "output omitted") {
		t.Fatalf("short output must not be marked as omitted: %q", plain)
	}
}
