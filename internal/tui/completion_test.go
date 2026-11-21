package tui

import (
	"reflect"
	"testing"
)

// sampleCandidates 故意乱序，便于验证排序；含 CJK 与大小写混排。
func sampleCandidates() []CompletionCandidate {
	return []CompletionCandidate{
		{Command: "config", Description: "配置"},
		{Command: "commit", Description: "提交"},
		{Command: "帮助", Description: "help"},
		{Command: "compile", Description: "编译"},
		{Command: "test", Description: "测试"},
	}
}

func commandsOf(s *CompletionState) []string {
	orig := s.index
	defer func() { s.index = orig }()

	n := s.Count()
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		// 临时改动 index 遍历所有可见候选，退出时恢复原值。
		s.index = i
		c, ok := s.Current()
		if ok {
			out = append(out, c.Command)
		}
	}
	return out
}

func TestCompletionNewNilAndEmpty(t *testing.T) {
	for _, cands := range [][]CompletionCandidate{nil, []CompletionCandidate{}} {
		s := NewCompletionState(cands)
		if s == nil {
			t.Fatal("NewCompletionState returned nil")
		}
		if got := s.Count(); got != 0 {
			t.Fatalf("Count() = %d, want 0", got)
		}
		if _, ok := s.Current(); ok {
			t.Fatal("Current() should fail with no candidates")
		}
		// 无候选时导航是安全的无操作。
		s.Next()
		s.Prev()
		if s.index != 0 {
			t.Fatalf("index = %d after Next/Prev on empty state, want 0", s.index)
		}
	}
}

func TestCompletionStableSortByCommand(t *testing.T) {
	cands := []CompletionCandidate{
		{Command: "zeta", Description: "second-z"},
		{Command: "alpha", Description: "first-a"},
		{Command: "zeta", Description: "second-z-again"},
		{Command: "beta", Description: "b"},
	}
	s := NewCompletionState(cands)
	// 同 Command 保持输入中的相对顺序（稳定排序）。
	want := []CompletionCandidate{
		{Command: "alpha", Description: "first-a"},
		{Command: "beta", Description: "b"},
		{Command: "zeta", Description: "second-z"},
		{Command: "zeta", Description: "second-z-again"},
	}
	if !reflect.DeepEqual(s.candidates, want) {
		t.Fatalf("sorted candidates = %v, want %v", s.candidates, want)
	}
}

func TestCompletionDoesNotModifyInput(t *testing.T) {
	original := sampleCandidates()
	snapshot := append([]CompletionCandidate(nil), original...)

	s := NewCompletionState(original)
	s.Query("co")
	s.Next()

	if !reflect.DeepEqual(original, snapshot) {
		t.Fatalf("input slice was modified: got %v, want %v", original, snapshot)
	}
	// 内部主表与入参不共享底层数组。
	s.candidates[0].Command = "mutated"
	if original[0].Command == "mutated" {
		t.Fatal("NewCompletionState aliases the input slice")
	}
}

func TestCompletionPrefixFilter(t *testing.T) {
	s := NewCompletionState(sampleCandidates())
	s.Query("co")
	// 可见项保持 Command 字典序：commit < compile < config。
	want := []string{"commit", "compile", "config"}
	if got := commandsOf(s); !reflect.DeepEqual(got, want) {
		t.Fatalf("Query(%q) visible = %v, want %v", "co", got, want)
	}
	if s.Count() != len(want) {
		t.Fatalf("Count() = %d, want %d", s.Count(), len(want))
	}
}

func TestCompletionCaseInsensitive(t *testing.T) {
	s := NewCompletionState(sampleCandidates())
	s.Query("CO")
	want := []string{"commit", "compile", "config"}
	if got := commandsOf(s); !reflect.DeepEqual(got, want) {
		t.Fatalf("uppercase query visible = %v, want %v", got, want)
	}

	// 候选为大写、查询为小写同样命中。
	upper := NewCompletionState([]CompletionCandidate{{Command: "HELP", Description: "x"}})
	upper.Query("hel")
	if upper.Count() != 1 {
		t.Fatalf("lowercase query against uppercase command: Count = %d, want 1", upper.Count())
	}
}

func TestCompletionCJK(t *testing.T) {
	s := NewCompletionState(sampleCandidates())
	s.Query("帮")
	if got := commandsOf(s); !reflect.DeepEqual(got, []string{"帮助"}) {
		t.Fatalf("CJK prefix visible = %v, want [帮助]", got)
	}

	// 多字节汉字按 rune 过滤，不会在 UTF-8 字节中间被截断。
	s.Query("帮助")
	if s.Count() != 1 {
		t.Fatalf("full CJK query Count = %d, want 1", s.Count())
	}
	s.Query("帮x")
	if s.Count() != 0 {
		t.Fatalf("mismatched CJK query Count = %d, want 0", s.Count())
	}
}

func TestCompletionEmptyQuery(t *testing.T) {
	s := NewCompletionState(sampleCandidates())
	s.Query("xyznomatch")
	if s.Count() != 0 {
		t.Fatalf("unmatched query Count = %d, want 0", s.Count())
	}
	s.Query("")
	if got := commandsOf(s); !reflect.DeepEqual(got, []string{"commit", "compile", "config", "test", "帮助"}) {
		t.Fatalf("empty query visible = %v, want all sorted", got)
	}
}

func TestCompletionCurrent(t *testing.T) {
	s := NewCompletionState([]CompletionCandidate{
		{Command: "beta", Description: "B"},
		{Command: "alpha", Description: "A"},
	})

	c, ok := s.Current()
	if !ok || c.Command != "alpha" || c.Description != "A" {
		t.Fatalf("initial Current = (%+v, %v), want alpha/A true", c, ok)
	}

	s.Next()
	c, ok = s.Current()
	if !ok || c.Command != "beta" {
		t.Fatalf("after Next Current = (%+v, %v), want beta", c, ok)
	}

	// 空结果时返回零值与 false。
	s.Query("zzz")
	c, ok = s.Current()
	if ok || c != (CompletionCandidate{}) {
		t.Fatalf("empty Current = (%+v, %v), want zero/false", c, ok)
	}
}

func TestCompletionNextPrevWrap(t *testing.T) {
	s := NewCompletionState([]CompletionCandidate{
		{Command: "c", Description: ""},
		{Command: "a", Description: ""},
		{Command: "b", Description: ""},
	})
	// 排序后为 a, b, c；初始 index 指向 a。
	current := func() string {
		c, ok := s.Current()
		if !ok {
			return "<none>"
		}
		return c.Command
	}

	if current() != "a" {
		t.Fatalf("initial = %q, want a", current())
	}
	s.Next() // b
	s.Next() // c
	s.Next() // 回绕到 a
	if current() != "a" {
		t.Fatalf("after 3 Next = %q, want a (wrap)", current())
	}
	s.Prev() // 回绕到 c
	if current() != "c" {
		t.Fatalf("after Prev from a = %q, want c (wrap)", current())
	}
	s.Prev() // b
	if current() != "b" {
		t.Fatalf("after 2nd Prev = %q, want b", current())
	}

	// 单候选时 Next/Prev 停在原地。
	one := NewCompletionState([]CompletionCandidate{{Command: "only"}})
	one.Next()
	one.Prev()
	one.Next()
	if c, ok := one.Current(); !ok || c.Command != "only" {
		t.Fatalf("single candidate after navigation = (%+v, %v)", c, ok)
	}
}

func TestCompletionReset(t *testing.T) {
	s := NewCompletionState(sampleCandidates())
	s.Query("co")
	s.Next()
	if s.index != 1 {
		t.Fatalf("index before Reset = %d, want 1", s.index)
	}

	s.Reset()
	if s.query != "" || s.index != 0 {
		t.Fatalf("after Reset query=%q index=%d, want empty/0", s.query, s.index)
	}
	if got := commandsOf(s); !reflect.DeepEqual(got, []string{"commit", "compile", "config", "test", "帮助"}) {
		t.Fatalf("after Reset visible = %v, want all", got)
	}
}

func TestCompletionZeroValue(t *testing.T) {
	var s CompletionState
	if s.Count() != 0 {
		t.Fatalf("zero value Count = %d, want 0", s.Count())
	}
	if _, ok := s.Current(); ok {
		t.Fatal("zero value Current should fail")
	}
	s.Next()
	s.Prev()
	s.Query("x")
	s.Reset()
	if s.query != "" || s.index != 0 {
		t.Fatalf("zero value after ops query=%q index=%d", s.query, s.index)
	}

	// nil 指针同样安全，不 panic。
	var p *CompletionState
	if p.Count() != 0 {
		t.Fatalf("nil pointer Count = %d, want 0", p.Count())
	}
	if _, ok := p.Current(); ok {
		t.Fatal("nil pointer Current should fail")
	}
	p.Next()
	p.Prev()
	p.Query("x")
	p.Reset()
}
