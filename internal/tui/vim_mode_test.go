package tui

import (
	"testing"
	"unicode/utf8"
)

// 本文件为 R18 vim 接线准备"原语组合"的行为测试：VimMotion 以字节索引
// 工作，而 Editor 的游标是 rune 索引，两者之间必须经由显式换算衔接。
// 这些测试不依赖主接线文件（model_input.go / model.go / run.go），
// 只验证 VimMotion 与 Editor.SetCursor 组合后的不变量：
//   - rune 索引与字节索引双向换算正确（"你😀好" 中 rune1 ↔ byte3）；
//   - 越界游标被钳制，空/零值安全；
//   - CJK/emoji 绝不会被切半个 rune；
//   - motion 与 SetCursor 都不改变文本。

// vimModeMapper 在 rune 索引与字节索引之间双向换算。
// VimMotion 返回的字节索引总是落在合法 rune 边界上，因此换算无需吸附。
type vimModeMapper struct {
	text  string
	runes []rune
	offs  []int // offs[i] 是第 i 个 rune 的起始字节；offs[len(runes)]==len(text)
}

func newVimModeMapper(text string) vimModeMapper {
	m := vimModeMapper{text: text, runes: []rune(text), offs: []int{0}}
	byteIdx := 0
	for _, r := range m.runes {
		byteIdx += utf8.RuneLen(r)
		m.offs = append(m.offs, byteIdx)
	}
	return m
}

// runeToByte 把 rune 索引换算成字节索引，并钳制到 [0, len(text)]。
func (m vimModeMapper) runeToByte(runeIdx int) int {
	if runeIdx < 0 {
		runeIdx = 0
	} else if runeIdx > len(m.runes) {
		runeIdx = len(m.runes)
	}
	return m.offs[runeIdx]
}

// byteToRune 把合法 rune 边界上的字节索引换算回 rune 索引。
func (m vimModeMapper) byteToRune(byteIdx int) int {
	if byteIdx < 0 {
		byteIdx = 0
	} else if byteIdx > len(m.text) {
		byteIdx = len(m.text)
	}
	for i := len(m.offs) - 1; i >= 0; i-- {
		if m.offs[i] <= byteIdx {
			return i
		}
	}
	return 0
}

// vimModeApply 模拟主会话的接线流程：把编辑器的 rune 光标换算成字节索引，
// 调用 VimMotion，再把结果的字节索引换算回 rune 索引写回编辑器。
// motion 不改变文本，故这里只同步光标，不触碰其他状态。
func vimModeApply(e *Editor, key string) bool {
	m := newVimModeMapper(e.Text())
	_, byteCursor, handled := VimMotion(m.text, m.runeToByte(e.Cursor()), key)
	if !handled {
		return false
	}
	e.SetCursor(m.byteToRune(byteCursor))
	return true
}

// TestEditorSetCursorClamps 验证 SetCursor 把游标钳制到 [0, len(runes)]，
// 且对零值与空缓冲区安全。
func TestEditorSetCursorClamps(t *testing.T) {
	e := NewEditor()
	e.SetText("你😀好") // 3 个 rune
	for _, tc := range []struct {
		in   int
		want int
	}{
		{-999, 0},
		{-1, 0},
		{0, 0},
		{1, 1},
		{2, 2},
		{3, 3},
		{4, 3},
		{999, 3},
	} {
		e.SetCursor(tc.in)
		if got := e.Cursor(); got != tc.want {
			t.Errorf("SetCursor(%d): cursor = %d, want %d", tc.in, got, tc.want)
		}
	}

	// 零值编辑器与清空后的编辑器：len(runes)==0，任何输入都归零。
	var zero Editor
	zero.SetCursor(7)
	if got := zero.Cursor(); got != 0 {
		t.Errorf("零值编辑器 SetCursor(7): cursor = %d, want 0", got)
	}
	empty := NewEditor()
	empty.SetText("abc")
	empty.Clear()
	empty.SetCursor(5)
	if got := empty.Cursor(); got != 0 {
		t.Errorf("空缓冲区 SetCursor(5): cursor = %d, want 0", got)
	}
}

// TestEditorSetCursorPreservesOtherState 验证 SetCursor 只改游标，
// 不影响文本、历史与浏览状态。
func TestEditorSetCursorPreservesOtherState(t *testing.T) {
	e := NewEditor()
	e.SetText("hello")
	e.Record("hello")
	e.Record("world")
	if !e.HistoryPrev() {
		t.Fatal("准备阶段：HistoryPrev 应成功")
	}
	beforeText, beforeIndex := e.Text(), e.index
	beforeHistory := append([]string(nil), e.history...)

	e.SetCursor(2)

	if got := e.Cursor(); got != 2 {
		t.Errorf("cursor = %d, want 2", got)
	}
	if got := e.Text(); got != beforeText {
		t.Errorf("文本被改动: got %q, want %q", got, beforeText)
	}
	if e.index != beforeIndex {
		t.Errorf("浏览状态被改动: index = %d, want %d", e.index, beforeIndex)
	}
	if len(e.history) != len(beforeHistory) {
		t.Fatalf("历史长度被改动: got %d, want %d", len(e.history), len(beforeHistory))
	}
	for i := range beforeHistory {
		if e.history[i] != beforeHistory[i] {
			t.Errorf("历史[%d] 被改动: got %q, want %q", i, e.history[i], beforeHistory[i])
		}
	}
}

// TestVimModeRuneByteMapping 验证两个索引空间双向换算：
// 以 "你😀好" 为例，rune1 ↔ byte3、rune2 ↔ byte7、rune3 ↔ byte10。
func TestVimModeRuneByteMapping(t *testing.T) {
	text := "你😀好"
	m := newVimModeMapper(text)
	want := map[int]int{0: 0, 1: 3, 2: 7, 3: 10}
	for runeIdx, byteIdx := range want {
		if got := m.runeToByte(runeIdx); got != byteIdx {
			t.Errorf("runeToByte(%d) = %d, want %d", runeIdx, got, byteIdx)
		}
		if got := m.byteToRune(byteIdx); got != runeIdx {
			t.Errorf("byteToRune(%d) = %d, want %d", byteIdx, got, runeIdx)
		}
	}

	// 双向往返：对每一个 rune 索引，byte→rune 必须还原原值。
	for r := 0; r <= len(m.runes); r++ {
		if got := m.byteToRune(m.runeToByte(r)); got != r {
			t.Errorf("往返失败: rune %d -> byte %d -> rune %d", r, m.runeToByte(r), got)
		}
	}
}

// TestVimModeMotionRoundTrip 验证组合流程：h/l 在 rune 空间每次恰好
// 移动一个 rune，且换算后的光标与直接以字节调用 VimMotion 一致。
func TestVimModeMotionRoundTrip(t *testing.T) {
	text := "你😀好"
	m := newVimModeMapper(text)

	// 从 rune 0 出发，连续 l 应依次停在 rune 1、2、3（对应 byte 3、7、10）。
	for step, wantRune := range []int{1, 2, 3} {
		_, wantByte, handled := VimMotion(text, m.runeToByte(step), "l")
		if !handled {
			t.Fatalf("第 %d 步：l 应被处理", step)
		}
		if wantByte != m.runeToByte(wantRune) {
			t.Fatalf("第 %d 步：字节光标 = %d, want %d", step, wantByte, m.runeToByte(wantRune))
		}

		e := NewEditor()
		e.SetText(text)
		e.SetCursor(step)
		if !vimModeApply(&e, "l") {
			t.Fatalf("第 %d 步：vimModeApply(l) 未处理", step)
		}
		if got := e.Cursor(); got != wantRune {
			t.Errorf("第 %d 步 l: cursor = %d, want rune %d (byte %d)", step, got, wantRune, wantByte)
		}
	}

	// 从末尾 rune 3 出发，h 应回到 rune 2（byte 7）。
	e := NewEditor()
	e.SetText(text)
	e.SetCursor(3)
	if !vimModeApply(&e, "h") {
		t.Fatal("h 应被处理")
	}
	if got := e.Cursor(); got != 2 {
		t.Errorf("末尾 h: cursor = %d, want 2 (byte 7)", got)
	}
}

// TestVimModeCJKNotSplit 验证 motion + SetCursor 后光标始终落在 rune 边界，
// 且缓冲区 rune 序列不变——CJK/emoji 不会被切成两半。
func TestVimModeCJKNotSplit(t *testing.T) {
	text := "你😀好"
	original := []rune(text)

	// 在每个 rune 边界上走一遍 l / h，检查落点仍为合法边界、文本不变。
	m := newVimModeMapper(text)
	for runeIdx, byteIdx := range m.offs {
		for _, key := range []string{"l", "h"} {
			gotText, gotByte, handled := VimMotion(text, byteIdx, key)
			if !handled {
				t.Fatalf("rune %d key %q: motion 未被处理", runeIdx, key)
			}
			if gotText != text {
				t.Fatalf("rune %d key %q: motion 改了文本: %q", runeIdx, key, gotText)
			}
			if gotByte < 0 || gotByte > len(text) || (gotByte < len(text) && !utf8.RuneStart(text[gotByte])) {
				t.Fatalf("rune %d key %q: 字节光标 %d 不在 rune 边界", runeIdx, key, gotByte)
			}

			e := NewEditor()
			e.SetText(text)
			e.SetCursor(runeIdx)
			vimModeApply(&e, key)
			if got := e.Runes(); !equalRunes(got, original) {
				t.Fatalf("rune %d key %q: 缓冲区 rune 被改动: %v", runeIdx, key, got)
			}
			if got := e.Cursor(); got < 0 || got > len(e.Runes()) {
				t.Fatalf("rune %d key %q: cursor %d 越界", runeIdx, key, got)
			}
		}
	}
}

// TestVimModeMotionKeepsText 验证组合流程对多种键与含 CJK/换行的文本
// 都不改变文本内容。
func TestVimModeMotionKeepsText(t *testing.T) {
	texts := []string{"hello world", "你😀好", "foo,你好 bar", "ab\n你😀cd"}
	keys := []string{"h", "l", "0", "^", "$", "w", "b", "i", "a", "I", "A"}
	for _, text := range texts {
		for _, key := range keys {
			e := NewEditor()
			e.SetText(text)
			e.SetCursor(0)
			vimModeApply(&e, key)
			if got := e.Text(); got != text {
				t.Errorf("text=%q key=%q: 文本被改动: %q", text, key, got)
			}
		}
	}
}

// equalRunes 逐元素比较两个 rune 切片。
func equalRunes(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
