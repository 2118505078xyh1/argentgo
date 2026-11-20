package tui

import (
	"strings"
	"testing"
)

// 表格单元格截断辅助函数是包中最后一个仍逐个 rune
// 遍历的宽度路径。其他所有地方（TextWidth、Clip、Wrap、
// Buffer.WriteString、wrapInput）都按字素簇测量，使 ZWJ emoji、
// 旗帜或组合序列在切割处不会留下悬空的连接符或半面
// 旗帜。这些测试为表格单元格固定同样的规则。

func TestTruncateTableCellKeepsZWJClusterIntact(t *testing.T) {
	family := familyZWJ // 👨‍👩‍👧, one 2-cell cluster
	text := family + "中"
	// 共 4 个单元格；3 个单元格的预算容得下整个家庭 emoji，但容不下
	// 其后的 CJK rune。按 rune 遍历的循环在第一个
	// 图形处停止，在省略号前留下悬空的 ZWJ。
	got := truncateTableCell(text, 3, TextWidth)
	want := family + "…"
	if got != want {
		t.Fatalf("truncateTableCell(%q, 3) = %q, want %q", text, got, want)
	}
	if strings.Contains(strings.TrimSuffix(got, "…"), "\u200d") &&
		!strings.HasSuffix(strings.TrimSuffix(got, "…"), family) {
		t.Fatalf("cut left a dangling ZWJ: %q", got)
	}
}

func TestClipTableCellDropsWholeFlag(t *testing.T) {
	text := flagCN + "中" // flag (2 cells) + CJK (2 cells)
	// 单单元格列容不下双单元格的旗帜。必须丢弃整个簇，
	// 而不是在第一个区域指示符后截断。
	got := clipTableCell(text, 1, TextWidth)
	if got != "" {
		t.Fatalf("clipTableCell(%q, 1) = %q, want empty (no half a flag)", text, got)
	}
}

func TestRenderMarkdownTableKeepsGraphemesWithSpaceOnlyBreaker(t *testing.T) {
	// 调用方可以注入一个只在空白处断开、且不硬断长 token 的
	// breaker。使用这样的 breaker 时，整个单元格会到达
	// truncateTableCell，后者仍必须在字素边界处切割。
	breaker := func(s string, width int) []string { return strings.Fields(s) }
	family := familyZWJ
	lines := []string{
		"| h |",
		"| --- |",
		"| " + family + "中 |",
	}
	got := renderMarkdownTable(lines, 7, TextWidth, breaker)
	want := []string{
		"| h   |",
		"| --- |",
		"| " + family + "… |",
	}
	wantRows := strings.Join(want, "\n")
	if rows := strings.Join(got, "\n"); rows != wantRows {
		t.Fatalf("renderMarkdownTable() =\n%s\nwant\n%s", rows, wantRows)
	}
	for _, row := range got {
		if strings.Contains(strings.ReplaceAll(row, family, ""), "\u200d") {
			t.Fatalf("row %q has a dangling ZWJ", row)
		}
	}
}
