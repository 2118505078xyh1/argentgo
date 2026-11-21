package tui

import "testing"

// motionCase 是一条 vim motion 用例。cursor 与 wantCursor 均为字节索引。
type motionCase struct {
	name       string
	text       string
	cursor     int
	key        string
	wantCursor int
	wantHandle bool
}

// checkMotion 运行一条用例，并断言文本永不被 motion 修改。
func checkMotion(t *testing.T, tc motionCase) {
	t.Helper()
	gotText, gotCursor, gotHandled := VimMotion(tc.text, tc.cursor, tc.key)
	if gotText != tc.text {
		t.Fatalf("%s: text changed: got %q, want %q", tc.name, gotText, tc.text)
	}
	if gotCursor != tc.wantCursor {
		t.Errorf("%s: cursor = %d, want %d (text %q, key %q)", tc.name, gotCursor, tc.wantCursor, tc.text, tc.key)
	}
	if gotHandled != tc.wantHandle {
		t.Errorf("%s: handled = %v, want %v (key %q)", tc.name, gotHandled, tc.wantHandle, tc.key)
	}
}

func TestVimMotionASCII(t *testing.T) {
	cases := []motionCase{
		{"h at start stays", "hello world", 0, "h", 0, true},
		{"h moves left one rune", "hello world", 6, "h", 5, true},
		{"l moves right one rune", "hello world", 0, "l", 1, true},
		{"l at end stays", "hello world", 11, "l", 11, true},
		{"zero at middle goes line start", "hello world", 6, "0", 0, true},
		{"dollar at start goes line end", "hello world", 0, "$", 11, true},
		{"w from first word", "hello world", 0, "w", 6, true},
		{"w from second word goes end", "hello world", 6, "w", 11, true},
		{"b from second word", "hello world", 6, "b", 0, true},
		{"b at end of first word", "hello world", 5, "b", 0, true},
		{"i keeps cursor", "hello world", 6, "i", 6, true},
		{"a steps forward", "hello world", 6, "a", 7, true},
		{"a at end stays", "hello world", 11, "a", 11, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { checkMotion(t, tc) })
	}
}

func TestVimMotionRuneBoundaries(t *testing.T) {
	// 你(0-2) 😀(3-6) 好(7-9)，合法 rune 边界为 0/3/7/10。
	cases := []motionCase{
		{"l over CJK", "你😀好", 0, "l", 3, true},
		{"l over emoji", "你😀好", 3, "l", 7, true},
		{"l past last rune clamps", "你😀好", 7, "l", 10, true},
		{"h over emoji", "你😀好", 7, "h", 3, true},
		{"h over CJK", "你😀好", 3, "h", 0, true},
		{"h at start clamps", "你😀好", 0, "h", 0, true},
		{"a steps over emoji", "你😀好", 3, "a", 7, true},
		{"dollar with CJK", "你😀好", 0, "$", 10, true},
		{"zero with CJK", "你😀好", 10, "0", 0, true},
		{"b over CJK word", "你好 世界", 10, "b", 7, true},
		{"h ignores mid-rune offset", "你好", 1, "h", 0, true},
		{"l ignores mid-rune offset", "你好", 4, "l", 6, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { checkMotion(t, tc) })
	}
}

func TestVimMotionWords(t *testing.T) {
	// "foo,你好 bar"：foo(0-2) ,(3) 你(4-6) 好(7-9) 空格(10) bar(11-13)。
	// 关键字（foo、你好、bar）与标点（,）分属不同词类。
	cases := []motionCase{
		{"w crosses class boundary", "foo,你好 bar", 0, "w", 3, true},
		{"w from punctuation to CJK", "foo,你好 bar", 3, "w", 4, true},
		{"w skips space to word", "foo,你好 bar", 4, "w", 11, true},
		{"b from CJK to punctuation", "foo,你好 bar", 4, "b", 3, true},
		{"b from punctuation to ascii word", "foo,你好 bar", 3, "b", 0, true},
		{"b from later word back to CJK", "foo,你好 bar", 11, "b", 4, true},
		{"w at end stays", "foo,你好 bar", 14, "w", 14, true},
		{"b at start stays", "foo,你好 bar", 0, "b", 0, true},
		{"w skips leading spaces", "  alpha", 0, "w", 2, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { checkMotion(t, tc) })
	}
}

func TestVimMotionMultiline(t *testing.T) {
	cases := []motionCase{
		{"zero goes to current line start", "ab\ncd", 4, "0", 3, true},
		{"dollar goes to current line end", "ab\ncd", 3, "$", 5, true},
		{"dollar before newline", "ab\ncd", 1, "$", 2, true},
		{"zero on first line", "ab\ncd", 1, "0", 0, true},
		{"caret skips indentation", "   hi", 5, "^", 3, true},
		{"capital I inserts at first non-blank", "   hi", 4, "I", 3, true},
		{"capital A appends at line end", "abc   ", 0, "A", 6, true},
		{"caret at end of line", "   hi", 4, "^", 3, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { checkMotion(t, tc) })
	}
}

func TestVimMotionBounds(t *testing.T) {
	cases := []motionCase{
		{"empty text h", "", 0, "h", 0, true},
		{"empty text l", "", 0, "l", 0, true},
		{"empty text w", "", 0, "w", 0, true},
		{"empty text b", "", 0, "b", 0, true},
		{"empty text dollar", "", 0, "$", 0, true},
		{"negative cursor clamps to zero", "abc", -5, "l", 1, true},
		{"overflow cursor clamps to end", "abc", 100, "h", 2, true},
		{"overflow cursor with insert clamps", "abc", 100, "a", 3, true},
		{"mid-rune cursor snaps down", "你好", 2, "l", 3, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { checkMotion(t, tc) })
	}
}

func TestVimMotionUnhandledKeys(t *testing.T) {
	for _, key := range []string{"j", "k", "x", "", "HH", " ", "0 "} {
		t.Run("key_"+key, func(t *testing.T) {
			checkMotion(t, motionCase{
				name:       "unhandled " + key,
				text:       "abc",
				cursor:     1,
				key:        key,
				wantCursor: 1,
				wantHandle: false,
			})
		})
	}
}

func TestVimMotionPureAndDeterministic(t *testing.T) {
	text := "foo,你好 bar"
	snapshot := append([]byte(nil), text...)
	cursor := 4

	firstText, firstCursor, firstHandled := VimMotion(text, cursor, "w")
	for i := 0; i < 3; i++ {
		gotText, gotCursor, gotHandled := VimMotion(text, cursor, "w")
		if gotText != firstText || gotCursor != firstCursor || gotHandled != firstHandled {
			t.Fatalf("call %d is not deterministic: got (%q, %d, %v), want (%q, %d, %v)",
				i, gotText, gotCursor, gotHandled, firstText, firstCursor, firstHandled)
		}
	}
	if string(snapshot) != text {
		t.Fatalf("input text was mutated: got %q, want %q", text, string(snapshot))
	}
	if cursor != 4 {
		t.Fatalf("input cursor was mutated: got %d, want 4", cursor)
	}
}
