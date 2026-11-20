package tui

import (
	"strings"
	"testing"
)

func TestJoinSelectedRowsSoftWrap(t *testing.T) {
	// 视口 20，阈值 12；首行接近占满，判定为软折行，和续行用空格相接。
	rows := []string{
		"a long wrapped line that fills the screen",
		"continuation",
	}
	got := joinSelectedRows(rows, 20)
	want := "a long wrapped line that fills the screen continuation"
	if got != want {
		t.Fatalf("soft wrap = %q, want %q", got, want)
	}
}

func TestJoinSelectedRowsShortRowsKeepNewline(t *testing.T) {
	// 两行都远窄于阈值时，保留原来的逐行换行。
	got := joinSelectedRows([]string{"hello", "world"}, 20)
	if want := "hello\nworld"; got != want {
		t.Fatalf("short rows = %q, want %q", got, want)
	}
}

func TestJoinSelectedRowsEmptyLineParagraph(t *testing.T) {
	got := joinSelectedRows([]string{"第一段", "", "第二段"}, 80)
	if want := "第一段\n\n第二段"; got != want {
		t.Fatalf("paragraph break = %q, want %q", got, want)
	}
}

func TestJoinSelectedRowsCJK(t *testing.T) {
	// 中文按两格计宽：6 个汉字 = 12 格，达到视口 10 的阈值 6，判定软折行。
	long := "中文中文中文"
	if got := joinSelectedRows([]string{long, "继续"}, 10); got != long+" 继续" {
		t.Fatalf("CJK soft wrap = %q, want %q", got, long+" 继续")
	}
	// 两个汉字只有 4 格，低于阈值，保持换行。
	if got := joinSelectedRows([]string{"中文", "继续"}, 20); got != "中文\n继续" {
		t.Fatalf("CJK short = %q, want %q", got, "中文\n继续")
	}
}

func TestJoinSelectedRowsGraphemeSafety(t *testing.T) {
	// 组合 emoji 与带声调的拉丁字母都不应在宽度统计或拼接处被拆坏。
	row := "café 👨‍👩‍👧‍👦 结束"
	got := joinSelectedRows([]string{row}, 80)
	if got != row {
		t.Fatalf("grapheme row = %q, want %q", got, row)
	}
}

func TestJoinSelectedRowsInlineCode(t *testing.T) {
	// codeSpanMark 成对出现时还原为反引号；落单的标记被丢弃。
	rows := []string{"call " + codeSpanMark + "fmt.Println" + codeSpanMark + " now"}
	if got := joinSelectedRows(rows, 80); got != "call `fmt.Println` now" {
		t.Fatalf("inline code = %q, want %q", got, "call `fmt.Println` now")
	}
	// 被软折行切开的代码段在拼接后仍能配对。
	split := []string{
		"使用 " + codeSpanMark + "fmt.Long",
		"FunctionName" + codeSpanMark + " 完成",
	}
	want := "使用 `fmt.Long FunctionName` 完成"
	if got := joinSelectedRows(split, 20); got != want {
		t.Fatalf("split code span = %q, want %q", got, want)
	}
}

func TestJoinSelectedRowsSingleLine(t *testing.T) {
	if got := joinSelectedRows([]string{"hello"}, 80); got != "hello" {
		t.Fatalf("single line = %q, want %q", got, "hello")
	}
}

func TestJoinSelectedRowsEmptyInput(t *testing.T) {
	for _, rows := range [][]string{nil, {}} {
		if got := joinSelectedRows(rows, 80); got != "" {
			t.Fatalf("empty input = %q, want empty", got)
		}
	}
}

func TestSelectedTextRestoresCodeSpansAndSoftWrap(t *testing.T) {
	m := New(Config{})
	codeColor := m.palette.Code
	if codeColor == ColorDefault {
		t.Fatal("测试需要一个有色的 code 样式")
	}

	// 行内代码段：普通文本 + 代码样式 + 普通文本，复制后应带回反引号。
	buf := NewBuffer(20, 3)
	buf.Clear()
	buf.WriteString(0, 0, 20, "call ", Style{})
	buf.WriteString(5, 0, 20, "fmt.Println", Style{Fg: codeColor})
	buf.WriteString(16, 0, 20, " now", Style{})
	m.MouseDown(0, 0)
	m.MouseUp(19, 0)
	if got, want := m.SelectedText(buf), "call `fmt.Println` now"; got != want {
		t.Fatalf("code span copy = %q, want %q", got, want)
	}

	// 两屏满行：复制时应合并成一行而非插入硬换行。
	long := strings.Repeat("a", 20)
	buf2 := NewBuffer(20, 3)
	buf2.Clear()
	buf2.WriteString(0, 0, 20, long, Style{})
	buf2.WriteString(0, 1, 20, strings.Repeat("b", 20), Style{})
	m.ClearSelection()
	m.MouseDown(0, 0)
	m.MouseUp(19, 1)
	if got, want := m.SelectedText(buf2), long+" "+strings.Repeat("b", 20); got != want {
		t.Fatalf("soft wrap copy = %q, want %q", got, want)
	}
}
