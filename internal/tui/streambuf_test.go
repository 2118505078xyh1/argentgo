package tui

import (
	"strings"
	"testing"
)

func TestMarkdownStreamBufPlainTextIsImmediate(t *testing.T) {
	var b MarkdownStreamBuf
	for _, delta := range []string{"hello", " world", "中文", " emoji 🙂"} {
		if got := b.Push(delta); got != delta {
			t.Fatalf("Push(%q) = %q, want the whole delta", delta, got)
		}
	}
	if got := b.Flush(); got != "" {
		t.Fatalf("Flush() = %q, want nothing left over", got)
	}
}

func TestMarkdownStreamBufHoldsHalfBoldUntilClosed(t *testing.T) {
	var b MarkdownStreamBuf
	if got := b.Push("plain *"); got != "plain " {
		t.Fatalf("Push trailing single star = %q, want %q", got, "plain ")
	}
	if got := b.Push("*bold*"); got != "" {
		t.Fatalf("Push still-unclosed bold = %q, want nothing", got)
	}
	if got := b.Push("* done"); got != "**bold** done" {
		t.Fatalf("Push closing star = %q, want %q", got, "**bold** done")
	}
	if got := b.Flush(); got != "" {
		t.Fatalf("Flush() = %q, want nothing left over", got)
	}
}

func TestMarkdownStreamBufHoldsInlineCodeUntilClosed(t *testing.T) {
	var b MarkdownStreamBuf
	if got := b.Push("use `co"); got != "use " {
		t.Fatalf("Push unclosed inline code = %q, want %q", got, "use ")
	}
	if got := b.Push("de` now"); got != "`code` now" {
		t.Fatalf("Push closing backtick = %q, want %q", got, "`code` now")
	}
}

func TestMarkdownStreamBufHoldsUnclosedFence(t *testing.T) {
	var b MarkdownStreamBuf
	if got := b.Push("before\n```go\ncode"); got != "before\n" {
		t.Fatalf("Push unclosed fence = %q, want %q", got, "before\n")
	}
	if got := b.Push("\nmore\n```\nafter"); got != "```go\ncode\nmore\n```\nafter" {
		t.Fatalf("Push closing fence = %q, want %q", got, "```go\ncode\nmore\n```\nafter")
	}
}

func TestMarkdownStreamBufHoldsUnderscoreEmphasis(t *testing.T) {
	var b MarkdownStreamBuf
	if got := b.Push("__under"); got != "" {
		t.Fatalf("Push unclosed emphasis = %q, want nothing", got)
	}
	if got := b.Push("score__ ok"); got != "__underscore__ ok" {
		t.Fatalf("Push closing emphasis = %q, want %q", got, "__underscore__ ok")
	}
}

func TestMarkdownStreamBufFlushEmitsRemainder(t *testing.T) {
	var b MarkdownStreamBuf
	if got := b.Push("a `unclosed fence *"); got != "a " {
		t.Fatalf("Push = %q, want %q", got, "a ")
	}
	if got := b.Flush(); got != "`unclosed fence *" {
		t.Fatalf("Flush() = %q, want the withheld tail", got)
	}
	if got := b.Flush(); got != "" {
		t.Fatalf("second Flush() = %q, want empty", got)
	}
}

func TestMarkdownStreamBufDoesNotSplitUTF8(t *testing.T) {
	var b MarkdownStreamBuf
	if got := b.Push("中文测试"); got != "中文测试" {
		t.Fatalf("Push CJK = %q, want the whole string", got)
	}
	if got := b.Push("**粗体"); got != "" {
		t.Fatalf("Push unclosed bold after CJK = %q, want nothing", got)
	}
	if got := b.Push("**"); got != "**粗体**" {
		t.Fatalf("Push closing bold = %q, want %q", got, "**粗体**")
	}
	if got := b.Push("🙂"); got != "🙂" {
		t.Fatalf("Push emoji = %q, want the whole rune", got)
	}
}

func TestMarkdownStreamBufResetClearsState(t *testing.T) {
	var b MarkdownStreamBuf
	if got := b.Push("left `code"); got != "left " {
		t.Fatalf("Push = %q, want %q", got, "left ")
	}
	b.Reset()
	if got := b.Push("right"); got != "right" {
		t.Fatalf("Push after Reset = %q, want no leftover state", got)
	}
	if got := b.Flush(); got != "" {
		t.Fatalf("Flush after Reset = %q, want empty", got)
	}
}

func TestMarkdownStreamBufConcatenationIsLossless(t *testing.T) {
	// 每个被推入的字节最终都必须按顺序输出，要么由
	// Push 输出，要么由最后的 Flush 输出。这防止悄悄吞掉文本。
	deltas := []string{
		"intro **bold", "** and `code", "` then\n```", "go\nline", "\n```\ndone *_x_",
	}
	var b MarkdownStreamBuf
	var emitted strings.Builder
	for _, d := range deltas {
		emitted.WriteString(b.Push(d))
	}
	emitted.WriteString(b.Flush())
	want := strings.Join(deltas, "")
	if got := emitted.String(); got != want {
		t.Fatalf("emitted %q, want %q", got, want)
	}
}

func TestMarkdownStreamBufHoldsTableUntilBlockEnds(t *testing.T) {
	var b MarkdownStreamBuf
	cases := []struct {
		delta string
		want  string
	}{
		{"intro\n", "intro\n"},
		{"a|b", ""},       // potential header, delimiter not seen yet
		{"\n---|---", ""}, // delimiter still undecidable
		{"\n1|2", ""},     // body row still streaming
		{"\noutro\n", "a|b\n---|---\n1|2\noutro\n"},
	}
	for _, tc := range cases {
		if got := b.Push(tc.delta); got != tc.want {
			t.Fatalf("Push(%q) = %q, want %q", tc.delta, got, tc.want)
		}
	}
	if got := b.Flush(); got != "" {
		t.Fatalf("Flush() = %q, want nothing left over", got)
	}
}

func TestMarkdownStreamBufHoldsTrailingTableBodyRow(t *testing.T) {
	// 仍在流式传输的不含 '|' 的正文行不得结束块：
	// 提交它会渲染窄列，而后者在下一个增量中变宽。
	var b MarkdownStreamBuf
	if got := b.Push("a|b\n---|---\n1|2\nlong "); got != "" {
		t.Fatalf("Push partial block = %q, want nothing", got)
	}
	if got := b.Push("cell\nend\n"); got != "a|b\n---|---\n1|2\nlong cell\nend\n" {
		t.Fatalf("Push block end = %q, want the whole table and trailing row", got)
	}
}

func TestMarkdownStreamBufCommitsOrdinaryPipeLine(t *testing.T) {
	// 下一完整行不是分隔符的 '|' 行是普通
	// 文本；绝不能只因为它含竖线就永远保留它。
	var b MarkdownStreamBuf
	if got := b.Push("use a|b"); got != "" {
		t.Fatalf("Push pipe line = %q, want held until the next line", got)
	}
	if got := b.Push("\nnext line\n"); got != "use a|b\nnext line\n" {
		t.Fatalf("Push resolved pipe line = %q, want the resolved text", got)
	}
}

func TestMarkdownStreamBufTableConcatenationIsLossless(t *testing.T) {
	deltas := []string{"x|y", "\n---|---", "\n1|2", "\n\n", "tail"}
	var b MarkdownStreamBuf
	var emitted strings.Builder
	for _, d := range deltas {
		emitted.WriteString(b.Push(d))
	}
	emitted.WriteString(b.Flush())
	if got, want := emitted.String(), strings.Join(deltas, ""); got != want {
		t.Fatalf("emitted %q, want %q", got, want)
	}
}

func TestAppendStreamHoldsHalfMarkerUntilCompleted(t *testing.T) {
	m := New(Config{})
	m.AppendStream("hello **")
	entries := m.Transcript()
	if len(entries) != 1 || entries[0].Kind != EntryAssistant || entries[0].Text != "hello " {
		t.Fatalf("assistant block after half marker = %+v, want text %q", entries, "hello ")
	}

	m.AppendStream("bold**")
	entries = m.Transcript()
	if len(entries) != 1 || entries[0].Text != "hello **bold**" {
		t.Fatalf("assistant block after completion = %+v, want %q", entries, "hello **bold**")
	}

	m.EndTurn(nil)
	entries = m.Transcript()
	if len(entries) != 1 || entries[0].Text != "hello **bold**" {
		t.Fatalf("assistant block after EndTurn = %+v, want %q", entries, "hello **bold**")
	}
}

func TestEndTurnFlushesHeldMarkerTail(t *testing.T) {
	m := New(Config{})
	m.AppendStream("text with `unclosed")
	m.EndTurn(nil)
	entries := m.Transcript()
	if len(entries) != 1 || entries[0].Kind != EntryAssistant || entries[0].Text != "text with `unclosed" {
		t.Fatalf("assistant block = %+v, want the held tail flushed", entries)
	}
}

func TestNextTurnStartsWithCleanStreamBuf(t *testing.T) {
	m := New(Config{})
	m.AppendStream("first `open")
	m.EndTurn(nil)

	m.AppendStream("second")
	entries := m.Transcript()
	if len(entries) != 2 || entries[0].Text != "first `open" || entries[1].Text != "second" {
		t.Fatalf("blocks across turns = %+v, want no marker residue", entries)
	}
}
