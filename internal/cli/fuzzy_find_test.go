package cli

import (
	"reflect"
	"testing"
)

func TestFuzzyFindSessionsEmptyQueryReturnsAllInOrder(t *testing.T) {
	titles := []string{"third", "first", "second"}
	got := fuzzyFindSessions("", titles)
	want := []int{0, 1, 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fuzzyFindSessions(\"\") = %v, want %v", got, want)
	}
}

func TestFuzzyFindSessionsWhitespaceOnlyQueryReturnsAllInOrder(t *testing.T) {
	titles := []string{"one", "two"}
	got := fuzzyFindSessions("   \t ", titles)
	want := []int{0, 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("whitespace-only query = %v, want %v", got, want)
	}
}

func TestFuzzyFindSessionsNoMatchReturnsEmpty(t *testing.T) {
	titles := []string{"alpha", "beta"}
	got := fuzzyFindSessions("zzz", titles)
	if len(got) != 0 {
		t.Fatalf("fuzzyFindSessions(\"zzz\") = %v, want empty", got)
	}
}

func TestFuzzyFindSessionsKeepsOriginalIndices(t *testing.T) {
	// 未匹配的条目位于匹配条目之前，返回的必须是原始下标而非重编号。
	titles := []string{"unrelated", "target", "also unrelated"}
	got := fuzzyFindSessions("target", titles)
	want := []int{1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fuzzyFindSessions = %v, want %v", got, want)
	}
}

func TestFuzzyFindSessionsSubsequenceMatch(t *testing.T) {
	// 非连续的字符子序列也应匹配。
	titles := []string{"axbxc"}
	got := fuzzyFindSessions("abc", titles)
	if !reflect.DeepEqual(got, []int{0}) {
		t.Fatalf("subsequence match = %v, want [0]", got)
	}
}

func TestFuzzyFindSessionsCaseInsensitive(t *testing.T) {
	titles := []string{"aBc", "ABC", "xyz"}
	got := fuzzyFindSessions("abc", titles)
	if !reflect.DeepEqual(got, []int{0, 1}) {
		t.Fatalf("lowercase query = %v, want [0 1]", got)
	}
	// query 含大写时同样不区分大小写。
	got = fuzzyFindSessions("AbC", []string{"aBc", "ABC", "xyz"})
	if !reflect.DeepEqual(got, []int{0, 1}) {
		t.Fatalf("mixed-case query = %v, want [0 1]", got)
	}
}

func TestFuzzyFindSessionsRanksExactAndBoundaryAboveScattered(t *testing.T) {
	titles := []string{
		"aXXbXXc", // 分散匹配，间隔大
		"abc",     // 完全且连续、位于开头
		"foo abc", // 连续，但不在开头
	}
	got := fuzzyFindSessions("abc", titles)
	// 开头连续匹配得分最高；"foo abc" 的词边界高于分散匹配。
	want := []int{1, 2, 0}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("relevance order = %v, want %v", got, want)
	}
}

func TestFuzzyFindSessionsShorterTextWinsTie(t *testing.T) {
	titles := []string{"xxabxx", "ab"}
	got := fuzzyFindSessions("ab", titles)
	// 两者都是连续匹配，但 "ab" 更短。
	if !reflect.DeepEqual(got, []int{1, 0}) {
		t.Fatalf("shorter-text ordering = %v, want [1 0]", got)
	}
}

func TestFuzzyFindSessionsStableForEqualTitles(t *testing.T) {
	titles := []string{"same", "same", "same"}
	got := fuzzyFindSessions("same", titles)
	want := []int{0, 1, 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("equal titles should keep original order, got %v, want %v", got, want)
	}
}

func TestFuzzyFindSessionsCJKAndEmojiSafe(t *testing.T) {
	titles := []string{
		"打开配置文件", // 不含查询字符
		"你好世界",   // 含 "好世"
		"世界你好🚀",  // 含 "世界" 且带 emoji
	}
	// 多字节字符按 rune 处理，"好世" 只出现在第二个标题。
	if got := fuzzyFindSessions("好世", titles); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("CJK query 好世 = %v, want [1]", got)
	}
	// "世界" 同时匹配第三和第二个标题；起始处匹配的第三个应排在前。
	got := fuzzyFindSessions("世界", titles)
	if !reflect.DeepEqual(got, []int{2, 1}) {
		t.Fatalf("CJK query 世界 = %v, want [2 1]", got)
	}
	// emoji 作为普通 rune，不打断子序列匹配。
	if got := fuzzyFindSessions("界🚀", titles); !reflect.DeepEqual(got, []int{2}) {
		t.Fatalf("emoji query = %v, want [2]", got)
	}
}

func TestFuzzyFindSessionsDeterministic(t *testing.T) {
	titles := []string{"abc", "aXbc", "zzz", "ABC", "ab"}
	first := fuzzyFindSessions("abc", titles)
	for i := 0; i < 5; i++ {
		if got := fuzzyFindSessions("abc", titles); !reflect.DeepEqual(got, first) {
			t.Fatalf("call %d = %v, want %v", i, got, first)
		}
	}
}
