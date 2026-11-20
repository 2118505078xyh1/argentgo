package tui

import "testing"

// 输入框使用与记录相同的字素边界规则：ZWJ
// 家庭 emoji、区域指示符旗帜和组合序列都是单一
// 可见字形，绝不能被折到两行。wrapInput 过去
// 逐个 rune 测量，因此 ZWJ 序列中的第二个图形（或
// 旗帜的第二个字母）可能被推到下一行，留下悬空的
// 连接符 / 半面旗帜。
func TestWrapInputKeepsGraphemeClustersWhole(t *testing.T) {
	family := "A👨‍👩‍👧B" // A + family emoji + B, 1+2+1 = 4 cells
	rows := wrapInput([]rune(family), 4)
	if len(rows) != 1 {
		t.Fatalf("wrapping %q at 4 cells = %d rows %+v, want one whole row", family, len(rows), rows)
	}
	if rows[0].text != family {
		t.Fatalf("row text = %q, want %q", rows[0].text, family)
	}
}

func TestWrapInputDoesNotSplitFlagPair(t *testing.T) {
	text := "X🇺🇸Y" // X + US flag + Y
	rows := wrapInput([]rune(text), 2)
	if len(rows) != 3 {
		t.Fatalf("wrapping %q at 2 cells = %d rows %+v, want 3 whole rows", text, len(rows), rows)
	}
	want := []string{"X", "🇺🇸", "Y"}
	for i, row := range rows {
		if row.text != want[i] {
			t.Errorf("row %d = %q, want %q", i, row.text, want[i])
		}
	}
}

// 显式换行和普通宽/CJK 文本必须保持旧行为。
func TestWrapInputStillFoldsLinesAndWideRunes(t *testing.T) {
	rows := wrapInput([]rune("你ab\ncd"), 3)
	want := []string{"你a", "b", "cd"}
	if len(rows) != len(want) {
		t.Fatalf("rows = %d %+v, want %d", len(rows), rows, len(want))
	}
	for i, row := range rows {
		if row.text != want[i] {
			t.Errorf("row %d = %q, want %q", i, row.text, want[i])
		}
	}
}
