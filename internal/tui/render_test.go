package tui

import (
	"context"
	"strings"
	"testing"
)

func TestLayoutPlacesHeaderBodyInputAndStatus(t *testing.T) {
	m := New(Config{Model: "test-model", Root: "/work/project"})
	m.AppendUser("hello")
	buffer := m.Frame(80, 24)

	header := buffer.RowText(0)
	for _, want := range []string{"argent", "test-model"} {
		if !strings.Contains(header, want) {
			t.Fatalf("header %q is missing %q", header, want)
		}
	}
	if got := buffer.RowText(23); got == "" {
		t.Fatalf("the status line is empty")
	}
	body := strings.Join(rowTexts(buffer), "\n")
	if !strings.Contains(body, "› hello") {
		t.Fatalf("the user block is missing from the frame:\n%s", body)
	}
	if !strings.Contains(body, "╭") || !strings.Contains(body, "╰") {
		t.Fatalf("the input box borders are missing:\n%s", body)
	}
}

func TestCursorPositionTracksTheInputBox(t *testing.T) {
	m := New(Config{})
	row, col := m.CursorPosition(80, 24)
	if row != 21 || col != 5 {
		t.Fatalf("caret = %d,%d, want 21,5 (the first input row)", row, col)
	}
	m.HandleKey(Key{Kind: KindRune, Rune: 'a'})
	m.HandleKey(Key{Kind: KindRune, Rune: 'b'})
	_, col = m.CursorPosition(80, 24)
	if col != 7 {
		t.Fatalf("caret column = %d, want 7 after two runes", col)
	}
	if got := m.Frame(80, 24).RowText(20); !strings.Contains(got, "› ab") {
		t.Fatalf("input row = %q, want the prompt mark and text", got)
	}
}

func TestMultilineInputGrowsTheBox(t *testing.T) {
	m := New(Config{})
	m.HandleKey(Key{Kind: KindRune, Rune: 'a'})
	m.HandleKey(Key{Kind: KindNewline})
	m.HandleKey(Key{Kind: KindRune, Rune: 'b'})
	row, _ := m.CursorPosition(80, 24)
	if row != 21 {
		t.Fatalf("caret row = %d, want 21 (the box grows upward, the caret stays on the last input line)", row)
	}
	buffer := m.Frame(80, 24)
	body := strings.Join(rowTexts(buffer), "\n")
	if !strings.Contains(body, "› a") || !strings.Contains(body, "  b") {
		t.Fatalf("the two input lines are not rendered:\n%s", body)
	}
}

func TestAssistantMarkdownLite(t *testing.T) {
	m := New(Config{})
	m.AppendUser("q")
	m.AppendStream("**bold** and `code`\n```\nfenced\n```")
	m.EndTurn(nil)
	body := strings.Join(rowTexts(m.Frame(60, 24)), "\n")
	if !strings.Contains(body, "◆ bold and code") {
		t.Fatalf("bold or inline code markers were not stripped:\n%s", body)
	}
	if !strings.Contains(body, "  fenced") {
		t.Fatalf("the fenced block is not indented by two spaces:\n%s", body)
	}
	if strings.Contains(body, "```") {
		t.Fatalf("the fence marker leaked into the frame:\n%s", body)
	}
}

func TestToolCallShowsNameArgsAndTruncatedOutput(t *testing.T) {
	m := New(Config{})
	m.AppendTool("read", `{"path":"internal/tui/render.go"}`, "line one\nline two\nline three\nline four\nline five", false)
	body := strings.Join(rowTexts(m.Frame(60, 24)), "\n")
	if !strings.Contains(body, "● read(internal/tui/render.go)") {
		t.Fatalf("tool call header is wrong:\n%s", body)
	}
	if !strings.Contains(body, "line one") {
		t.Fatalf("the first output line is missing:\n%s", body)
	}
	if !strings.Contains(body, "more lines") {
		t.Fatalf("long output should be truncated with a counter:\n%s", body)
	}

	m.HandleKey(Key{Kind: KindRune, Rune: '/'})
	m.HandleKey(Key{Kind: KindRune, Rune: 'e'})
	m.HandleKey(Key{Kind: KindRune, Rune: 'x'})
	m.HandleKey(Key{Kind: KindRune, Rune: 'p'})
	m.HandleKey(Key{Kind: KindRune, Rune: 'a'})
	m.HandleKey(Key{Kind: KindRune, Rune: 'n'})
	m.HandleKey(Key{Kind: KindRune, Rune: 'd'})
	m.HandleKey(Key{Kind: KindEnter})
	expanded := strings.Join(rowTexts(m.Frame(60, 24)), "\n")
	if !strings.Contains(expanded, "line five") {
		t.Fatalf("/expand should show the whole output:\n%s", expanded)
	}
}

func TestReasoningBlockIsDimAndPrefixed(t *testing.T) {
	m := New(Config{})
	m.AppendReasoning("weighing options")
	body := strings.Join(rowTexts(m.Frame(60, 24)), "\n")
	if !strings.Contains(body, "◇ thinking") {
		t.Fatalf("reasoning header is missing:\n%s", body)
	}
	if !strings.Contains(body, "weighing options") {
		t.Fatalf("reasoning text is missing:\n%s", body)
	}
}

func TestStreamingRepaintsOnlyChangedRows(t *testing.T) {
	m := New(Config{})
	m.AppendUser("question")
	m.AppendStream("first")
	buffer := m.Frame(80, 24)

	var sink strings.Builder
	if _, err := buffer.Flush(&sink); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if sink.Len() == 0 {
		t.Fatalf("the first frame should paint")
	}

	sink.Reset()
	m.AppendStream(" second")
	m.Paint(buffer)
	written, err := buffer.Flush(&sink)
	if err != nil {
		t.Fatalf("second flush: %v", err)
	}
	if written == 0 {
		t.Fatalf("a streamed delta must repaint its row")
	}
	if written >= buffer.Height() {
		t.Fatalf("a streamed delta repainted %d rows, want only the changed ones", written)
	}
	if !strings.Contains(sink.String(), "second") {
		t.Fatalf("the delta is missing from the repaint: %q", sink.String())
	}
}

func TestApprovalModalIsCentredOverTheBody(t *testing.T) {
	m := New(Config{})
	done := make(chan bool, 1)
	go func() {
		ok, _ := m.Approve(context.Background(), "bash", `{"command":"go test ./..."}`)
		done <- ok
	}()
	waitFor(t, "the modal", func() bool { _, _, ok := m.PendingApproval(); return ok })

	body := strings.Join(rowTexts(m.Frame(80, 24)), "\n")
	for _, want := range []string{"approval", "bash", "go test", "[y] allow", "[n] deny", "[a] allow for session"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the modal is missing %q:\n%s", want, body)
		}
	}
	// 底部快捷提示行已按用户要求移除；弹窗自身仍显示 y/n/a 按键。
	if !strings.Contains(body, "[y] allow") || !strings.Contains(body, "[n] deny") {
		t.Fatalf("the modal keys must be visible in the dialog:\n%s", body)
	}
	m.HandleKey(Key{Kind: KindRune, Rune: 'n'})
	<-done
}

func TestInputBoxRowsFillTheWidth(t *testing.T) {
	m := New(Config{})
	m.HandleKey(Key{Kind: KindPaste, Text: strings.Repeat("x", 150)})
	buffer := m.Frame(100, 24)
	seen := 0
	for y := 0; y < buffer.Height(); y++ {
		text := buffer.RowText(y)
		if strings.HasPrefix(text, "╭") || strings.HasPrefix(text, "╰") || strings.HasPrefix(text, "│") {
			if got := TextWidth(text); got != 100 {
				t.Fatalf("input row %d is %d cells wide, want 100: %q", y, got, text)
			}
			seen++
		}
	}
	if seen != 4 {
		t.Fatalf("found %d input-box rows, want a top border, two text rows and a bottom border", seen)
	}
}

func TestBodyIsCentredOnWideTerminals(t *testing.T) {
	m := New(Config{})
	m.AppendUser("hi")
	buffer := m.Frame(200, 30)
	found := ""
	for y := 0; y < buffer.Height(); y++ {
		if row := buffer.RowText(y); strings.Contains(row, "› hi") {
			found = row
			break
		}
	}
	if found == "" {
		t.Fatalf("the user block was not rendered")
	}
	if !strings.HasPrefix(found, strings.Repeat(" ", 40)) {
		t.Fatalf("body starts at the wrong column: %q", found)
	}
}

func TestSmallTerminalsDoNotPanic(t *testing.T) {
	m := New(Config{})
	m.AppendUser("hello")
	for _, dims := range [][2]int{{1, 1}, {10, 4}, {20, 6}, {40, 8}} {
		buffer := m.Frame(dims[0], dims[1])
		if buffer.Width() != dims[0] || buffer.Height() != dims[1] {
			t.Fatalf("frame %v has size %dx%d", dims, buffer.Width(), buffer.Height())
		}
	}
}

func rowTexts(buffer *Buffer) []string {
	out := make([]string, 0, buffer.Height())
	for y := 0; y < buffer.Height(); y++ {
		out = append(out, buffer.RowText(y))
	}
	return out
}
