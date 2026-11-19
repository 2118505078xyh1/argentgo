package tui

import (
	"reflect"
	"testing"
)

func TestDecoderReadsNavigationSequences(t *testing.T) {
	cases := map[string]Kind{
		"\x1b[A":        KindUp,
		"\x1b[B":        KindDown,
		"\x1b[C":        KindRight,
		"\x1b[D":        KindLeft,
		"\x1b[H":        KindHome,
		"\x1b[F":        KindEnd,
		"\x1b[1~":       KindHome,
		"\x1b[4~":       KindEnd,
		"\x1b[3~":       KindDelete,
		"\x1b[5~":       KindPageUp,
		"\x1b[6~":       KindPageDown,
		"\x1b[1;2A":     KindScrollUp,
		"\x1b[1;2B":     KindScrollDown,
		"\x1b[Z":        KindBackTab,
		"\x1bOA":        KindUp,
		"\x1b[<64;5;9M": KindScrollUp,
		"\x1b[<65;5;9M": KindScrollDown,
		"\x1b[<0;5;9M":  KindIgnore,
		"\x1b[I":        KindIgnore,
	}
	for input, want := range cases {
		decoder := &Decoder{}
		keys := decoder.Feed([]byte(input))
		if len(keys) != 1 {
			t.Errorf("%q decoded to %d keys, want 1", input, len(keys))
			continue
		}
		if keys[0].Kind != want {
			t.Errorf("%q decoded to %v, want %v", input, keys[0].Kind, want)
		}
	}
}

func TestDecoderKeepsSplitSequences(t *testing.T) {
	decoder := &Decoder{}
	if keys := decoder.Feed([]byte("\x1b[")); len(keys) != 0 {
		t.Fatalf("a partial sequence should not decode yet: %v", keys)
	}
	keys := decoder.Feed([]byte("A"))
	if len(keys) != 1 || keys[0].Kind != KindUp {
		t.Fatalf("keys = %v, want Up", keys)
	}
	if decoder.Pending() {
		t.Fatalf("the buffer should be empty")
	}
}

func TestLoneEscapeFlushesOnlyWhenAsked(t *testing.T) {
	decoder := &Decoder{}
	if keys := decoder.Feed([]byte{0x1b}); len(keys) != 0 {
		t.Fatalf("a lone ESC must wait: %v", keys)
	}
	keys := decoder.FlushEscape()
	if len(keys) != 1 || keys[0].Kind != KindEsc {
		t.Fatalf("keys = %v, want Esc", keys)
	}
	if keys := decoder.FlushEscape(); len(keys) != 0 {
		t.Fatalf("the escape was already flushed: %v", keys)
	}
}

func TestDecoderReadsControlKeys(t *testing.T) {
	cases := map[byte]Kind{
		0x01: KindCtrlA,
		0x03: KindCtrlC,
		0x05: KindCtrlE,
		0x08: KindBackspace,
		0x09: KindTab,
		0x0a: KindNewline,
		0x0b: KindCtrlK,
		0x0c: KindCtrlL,
		0x0d: KindEnter,
		0x15: KindCtrlU,
		0x17: KindCtrlW,
		0x7f: KindBackspace,
	}
	for input, want := range cases {
		decoder := &Decoder{}
		keys := decoder.Feed([]byte{input})
		if len(keys) != 1 || keys[0].Kind != want {
			t.Errorf("%#x decoded to %v, want %v", input, keys, want)
		}
	}
}

func TestDecoderReadsUTF8Runes(t *testing.T) {
	decoder := &Decoder{}
	keys := decoder.Feed([]byte("你"))
	want := []Key{{Kind: KindRune, Rune: '你'}}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys = %#v, want %#v", keys, want)
	}

	partial := &Decoder{}
	if keys := partial.Feed([]byte{0xe4}); len(keys) != 0 {
		t.Fatalf("half a rune should wait: %v", keys)
	}
	if keys := partial.Feed([]byte{0xbd}); len(keys) != 0 {
		t.Fatalf("two thirds of a rune should still wait: %v", keys)
	}
	keys = partial.Feed([]byte{0xa0})
	if len(keys) != 1 || keys[0].Rune != '你' {
		t.Fatalf("keys = %#v, want 你", keys)
	}
}

func TestDecoderReadsAltEnterAndPaste(t *testing.T) {
	decoder := &Decoder{}
	keys := decoder.Feed([]byte("\x1b\r"))
	if len(keys) != 1 || keys[0].Kind != KindAltEnter {
		t.Fatalf("alt+enter = %#v", keys)
	}

	paste := &Decoder{}
	keys = paste.Feed([]byte("\x1b[200~a\nb\x1b[201~"))
	if len(keys) != 1 || keys[0].Kind != KindPaste || keys[0].Text != "a\nb" {
		t.Fatalf("paste = %#v", keys)
	}

	split := &Decoder{}
	if keys := split.Feed([]byte("\x1b[200~abc")); len(keys) != 0 {
		t.Fatalf("an unfinished paste should wait: %#v", keys)
	}
	keys = split.Feed([]byte("\x1b[201~"))
	if len(keys) != 1 || keys[0].Text != "abc" {
		t.Fatalf("paste = %#v", keys)
	}
}
