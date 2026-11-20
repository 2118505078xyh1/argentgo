package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPreviewHeadTailKeepsHeadAndTailWithMarker(t *testing.T) {
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%02d", i)
	}

	got := previewHeadTail(lines, 6)
	want := []string{
		"line-00", "line-01", "line-02",
		"… (14 lines hidden)",
		"line-17", "line-18", "line-19",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("头尾预览 = %q, 期望 %q", got, want)
	}
	// 纯函数不得改动输入切片。
	if lines[0] != "line-00" || lines[19] != "line-19" {
		t.Fatalf("输入被修改: 首=%q 尾=%q", lines[0], lines[19])
	}
}

func TestPreviewHeadTailReturnsInputWhenWithinBudget(t *testing.T) {
	lines := []string{"甲", "乙", "丙"}
	got := previewHeadTail(lines, 5)
	if !reflect.DeepEqual(got, lines) {
		t.Fatalf("行数不足应原样返回, 得到 %q", got)
	}
	// 返回的是副本，改动它不应污染输入。
	got[0] = "已改动"
	if lines[0] != "甲" {
		t.Fatalf("改动返回值污染了输入: %q", lines)
	}
}

func TestPreviewHeadTailExactBudget(t *testing.T) {
	lines := []string{"a", "b", "c", "d", "e"}
	got := previewHeadTail(lines, len(lines))
	if !reflect.DeepEqual(got, lines) {
		t.Fatalf("恰好等于预算应原样返回, 得到 %q", got)
	}
}

func TestPreviewHeadTailOneLineOver(t *testing.T) {
	lines := []string{"0", "1", "2", "3", "4", "5"}
	got := previewHeadTail(lines, 5)
	want := []string{"0", "1", "… (1 lines hidden)", "3", "4", "5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("超一行预览 = %q, 期望 %q", got, want)
	}
}

func TestPreviewHeadTailCJK(t *testing.T) {
	lines := []string{
		"汉字行一", "日本語の行", "emoji😀行",
		"错误：连接失败", "最后的结果", "收尾行",
	}
	got := previewHeadTail(lines, 4)
	want := []string{
		"汉字行一", "日本語の行",
		"… (2 lines hidden)",
		"最后的结果", "收尾行",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("中文预览 = %q, 期望 %q", got, want)
	}
	for _, s := range got {
		if !utf8.ValidString(s) {
			t.Fatalf("输出含损坏的 UTF-8: %q", s)
		}
	}
}

func TestPreviewHeadTailMaxBoundaries(t *testing.T) {
	lines := []string{"a", "b", "c", "d"}
	cases := []struct {
		name string
		max  int
		want []string
	}{
		{name: "zero", max: 0, want: nil},
		{name: "negative", max: -1, want: nil},
		{name: "one", max: 1, want: []string{"… (3 lines hidden)", "d"}},
		{name: "two", max: 2, want: []string{"a", "… (2 lines hidden)", "d"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := previewHeadTail(lines, tc.max)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("max=%d 预览 = %q, 期望 %q", tc.max, got, tc.want)
			}
		})
	}
}

func TestPreviewHeadTailEmpty(t *testing.T) {
	if got := previewHeadTail(nil, 3); len(got) != 0 {
		t.Fatalf("nil 输入应返回空, 得到 %q", got)
	}
	if got := previewHeadTail([]string{}, 3); len(got) != 0 {
		t.Fatalf("空切片应返回空, 得到 %q", got)
	}
}

func TestIsHiddenMarker(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{line: "… (14 lines hidden)", want: true},
		{line: "… (0 lines hidden)", want: true},
		{line: "… () lines hidden)", want: false},
		{line: "… (abc lines hidden)", want: false},
		{line: "… 5 more lines", want: false},
		{line: "普通输出行", want: false},
	}
	for _, tc := range cases {
		if got := isHiddenMarker(tc.line); got != tc.want {
			t.Fatalf("isHiddenMarker(%q) = %v, 期望 %v", tc.line, got, tc.want)
		}
	}
}

func TestToolLinesHeadTailPreviewOptIn(t *testing.T) {
	orig := HeadTailToolPreview
	defer func() { HeadTailToolPreview = orig }()

	raw := make([]string, 25)
	for i := range raw {
		raw[i] = fmt.Sprintf("L%02d", i)
	}
	entry := Entry{Kind: EntryTool, Name: "bash", Args: `{}`, Output: strings.Join(raw, "\n")}
	p := Palette{}

	// 默认关闭：保持旧的 "… N more lines" 预览，尾部行不可见。
	HeadTailToolPreview = false
	off := lineTexts(toolLines(entry, 60, true, p))
	if !strings.Contains(off, "… 5 more lines") {
		t.Fatalf("默认预览应含旧溢出标记, 得到:\n%s", off)
	}
	if strings.Contains(off, "L24") {
		t.Fatalf("默认预览不应显示尾部行, 得到:\n%s", off)
	}

	// 打开开关：改用头尾窗口，尾部行可见且标记为新模板。
	HeadTailToolPreview = true
	on := lineTexts(toolLines(entry, 60, true, p))
	if !strings.Contains(on, "… (5 lines hidden)") {
		t.Fatalf("头尾预览应含新标记, 得到:\n%s", on)
	}
	if !strings.Contains(on, "L24") {
		t.Fatalf("头尾预览应显示尾部行, 得到:\n%s", on)
	}
	if strings.Contains(on, "… 5 more lines") {
		t.Fatalf("头尾预览不应含旧溢出标记, 得到:\n%s", on)
	}
}
