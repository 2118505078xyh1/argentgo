package tui

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReasoningTailWindowFoldsOverlong(t *testing.T) {
	lines := []string{"l0", "l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "l9"}
	got := reasoningTailWindow(lines, 3, "… %d 行已折叠")
	want := []string{"… 7 行已折叠", "l7", "l8", "l9"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reasoningTailWindow = %q, want %q", got, want)
	}
}

func TestReasoningTailWindowShortReturnsUnchanged(t *testing.T) {
	lines := []string{"one", "two", "three"}
	got := reasoningTailWindow(lines, 5, "… %d 行已折叠")
	if !reflect.DeepEqual(got, lines) {
		t.Fatalf("short input changed: got %q, want %q", got, lines)
	}
	// 返回的是副本，改动结果不应污染输入。
	got[0] = "mutated"
	if lines[0] != "one" {
		t.Fatalf("result aliases input: lines[0] = %q", lines[0])
	}
}

func TestReasoningTailWindowCJKAndRuneSafe(t *testing.T) {
	lines := []string{
		"第一行的思考内容",
		"第二行包含 emoji 🚀 与汉字",
		"第三行👨‍👩‍👧‍👦 是 ZWJ 序列",
		"第四行以中文结尾。",
	}
	got := reasoningTailWindow(lines, 2, "… %d 行已折叠")
	want := []string{"… 2 行已折叠", "第三行👨‍👩‍👧‍👦 是 ZWJ 序列", "第四行以中文结尾。"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CJK tail window = %q, want %q", got, want)
	}
	for _, text := range got {
		if !utf8.ValidString(text) {
			t.Fatalf("invalid UTF-8 in tail window: %q", text)
		}
	}
}

func TestReasoningTailWindowEmpty(t *testing.T) {
	for _, lines := range [][]string{nil, {}} {
		got := reasoningTailWindow(lines, 3, "… %d 行已折叠")
		if len(got) != 0 {
			t.Fatalf("empty input produced %q", got)
		}
	}
}

func TestReasoningTailWindowTailBoundaries(t *testing.T) {
	lines := []string{"a", "b", "c", "d"}
	cases := []struct {
		name string
		tail int
		want []string
	}{
		{"tail zero keeps all", 0, []string{"a", "b", "c", "d"}},
		{"tail negative keeps all", -1, []string{"a", "b", "c", "d"}},
		{"tail equals length", 4, []string{"a", "b", "c", "d"}},
		{"tail exceeds length", 9, []string{"a", "b", "c", "d"}},
		{"tail one folds the rest", 1, []string{"… 3 行已折叠", "d"}},
		{"tail just below length", 3, []string{"… 1 行已折叠", "b", "c", "d"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reasoningTailWindow(lines, tc.tail, "… %d 行已折叠")
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("tail=%d: got %q, want %q", tc.tail, got, tc.want)
			}
		})
	}
}

func TestReasoningTailWindowHintLine(t *testing.T) {
	lines := strings.Split(strings.Repeat("思考很长的一行\n", 24), "\n")
	lines = lines[:len(lines)-1] // 去掉 split 产生的末尾空行
	got := reasoningTailWindow(lines, 4, "… %d lines folded")
	if len(got) != 5 {
		t.Fatalf("hint + tail should be 5 rows, got %d: %q", len(got), got)
	}
	if got[0] != "… 20 lines folded" {
		t.Fatalf("hint line = %q, want %q", got[0], "… 20 lines folded")
	}
	for i := 1; i < len(got); i++ {
		if got[i] == got[0] {
			t.Fatalf("tail row %d duplicates the hint", i)
		}
	}
}

func TestReasoningTailWindowOptInWiring(t *testing.T) {
	original := ReasoningTailWindow
	defer func() { ReasoningTailWindow = original }()

	m := New(Config{Locale: "zh"})
	m.AppendReasoning("第一行\n第二行\n第三行\n第四行\n第五行\n第六行\n第七行\n第八行")

	ReasoningTailWindow = false
	defaultLines := m.reasoningLines(m.entries[0], 60)
	if len(defaultLines) != 1 {
		t.Fatalf("default path should stay one row, got %d", len(defaultLines))
	}

	ReasoningTailWindow = true
	windowed := m.reasoningLines(m.entries[0], 60)
	if len(windowed) < 2 {
		t.Fatalf("opt-in path should add tail rows, got %d", len(windowed))
	}
	if !strings.Contains(windowed[1].Text(), "行已折叠") {
		t.Fatalf("folded hint missing in opt-in output: %q", windowed[1].Text())
	}
	// 摘要标题本就会提取首句，因此折叠检查只针对追加的尾窗行。
	tailJoined := ""
	for _, line := range windowed[1:] {
		tailJoined += line.Text() + "\n"
	}
	if !strings.Contains(tailJoined, "第七行") || !strings.Contains(tailJoined, "第八行") {
		t.Fatalf("tail rows missing from opt-in output: %q", tailJoined)
	}
	if strings.Contains(tailJoined, "第一行") {
		t.Fatalf("folded head leaked into tail window: %q", tailJoined)
	}
}

func TestReasoningHiddenHintLocale(t *testing.T) {
	if got := reasoningHiddenHint("zh"); got != "… %d 行已折叠" {
		t.Fatalf("zh hint = %q", got)
	}
	if got := reasoningHiddenHint("en"); got != "… %d lines folded" {
		t.Fatalf("en hint = %q", got)
	}
}
