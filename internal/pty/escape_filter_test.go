package pty

import (
	"bytes"
	"testing"
)

func TestStripTerminalQueriesRemovesQueries(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"DSR cursor position", "before\x1b[6nafter", "beforeafter"},
		{"DSR device status", "a\x1b[5nb", "ab"},
		{"DSR with private params", "a\x1b[?6nb", "ab"},
		{"OSC background query BEL", "a\x1b]11;?\x07b", "ab"},
		{"OSC foreground query ST", "a\x1b]10;?\x1b\\b", "ab"},
		{"OSC indexed colour query", "a\x1b]4;1;?\x07b", "ab"},
		{"OSC cursor colour query", "a\x1b]12;?\x07b", "ab"},
		{"DA request", "a\x1b[cb", "ab"},
		{"DA with param", "a\x1b[0cb", "ab"},
		{"DA2 secondary", "a\x1b[>cb", "ab"},
		{"DECRQSS", "a\x1b[$qb", "ab"},
		{"DECRQSS with param", "a\x1b[1$qb", "ab"},
		{"several queries back to back", "\x1b[6n\x1b]11;?\x07\x1b[c\x1b[$q", ""},
		{"query before coloured text", "\x1b[6nhello\x1b[1mworld", "hello\x1b[1mworld"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clean, pending := stripTerminalQueries([]byte(tt.input))
			if len(pending) != 0 {
				t.Fatalf("pending = %q, want none", pending)
			}
			if got := string(clean); got != tt.want {
				t.Fatalf("clean = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStripTerminalQueriesPreservesDisplaySequences(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"SGR colour", "\x1b[31mred\x1b[0m"},
		{"SGR bold", "\x1b[1mbold\x1b[22m"},
		{"SGR 256 colour", "\x1b[38;5;200mhi\x1b[39m"},
		{"cursor hide/show", "\x1b[?25l\x1b[?25h"},
		{"cursor move and clear", "\x1b[2J\x1b[H"},
		{"charset designation", "\x1b(B\x1b)0"},
		{"OSC title without query", "\x1b]0;my title\x07text"},
		{"OSC hyperlink with '?' in URL", "\x1b]8;;https://x/?a=1\x1b\\link\x1b]8;;\x1b\\"},
		{"DECSCA (final q, non-$ intermediate)", "\x1b[\"qtext"},
		{"RIS", "\x1bc"},
		{"plain text", "plain text \n\t 端"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clean, pending := stripTerminalQueries([]byte(tt.input))
			if len(pending) != 0 {
				t.Fatalf("pending = %q, want none", pending)
			}
			if got := string(clean); got != tt.input {
				t.Fatalf("clean = %q, want the input unchanged %q", got, tt.input)
			}
		})
	}
}

// feedChunks 用给定的分块边界驱动流式包装器并
// feedChunks 用给定的分块边界驱动流式包装器并返回它发出的所有内容，在 EOF 时刷新任何尾部。复用调用方的读缓冲区不得破坏保留的字节。
func feedChunks(chunks ...[]byte) []byte {
	f := new(queryFilter)
	var out []byte
	for _, c := range chunks {
		out = append(out, f.Write(c)...)
	}
	return append(out, f.Flush()...)
}

func TestStripTerminalQueriesAcrossChunks(t *testing.T) {
	tests := []struct {
		name   string
		chunks [][]byte
		want   string
	}{
		{"DSR split", [][]byte{[]byte("\x1b["), []byte("6"), []byte("nX")}, "X"},
		{"DSR split after ESC", [][]byte{[]byte("a\x1b"), []byte("[6nb")}, "ab"},
		{"OSC BEL split", [][]byte{[]byte("\x1b]11;"), []byte("?\x07"), []byte("Y")}, "Y"},
		{"OSC ST split at ESC", [][]byte{[]byte("\x1b]10;?\x1b"), []byte("\\Z")}, "Z"},
		{"DECRQSS split", [][]byte{[]byte("\x1b["), []byte("$"), []byte("q")}, ""},
		{"DA2 split", [][]byte{[]byte("\x1b[>"), []byte("cW")}, "W"},
		{"SGR split is preserved", [][]byte{[]byte("\x1b["), []byte("3"), []byte("1mred")}, "\x1b[31mred"},
		{"charset split is preserved", [][]byte{[]byte("\x1b"), []byte("(Bok")}, "\x1b(Bok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(feedChunks(tt.chunks...)); got != tt.want {
				t.Fatalf("output = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStripTerminalQueriesKeepsUTF8(t *testing.T) {
	t.Run("whole string", func(t *testing.T) {
		in := "你好\x1b[31m世界\x1b[0m"
		clean, pending := stripTerminalQueries([]byte(in))
		if len(pending) != 0 || string(clean) != in {
			t.Fatalf("clean = %q pending = %q, want %q unchanged", clean, pending, in)
		}
	})

	t.Run("rune split across chunks", func(t *testing.T) {
		// "你" is E4 BD A0; cut it in the middle of the rune.
		got := feedChunks([]byte{0xe4, 0xbd}, []byte{0xa0, 0xe5, 0xa5, 0xbd}, []byte(""))
		if want := "你好"; string(got) != want {
			t.Fatalf("output = %x (%q), want %q", got, got, want)
		}
	})

	t.Run("query split around Chinese", func(t *testing.T) {
		got := feedChunks([]byte("中\x1b["), []byte("6n"), []byte("文"))
		if want := "中文"; string(got) != want {
			t.Fatalf("output = %q, want %q", got, want)
		}
	})
}

func TestStripTerminalQueriesPendingIsACopy(t *testing.T) {
	buf := []byte("x\x1b]11;?") // incomplete OSC -> held back as pending
	clean, pending := stripTerminalQueries(buf)
	if string(clean) != "x" || string(pending) != "\x1b]11;?" {
		t.Fatalf("clean = %q pending = %q", clean, pending)
	}
	// 复用调用方的读缓冲区不得破坏保留的字节。
	for i := range buf {
		buf[i] = 'Z'
	}
	if string(pending) != "\x1b]11;?" {
		t.Fatalf("pending aliases the input buffer: %q", pending)
	}
}

func TestQueryFilterFlushEmitsDanglingFragment(t *testing.T) {
	f := new(queryFilter)
	if got := string(f.Write([]byte("ok\x1b[6"))); got != "ok" {
		t.Fatalf("write = %q, want %q", got, "ok")
	}
	if got := string(f.Flush()); got != "\x1b[6" {
		t.Fatalf("flush = %q, want %q", got, "\x1b[6")
	}
	if got := f.Flush(); len(got) != 0 {
		t.Fatalf("second flush = %q, want none", got)
	}
	if !bytes.Equal(f.Write([]byte("x")), []byte("x")) {
		t.Fatal("write after flush must be clean")
	}
}
