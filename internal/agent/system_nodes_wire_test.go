package agent

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSystemNodesEmptyLeavesSystemByteIdentical 验证未配置 SystemNodes 时，
// 首条 system 消息与接线前逐字节一致（nil 与空 slice 两条路径都覆盖）。
func TestSystemNodesEmptyLeavesSystemByteIdentical(t *testing.T) {
	cases := []struct {
		name string
		opts Options
		want string
	}{
		{
			name: "nil nodes with custom system",
			opts: Options{System: "  custom base  "},
			want: "custom base",
		},
		{
			name: "empty slice with custom system",
			opts: Options{System: "custom base", SystemNodes: []string{}},
			want: "custom base",
		},
		{
			name: "nil nodes with default template",
			opts: Options{},
			want: fmt.Sprintf(systemTemplate, "."),
		},
		{
			name: "nil nodes with explicit root",
			opts: Options{Root: "/work"},
			want: fmt.Sprintf(systemTemplate, "/work"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newRunState(tc.opts, "hi", nil)
			if got := s.messages[0].Content; got != tc.want {
				t.Fatalf("system content = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSystemNodesAppendPreservesPrefix 验证非空 nodes 按序追加，且 base 仍是结果的精确前缀。
func TestSystemNodesAppendPreservesPrefix(t *testing.T) {
	base := "base system prompt"
	nodes := []string{"rule one", "rule two"}
	s := newRunState(Options{System: base, SystemNodes: nodes}, "hi", nil)
	got := s.messages[0].Content
	want := base + "\nrule one\nrule two"
	if got != want {
		t.Fatalf("system content = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, base) {
		t.Fatalf("system content lost the stable base prefix: %q", got)
	}
}

// TestSystemNodesDedup 验证 base 已含的节点与 nodes 中重复的节点都只出现一次，
// 空节点被忽略。
func TestSystemNodesDedup(t *testing.T) {
	base := "base contains alpha rule already"
	nodes := []string{"alpha rule", "beta rule", "beta rule", ""}
	s := newRunState(Options{System: base, SystemNodes: nodes}, "hi", nil)
	got := s.messages[0].Content
	want := base + "\nbeta rule"
	if got != want {
		t.Fatalf("system content = %q, want %q", got, want)
	}
	if n := strings.Count(got, "beta rule"); n != 1 {
		t.Errorf("beta rule count = %d, want 1: %q", n, got)
	}
	if n := strings.Count(got, "alpha rule"); n != 1 {
		t.Errorf("alpha rule count = %d, want 1 (only in base): %q", n, got)
	}
}

// TestSystemNodesCJK 验证 CJK 节点作为整段追加，不切分码点，且结果仍是合法 UTF-8。
func TestSystemNodesCJK(t *testing.T) {
	base := "基础提示"
	nodes := []string{"规则甲", "规则乙"}
	s := newRunState(Options{System: base, SystemNodes: nodes}, "hi", nil)
	got := s.messages[0].Content
	want := base + "\n规则甲\n规则乙"
	if got != want {
		t.Fatalf("system content = %q, want %q", got, want)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("system content is not valid UTF-8: %q", got)
	}
	gotRunes, wantRunes := []rune(got), []rune(want)
	if len(gotRunes) != len(wantRunes) {
		t.Fatalf("rune length = %d, want %d: %q", len(gotRunes), len(wantRunes), got)
	}
	for i := range wantRunes {
		if gotRunes[i] != wantRunes[i] {
			t.Fatalf("rune %d = %q, want %q: %q", i, gotRunes[i], wantRunes[i], got)
		}
	}
}
