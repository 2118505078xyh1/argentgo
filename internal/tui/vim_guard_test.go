package tui

import (
	"math/rand"
	"testing"
	"unicode"
	"unicode/utf8"
)

// 本文件对 VimMotion 做属性守护：不碰具体实现，仅从外部契约出发，
// 用随机 rune 串与一份独立参考实现逐字节对拍，覆盖文本不变、rune 边界、
// 越界钳制、mid-rune 吸附、确定性与 j/k 未处理等关键不变量。

// guardRefText 是被测文本的独立解码视图。它把字节索引映射为 rune 索引，
// 供参考实现以 rune 为单位推导 motion 结果，从而与生产实现解耦。
type guardRefText struct {
	rs     []rune
	offs   []int       // offs[i] 是第 i 个 rune 的起始字节；offs[len(rs)]==len(text)
	byByte map[int]int // 合法 rune 边界字节 -> rune 索引
	nbytes int
}

// guardNewRef 解码 text，建立字节边界到 rune 索引的映射。
// 本测试只喂合法 UTF-8 串，因此每个边界都唯一且有序。
func guardNewRef(text string) guardRefText {
	rt := guardRefText{
		offs:   []int{0},
		byByte: map[int]int{0: 0},
		nbytes: len(text),
	}
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		i += size
		rt.rs = append(rt.rs, r)
		rt.offs = append(rt.offs, i)
		rt.byByte[i] = len(rt.rs)
	}
	return rt
}

// clamp 模拟生产的钳制语义：负数归零、超长归末尾、落在 rune 中间则
// 向下吸附到最近的合法边界。
func (rt guardRefText) clamp(cursor int) int {
	if cursor < 0 {
		return 0
	}
	if cursor >= rt.nbytes {
		return rt.nbytes
	}
	for k := len(rt.offs) - 1; k >= 0; k-- {
		if rt.offs[k] <= cursor {
			return rt.offs[k]
		}
	}
	return 0
}

// motion 是 VimMotion 的独立参考实现：先在 rune 索引空间算出目标 rune，
// 再映射回字节边界。返回值依次为字节索引与 handled。
func (rt guardRefText) motion(cursor int, key string) (int, bool) {
	c := rt.clamp(cursor)
	ci := rt.byByte[c]
	n := len(rt.rs)
	switch key {
	case "h":
		if ci > 0 {
			ci--
		}
		return rt.offs[ci], true
	case "l", "a":
		if ci < n {
			ci++
		}
		return rt.offs[ci], true
	case "i":
		return c, true
	case "0":
		return rt.offs[rt.lineStart(ci)], true
	case "$", "A":
		return rt.offs[rt.lineEnd(ci)], true
	case "^", "I":
		return rt.offs[rt.firstNonBlank(ci)], true
	case "w":
		return rt.offs[rt.nextWord(ci)], true
	case "b":
		return rt.offs[rt.prevWord(ci)], true
	default:
		return c, false
	}
}

func (rt guardRefText) lineStart(ci int) int {
	for ci > 0 && rt.rs[ci-1] != '\n' {
		ci--
	}
	return ci
}

func (rt guardRefText) lineEnd(ci int) int {
	for ci < len(rt.rs) && rt.rs[ci] != '\n' {
		ci++
	}
	return ci
}

func (rt guardRefText) firstNonBlank(ci int) int {
	ci = rt.lineStart(ci)
	for ci < len(rt.rs) {
		r := rt.rs[ci]
		if r == '\n' || r == '\r' || (r != ' ' && r != '\t') {
			break
		}
		ci++
	}
	return ci
}

func guardIsBlank(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	}
	return false
}

func guardClass(r rune) int {
	if r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
		return 1
	}
	return 2
}

func (rt guardRefText) nextWord(ci int) int {
	n := len(rt.rs)
	if ci >= n {
		return n
	}
	if guardIsBlank(rt.rs[ci]) {
		for ci < n && guardIsBlank(rt.rs[ci]) {
			ci++
		}
		return ci
	}
	cls := guardClass(rt.rs[ci])
	for ci < n && !guardIsBlank(rt.rs[ci]) && guardClass(rt.rs[ci]) == cls {
		ci++
	}
	for ci < n && guardIsBlank(rt.rs[ci]) {
		ci++
	}
	return ci
}

func (rt guardRefText) prevWord(ci int) int {
	if ci <= 0 {
		return 0
	}
	j := ci
	for j > 0 && guardIsBlank(rt.rs[j-1]) {
		j--
	}
	if j <= 0 {
		return 0
	}
	cls := guardClass(rt.rs[j-1])
	for j > 0 && !guardIsBlank(rt.rs[j-1]) && guardClass(rt.rs[j-1]) == cls {
		j--
	}
	return j
}

// guardRunePool 覆盖 ASCII、关键字、空白、换行、CJK、emoji、ZWJ 与组合符，
// 确保随机串包含单字节与多字节 rune 的混合物。
var guardRunePool = []rune{
	'a', 'b', 'Z', '0', '_', ' ', '\t', '\n', '.', ',',
	'你', '好', '世', '界', '😀', '🎉', '👨', '\u200d', '\u0301', 'é', 'Ω',
}

// guardKeys 覆盖全部已实现 motion 以及若干未处理键。
var guardKeys = []string{"h", "l", "0", "^", "$", "w", "b", "i", "a", "I", "A", "j", "k", "x", ""}

// guardValidBoundary 报告 pos 是否为 text 的合法 rune 边界（含末尾）。
func guardValidBoundary(text string, pos int) bool {
	if pos < 0 || pos > len(text) {
		return false
	}
	return pos == len(text) || utf8.RuneStart(text[pos])
}

// TestVimGuardAgainstReference 随机串逐字节对拍参考实现：文本、光标、
// handled 三者必须一致，同时校验不变性与确定性。
func TestVimGuardAgainstReference(t *testing.T) {
	rng := rand.New(rand.NewSource(20261121))
	for iter := 0; iter < 3000; iter++ {
		n := rng.Intn(25)
		rr := make([]rune, n)
		for i := range rr {
			rr[i] = guardRunePool[rng.Intn(len(guardRunePool))]
		}
		text := string(rr)
		cursor := rng.Intn(len(text)+11) - 5 // 含负数与越界，也含 mid-rune
		key := guardKeys[rng.Intn(len(guardKeys))]

		rt := guardNewRef(text)
		wantCursor, wantHandled := rt.motion(cursor, key)

		gotText, gotCursor, gotHandled := VimMotion(text, cursor, key)
		if gotText != text {
			t.Fatalf("iter=%d key=%q cursor=%d text=%q: 文本被修改: got %q", iter, key, cursor, text, gotText)
		}
		if gotCursor != wantCursor || gotHandled != wantHandled {
			t.Fatalf("iter=%d key=%q cursor=%d text=%q: got (%d,%v), want (%d,%v)",
				iter, key, cursor, text, gotCursor, gotHandled, wantCursor, wantHandled)
		}
		if !guardValidBoundary(text, gotCursor) {
			t.Fatalf("iter=%d key=%q cursor=%d text=%q: 光标 %d 不在合法 rune 边界", iter, key, cursor, text, gotCursor)
		}
		// 确定性：相同入参重复调用必须逐字节一致。
		text2, cursor2, handled2 := VimMotion(text, cursor, key)
		if text2 != gotText || cursor2 != gotCursor || handled2 != gotHandled {
			t.Fatalf("iter=%d: 非确定性: 首次 (%q,%d,%v), 再次 (%q,%d,%v)",
				iter, gotText, gotCursor, gotHandled, text2, cursor2, handled2)
		}
	}
}

// TestVimGuardRuneByRuneMovement 逐个 rune 验证 CJK/emoji/ZWJ/组合符的
// h/l 移动：每次只能前进或后退一个 rune，而不是一个字节。
func TestVimGuardRuneByRuneMovement(t *testing.T) {
	text := "A你😀👨\u200d👩\u200d👧e\u0301界"
	// boundaries 按顺序列出所有合法 rune 起始字节与末尾。
	boundaries := []int{0}
	for i := range text {
		if utf8.RuneStart(text[i]) && i > 0 {
			boundaries = append(boundaries, i)
		}
	}
	boundaries = append(boundaries, len(text))

	for idx, b := range boundaries {
		if idx > 0 {
			gotText, gotCursor, handled := VimMotion(text, b, "h")
			if !handled || gotText != text || gotCursor != boundaries[idx-1] {
				t.Errorf("h from byte %d: got (%q,%d,%v), want (unchanged,%d,true)",
					b, gotText, gotCursor, handled, boundaries[idx-1])
			}
		}
		if idx+1 < len(boundaries) {
			gotText, gotCursor, handled := VimMotion(text, b, "l")
			if !handled || gotText != text || gotCursor != boundaries[idx+1] {
				t.Errorf("l from byte %d: got (%q,%d,%v), want (unchanged,%d,true)",
					b, gotText, gotCursor, handled, boundaries[idx+1])
			}
		}
	}
	// ZWJ 与组合符必须各自占一个 rune：验证相邻边界确实落在它们中间。
	foundZWJ, foundCombining := false, false
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == '\u200d' {
			gotText, gotCursor, _ := VimMotion(text, i, "l")
			if gotText != text || gotCursor != i+size {
				t.Errorf("ZWJ 后 l 应到下一个 rune，got %d want %d", gotCursor, i+size)
			}
			foundZWJ = true
		}
		if r == '\u0301' {
			foundCombining = true
		}
		i += size
	}
	if !foundZWJ || !foundCombining {
		t.Fatalf("测试文本未包含 ZWJ 或组合符: zwj=%v combining=%v", foundZWJ, foundCombining)
	}
}

// TestVimGuardOutOfRangeClamping 越界光标必须等价于钳制后的光标。
func TestVimGuardOutOfRangeClamping(t *testing.T) {
	text := "ab\n你😀cd"
	for _, cursor := range []int{-20, -7, -1, len(text), len(text) + 1, len(text) + 8} {
		clamped := cursor
		if clamped < 0 {
			clamped = 0
		} else if clamped > len(text) {
			clamped = len(text)
		}
		for _, key := range guardKeys {
			gotText, gotCursor, gotHandled := VimMotion(text, cursor, key)
			wantText, wantCursor, wantHandled := VimMotion(text, clamped, key)
			if gotText != wantText || gotCursor != wantCursor || gotHandled != wantHandled {
				t.Errorf("cursor=%d clamped=%d key=%q: got (%q,%d,%v), want (%q,%d,%v)",
					cursor, clamped, key, gotText, gotCursor, gotHandled, wantText, wantCursor, wantHandled)
			}
		}
	}
}

// TestVimGuardMidRuneSnapping 落在多字节 rune 中间的任意字节偏移，
// 必须与该 rune 起始边界给出完全一致的结果。
func TestVimGuardMidRuneSnapping(t *testing.T) {
	text := "你😀好Z世界"
	for i := 0; i < len(text); {
		_, size := utf8.DecodeRuneInString(text[i:])
		for mid := i + 1; mid < i+size; mid++ {
			for _, key := range guardKeys {
				gotText, gotCursor, gotHandled := VimMotion(text, mid, key)
				wantText, wantCursor, wantHandled := VimMotion(text, i, key)
				if gotText != wantText || gotCursor != wantCursor || gotHandled != wantHandled {
					t.Errorf("mid=%d base=%d key=%q: got (%q,%d,%v), want (%q,%d,%v)",
						mid, i, key, gotText, gotCursor, gotHandled, wantText, wantCursor, wantHandled)
				}
				if !guardValidBoundary(text, gotCursor) {
					t.Errorf("mid=%d key=%q: 光标 %d 不在合法边界", mid, key, gotCursor)
				}
			}
		}
		i += size
	}
}

// TestVimGuardJKUnhandled j/k 属于跨行历史导航，单行输入框必须返回
// handled=false，且保持钳制后的光标不变。
func TestVimGuardJKUnhandled(t *testing.T) {
	texts := []string{"abc", "你😀好", "ab\ncd", "", "  x "}
	cursors := []int{0, 1, 2, 6, -3, 999}
	for _, text := range texts {
		for _, cursor := range cursors {
			rt := guardNewRef(text)
			for _, key := range []string{"j", "k"} {
				gotText, gotCursor, handled := VimMotion(text, cursor, key)
				if handled {
					t.Errorf("text=%q cursor=%d key=%q: handled=true, want false", text, cursor, key)
				}
				if gotText != text {
					t.Errorf("text=%q cursor=%d key=%q: 文本被修改: %q", text, cursor, key, gotText)
				}
				if want := rt.clamp(cursor); gotCursor != want {
					t.Errorf("text=%q cursor=%d key=%q: 光标 %d, want %d", text, cursor, key, gotCursor, want)
				}
			}
		}
	}
}

// TestVimGuardDeterministic 固定输入重复调用，结果与入参快照都不得变化。
func TestVimGuardDeterministic(t *testing.T) {
	text := "foo,你好 bar\n baz😀"
	snapshot := append([]byte(nil), text...)
	cursor := 8
	key := "w"

	firstText, firstCursor, firstHandled := VimMotion(text, cursor, key)
	for i := 0; i < 8; i++ {
		gotText, gotCursor, gotHandled := VimMotion(text, cursor, key)
		if gotText != firstText || gotCursor != firstCursor || gotHandled != firstHandled {
			t.Fatalf("第 %d 次调用不一致: (%q,%d,%v) vs (%q,%d,%v)",
				i, gotText, gotCursor, gotHandled, firstText, firstCursor, firstHandled)
		}
	}
	if string(snapshot) != text {
		t.Fatalf("输入文本被修改: got %q, want %q", text, string(snapshot))
	}
	if cursor != 8 {
		t.Fatalf("输入 cursor 被修改: got %d, want 8", cursor)
	}
}
