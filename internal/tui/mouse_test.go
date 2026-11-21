package tui

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// decodeOne 喂入一个完整字节串，并期望恰好解码出一个按键。
func decodeOne(t *testing.T, input string) Key {
	t.Helper()
	decoder := &Decoder{}
	keys := decoder.Feed([]byte(input))
	if len(keys) != 1 {
		t.Fatalf("%q decoded to %d keys, want 1: %#v", input, len(keys), keys)
	}
	if decoder.Pending() {
		t.Fatalf("%q left bytes pending", input)
	}
	return keys[0]
}

func TestDecoderReadsSGRMouseReports(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want *MouseEvent
		kind Kind // the legacy Kind kept for compatibility
	}{
		{"left press", "\x1b[<0;7;12M", &MouseEvent{Kind: MousePress, Button: 0, Col: 7, Row: 12}, KindIgnore},
		{"middle press", "\x1b[<1;7;12M", &MouseEvent{Kind: MousePress, Button: 1, Col: 7, Row: 12}, KindMousePress},
		{"right press", "\x1b[<2;7;12M", &MouseEvent{Kind: MousePress, Button: 2, Col: 7, Row: 12}, KindMousePress},
		{"drag", "\x1b[<32;3;4M", &MouseEvent{Kind: MouseDrag, Button: 32, Col: 3, Row: 4}, KindMouseDrag},
		{"release", "\x1b[<0;8;13m", &MouseEvent{Kind: MouseRelease, Button: 0, Col: 8, Row: 13}, KindMouseRelease},
		{"wheel up", "\x1b[<64;5;9M", &MouseEvent{Kind: MouseWheelUp, Button: 64, Col: 5, Row: 9}, KindScrollUp},
		{"wheel down", "\x1b[<65;5;9M", &MouseEvent{Kind: MouseWheelDown, Button: 65, Col: 5, Row: 9}, KindScrollDown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := decodeOne(t, tc.in)
			if !reflect.DeepEqual(key.Mouse, tc.want) {
				t.Fatalf("%q mouse = %#v, want %#v", tc.in, key.Mouse, tc.want)
			}
			if key.Kind != tc.kind {
				t.Fatalf("%q Kind = %v, want legacy %v", tc.in, key.Kind, tc.kind)
			}
		})
	}
}

func TestDecoderIgnoresMalformedSGRMouseReports(t *testing.T) {
	// 这些正文在 M 之前不含 CSI 终结范围内的字节，因此
	// 通用转义解码器把整个正文交给 decodeSGRMouse。
	for _, input := range []string{"\x1b[<0;5M", "\x1b[<0;5;0M", "\x1b[<1;2M\x1b[<0;5M"} {
		decoder := &Decoder{}
		keys := decoder.Feed([]byte(input))
		for _, key := range keys {
			if key.Kind != KindIgnore || key.Mouse != nil {
				t.Fatalf("%q = %#v, want a plain Ignore", input, key)
			}
		}
	}
	if _, ok := decodeSGRMouse("<x;5;9", false); ok {
		t.Fatal("a non-numeric button should be rejected")
	}
}

func TestDecoderKeepsPartialSGRMousePending(t *testing.T) {
	decoder := &Decoder{}
	if keys := decoder.Feed([]byte("\x1b[<0;12;")); len(keys) != 0 || !decoder.Pending() {
		t.Fatalf("a partial mouse report should wait: %v (pending=%v)", keys, decoder.Pending())
	}
	if keys := decoder.Feed([]byte("7")); len(keys) != 0 || !decoder.Pending() {
		t.Fatalf("the report still lacks its final byte: %v (pending=%v)", keys, decoder.Pending())
	}
	keys := decoder.Feed([]byte("M"))
	if len(keys) != 1 || keys[0].Mouse == nil {
		t.Fatalf("completed report = %#v", keys)
	}
	if got := keys[0].Mouse; got.Col != 12 || got.Row != 7 {
		t.Fatalf("mouse = %#v, want col 12 row 7", got)
	}
	if decoder.Pending() {
		t.Fatal("the decoder should be drained")
	}
}

func TestDecoderMouseDoesNotChangeKeyboardDecoding(t *testing.T) {
	decoder := &Decoder{}
	keys := decoder.Feed([]byte("\x1b[Aa\x1b[<0;5;9M\x1b[B你"))
	wantKinds := []Kind{KindUp, KindRune, KindIgnore, KindDown, KindRune}
	if len(keys) != len(wantKinds) {
		t.Fatalf("got %d keys, want %d: %#v", len(keys), len(wantKinds), keys)
	}
	for i, kind := range wantKinds {
		if keys[i].Kind != kind {
			t.Errorf("keys[%d].Kind = %v, want %v", i, keys[i].Kind, kind)
		}
	}
	if keys[1].Rune != 'a' || keys[4].Rune != '你' {
		t.Fatalf("runes changed by mouse decoding: %#v", keys)
	}
	if keys[2].Mouse == nil || keys[2].Mouse.Col != 5 || keys[2].Mouse.Row != 9 {
		t.Fatalf("mouse key = %#v", keys[2])
	}
}

func TestDecoderSwallowsX10MouseWithoutRunes(t *testing.T) {
	// 用户实测的乱码来源：X10 的三个坐标字节（0x20 偏移的可打印
	// ASCII）曾被逐个当作 rune 插入输入框，例如 ";G;#G;"。
	decoder := &Decoder{}
	for _, key := range decoder.Feed([]byte("\x1b[M\"'&")) {
		if key.Kind == KindRune {
			t.Fatalf("X10 coordinate bytes leaked as a rune: %#v", key)
		}
	}
	if decoder.Pending() {
		t.Fatal("a complete X10 report left bytes pending")
	}
}

func TestDecoderSwallowsX10MouseSplitAcrossChunks(t *testing.T) {
	decoder := &Decoder{}
	for _, chunk := range []string{"\x1b[M", "\x22", "\x27\x26"} {
		for _, key := range decoder.Feed([]byte(chunk)) {
			if key.Kind == KindRune {
				t.Fatalf("a split X10 report leaked a rune: %#v", key)
			}
		}
	}
	if decoder.Pending() {
		t.Fatal("a split X10 report left bytes pending")
	}
}

func TestDecoderX10MapsMouse(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want *MouseEvent
		kind Kind // 保留的历史 Kind 值
	}{
		{"left press", "\x1b[M\x20\x27\x2c", &MouseEvent{Kind: MousePress, Button: 0, Col: 7, Row: 12}, KindIgnore},
		{"middle press", "\x1b[M\x21\x27\x2c", &MouseEvent{Kind: MousePress, Button: 1, Col: 7, Row: 12}, KindMousePress},
		{"right press", "\x1b[M\x22\x27\x2c", &MouseEvent{Kind: MousePress, Button: 2, Col: 7, Row: 12}, KindMousePress},
		{"alt-modified right press", "\x1b[M\x2a\x27\x2c", &MouseEvent{Kind: MousePress, Button: 10, Col: 7, Row: 12}, KindMousePress},
		{"drag", "\x1b[M\x40\x27\x2c", &MouseEvent{Kind: MouseDrag, Button: 32, Col: 7, Row: 12}, KindMouseDrag},
		{"release", "\x1b[M\x23\x27\x2c", &MouseEvent{Kind: MouseRelease, Button: 3, Col: 7, Row: 12}, KindMouseRelease},
		{"wheel up", "\x1b[M\x60\x27\x2c", &MouseEvent{Kind: MouseWheelUp, Button: 64, Col: 7, Row: 12}, KindScrollUp},
		{"wheel down", "\x1b[M\x61\x27\x2c", &MouseEvent{Kind: MouseWheelDown, Button: 65, Col: 7, Row: 12}, KindScrollDown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := decodeOne(t, tc.in)
			if !reflect.DeepEqual(key.Mouse, tc.want) {
				t.Fatalf("%q mouse = %#v, want %#v", tc.in, key.Mouse, tc.want)
			}
			if key.Kind != tc.kind {
				t.Fatalf("%q Kind = %v, want %v", tc.in, key.Kind, tc.kind)
			}
		})
	}
}

func TestMalformedSGRNoRunes(t *testing.T) {
	// 正文里的 'b' 落在 CSI 终结符范围内，通用循环会提前 cut 在它
	// 那里；必须由结尾的 M 定界，整包当作 Ignore 吞掉。
	decoder := &Decoder{}
	for _, key := range decoder.Feed([]byte("\x1b[<bad;xM")) {
		if key.Kind == KindRune || key.Mouse != nil {
			t.Fatalf("malformed SGR report = %#v, want a plain Ignore", key)
		}
	}
	if decoder.Pending() {
		t.Fatal("a terminated malformed SGR report left bytes pending")
	}
}

func TestRunEnablesSGR1006Not1016(t *testing.T) {
	var out bytes.Buffer
	if err := Run(context.Background(), Options{
		IO: &IO{In: strings.NewReader(""), Out: &out},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	written := out.String()
	if !strings.Contains(written, "?1006h") || !strings.Contains(written, "?1006l") {
		t.Fatalf("SGR 1006 mode was not enabled and restored: %q", written)
	}
	if strings.Contains(written, "?1016h") || strings.Contains(written, "?1016l") {
		t.Fatalf("pixel-coordinate mode 1016 must not be used: %q", written)
	}
	if !strings.Contains(written, "?1002h") || !strings.Contains(written, "?2004h") {
		t.Fatalf("drag tracking or bracketed paste was lost: %q", written)
	}
}

func TestSelectionStateMachineTracksDownDragUp(t *testing.T) {
	m := New(Config{})
	if m.HasSelection() || m.SelectionActive() {
		t.Fatal("a fresh model should have no selection")
	}
	m.MouseDrag(4, 4) // stray drag before a press is ignored
	if m.HasSelection() {
		t.Fatal("a drag without MouseDown must not start a selection")
	}
	m.MouseDown(2, 3)
	if !m.SelectionActive() {
		t.Fatal("MouseDown should start a drag")
	}
	m.MouseDrag(6, 7)
	if !m.HasSelection() {
		t.Fatal("a drag should create a range")
	}
	m.MouseUp(8, 9)
	if m.SelectionActive() {
		t.Fatal("MouseUp should end the drag")
	}
	if !m.HasSelection() {
		t.Fatal("the range should survive MouseUp so it can be copied")
	}
	m.ClearSelection()
	if m.HasSelection() || m.SelectionActive() {
		t.Fatal("ClearSelection should drop the range")
	}
}

func TestSelectedTextExtractsSingleAndMultiLineRanges(t *testing.T) {
	buf := NewBuffer(20, 5)
	buf.Clear()
	buf.WriteString(0, 0, 20, "hello world", Style{})
	buf.WriteString(0, 1, 20, "second line", Style{})

	m := New(Config{})
	if got := m.SelectedText(buf); got != "" {
		t.Fatalf("no selection should copy nothing, got %q", got)
	}

	m.MouseDown(0, 0)
	m.MouseUp(4, 0)
	if got := m.SelectedText(buf); got != "hello" {
		t.Fatalf("single-line selection = %q, want %q", got, "hello")
	}

	// 从第 1 行第 2 列向后拖拽到第 0 行第 6 列：端点会
	// 被归一化，因此仍复制 "world\nsec"。
	m.MouseDown(2, 1)
	m.MouseDrag(6, 0)
	m.MouseUp(6, 0)
	if got := m.SelectedText(buf); got != "world\nsec" {
		t.Fatalf("multi-line selection = %q, want %q", got, "world\nsec")
	}
}

func TestCopyToClipboardIsBestEffort(t *testing.T) {
	saved := clipboardWrite
	defer func() { clipboardWrite = saved }()

	var got string
	clipboardWrite = func(text string) error {
		got = text
		return nil
	}
	if err := CopyToClipboard("copied"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "copied" {
		t.Fatalf("clipboard backend got %q, want %q", got, "copied")
	}

	// 缺失/失败的 clip.exe 必须表现为错误，绝不 panic。
	clipboardWrite = func(string) error { return errors.New("clip.exe missing") }
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("CopyToClipboard panicked: %v", r)
			}
		}()
		_ = CopyToClipboard("text")
	}()
}
