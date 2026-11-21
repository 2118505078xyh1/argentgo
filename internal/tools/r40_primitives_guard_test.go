package tools

import (
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// r40RunConcurrently 并行执行 fn 共 n 次并等待全部结束，
// 用于纯函数的并发只读守卫（fn 不得写入共享入参）。
func r40RunConcurrently(n int, fn func(i int)) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			fn(i)
		}(i)
	}
	close(start)
	wg.Wait()
}

// r40CloneStringMap 浅拷贝字符串 map，nil 保持 nil，用于入参不变性守卫。
func r40CloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// TestR40GuardSummarizeDeterministicAndPure 锁定 SummarizeEditResult 的确定性、
// 入参不变性与并发只读安全，并逐字段比对 DiffSummary。
func TestR40GuardSummarizeDeterministicAndPure(t *testing.T) {
	cases := []struct{ before, after map[string]string }{
		{nil, nil},
		{map[string]string{}, map[string]string{}},
		{map[string]string{"a.txt": "a\nb\nc\n"}, map[string]string{"a.txt": "a\nB\nc\n"}},
		{
			map[string]string{"old.txt": "x\ny\n", "中文/文件.go": "第一行\n第二行\n"},
			map[string]string{"new.txt": "p\nq\nr\n", "中文/文件.go": "第一行\n修改行\n"},
		},
	}
	for idx, tc := range cases {
		beforeSnap, afterSnap := r40CloneStringMap(tc.before), r40CloneStringMap(tc.after)
		want := SummarizeEditResult(tc.before, tc.after)
		for rep := 0; rep < 64; rep++ {
			got := SummarizeEditResult(tc.before, tc.after)
			if got.Added != want.Added || got.Removed != want.Removed ||
				got.Files != want.Files || got.Preview != want.Preview {
				t.Fatalf("case %d rep %d: summary = %+v, want %+v", idx, rep, got, want)
			}
		}
		if !reflect.DeepEqual(tc.before, beforeSnap) || !reflect.DeepEqual(tc.after, afterSnap) {
			t.Fatalf("case %d: SummarizeEditResult 修改了入参", idx)
		}
		r40RunConcurrently(16, func(_ int) {
			if got := SummarizeEditResult(tc.before, tc.after); got != want {
				t.Errorf("case %d: 并发结果 = %+v, want %+v", idx, got, want)
			}
		})
	}
}

// TestR40GuardSummarizeCJKAndSpecialInputs 验证 CJK/emoji/组合字符/CRLF/空内容
// 等特殊输入下预览合法 UTF-8、路径按 rune 有界截断且不切坏多字节码点。
func TestR40GuardSummarizeCJKAndSpecialInputs(t *testing.T) {
	longCJK := strings.Repeat("字", diffSummaryPathRunes+10) + ".go"
	before := map[string]string{
		"中文目录/文件名.go":   "第一行\r\n第二行\n",
		"emoji🚀.txt":    "a\nb\n",
		"组合e\u0301.txt": "x\n",
		"空文件":           "",
		longCJK:         "old\n",
	}
	after := map[string]string{
		"中文目录/文件名.go":   "第一行\r\n修改行\n",
		"emoji🚀.txt":    "a\nb\nc\n",
		"组合e\u0301.txt": "x\n",
		"空文件":           "changed\n",
		longCJK:         "new\n",
	}
	beforeSnap, afterSnap := r40CloneStringMap(before), r40CloneStringMap(after)

	want := SummarizeEditResult(before, after)
	for rep := 0; rep < 32; rep++ {
		if got := SummarizeEditResult(before, after); got != want {
			t.Fatalf("rep %d: 特殊输入结果漂移: %+v vs %+v", rep, got, want)
		}
	}
	if !reflect.DeepEqual(before, beforeSnap) || !reflect.DeepEqual(after, afterSnap) {
		t.Fatal("特殊输入下入参被修改")
	}
	if !utf8.ValidString(want.Preview) {
		t.Fatalf("预览不是合法 UTF-8: %q", want.Preview)
	}
	lines := strings.Split(want.Preview, "\n")
	if len(lines) != diffSummaryPreviewLines {
		t.Fatalf("预览行数 = %d, want %d: %q", len(lines), diffSummaryPreviewLines, want.Preview)
	}
	for _, line := range lines {
		// 形态为 "<marker> <path> (+A/-R)"，校验路径 rune 数有界。
		rest, ok := strings.CutPrefix(line, "~ ")
		if !ok {
			if rest, ok = strings.CutPrefix(line, "+ "); !ok {
				rest, ok = strings.CutPrefix(line, "- ")
			}
		}
		if !ok {
			t.Fatalf("预览行形态异常: %q", line)
		}
		path, _, ok := strings.Cut(rest, " (+")
		if !ok {
			t.Fatalf("预览行缺少计数: %q", line)
		}
		if n := utf8.RuneCountInString(path); n > diffSummaryPathRunes {
			t.Fatalf("路径 rune 数 = %d, want <= %d: %q", n, diffSummaryPathRunes, path)
		}
		if !utf8.ValidString(path) {
			t.Fatalf("路径被截断为非法 UTF-8: %q", path)
		}
	}
	// 超长 CJK 路径必须被截断且完整出现在预览中的部分不能切坏汉字。
	if !strings.Contains(want.Preview, "…") {
		t.Fatalf("超长 CJK 路径应被截断: %q", want.Preview)
	}
}

// TestR40GuardDiffHelpersDeterministicAndPure 对四个内部辅助函数做确定性、
// 入参不变性、CJK/特殊输入与并发只读守卫。
func TestR40GuardDiffHelpersDeterministicAndPure(t *testing.T) {
	// splitLines：CRLF 归一化、去尾换行、空内容、CJK 与组合字符。
	for idx, in := range [][]byte{
		nil,
		{},
		[]byte("a\nb\n"),
		[]byte("a\r\nb\r\n"),
		[]byte("第一行\n第二行🚀\n"),
		[]byte("组合e\u0301\n"),
	} {
		snapshot := slices.Clone(in)
		want := diffSummarySplitLines(in)
		for rep := 0; rep < 32; rep++ {
			if got := diffSummarySplitLines(in); !slices.Equal(got, want) {
				t.Fatalf("splitLines case %d rep %d: %v, want %v", idx, rep, got, want)
			}
		}
		if !slices.Equal(in, snapshot) {
			t.Fatalf("splitLines case %d: 入参被修改", idx)
		}
		r40RunConcurrently(8, func(_ int) {
			if got := diffSummarySplitLines(in); !slices.Equal(got, want) {
				t.Errorf("splitLines case %d: 并发结果不一致", idx)
			}
		})
	}

	// lineCounts：相同内容、CJK 行替换、CRLF 差异归一。
	countCases := [][2][]byte{
		{[]byte("a\nb\n"), []byte("a\nb\n")},
		{[]byte("第一行\n第二行\n"), []byte("第一行\n修改行\n")},
		{[]byte("a\r\nb\n"), []byte("a\nb\n")},
		{nil, []byte("新增\n")},
	}
	for idx, pair := range countCases {
		b, a := slices.Clone(pair[0]), slices.Clone(pair[1])
		wAdd, wRem := diffSummaryLineCounts(pair[0], pair[1])
		for rep := 0; rep < 32; rep++ {
			gotAdd, gotRem := diffSummaryLineCounts(pair[0], pair[1])
			if gotAdd != wAdd || gotRem != wRem {
				t.Fatalf("lineCounts case %d rep %d: (%d,%d), want (%d,%d)", idx, rep, gotAdd, gotRem, wAdd, wRem)
			}
		}
		if !slices.Equal(pair[0], b) || !slices.Equal(pair[1], a) {
			t.Fatalf("lineCounts case %d: 入参被修改", idx)
		}
	}

	// truncatePath：CJK/emoji/组合标记边界，结果始终合法 UTF-8 且 rune 有界。
	truncCases := []struct {
		in  string
		max int
	}{
		{"a/b.go", 40},
		{strings.Repeat("字", 20), 5},
		{strings.Repeat("🚀", 20), 4},
		{"abc\u0301def", 4},
		{"a\u0301\u0302bcdef", 4},
		{"", 0},
		{"abc", 0},
	}
	for idx, tc := range truncCases {
		want := diffSummaryTruncatePath(tc.in, tc.max)
		for rep := 0; rep < 32; rep++ {
			if got := diffSummaryTruncatePath(tc.in, tc.max); got != want {
				t.Fatalf("truncatePath case %d rep %d: %q, want %q", idx, rep, got, want)
			}
		}
		if !utf8.ValidString(want) {
			t.Fatalf("truncatePath case %d: 结果非法 UTF-8: %q", idx, want)
		}
		if tc.max > 0 && utf8.RuneCountInString(want) > tc.max {
			t.Fatalf("truncatePath case %d: rune 数越界: %q", idx, want)
		}
		r40RunConcurrently(8, func(_ int) {
			if got := diffSummaryTruncatePath(tc.in, tc.max); got != want {
				t.Errorf("truncatePath case %d: 并发结果不一致", idx)
			}
		})
	}

	// isCombining：组合标记识别确定，普通 CJK/emoji/ASCII 不是组合标记。
	for _, tc := range []struct {
		r    rune
		want bool
	}{
		{'\u0301', true},
		{'\u0302', true},
		{'a', false},
		{'字', false},
		{'🚀', false},
	} {
		want := diffSummaryIsCombining(tc.r)
		for rep := 0; rep < 16; rep++ {
			if got := diffSummaryIsCombining(tc.r); got != want {
				t.Fatalf("isCombining(%U) rep %d = %v, want %v", tc.r, rep, got, want)
			}
		}
		if want != tc.want {
			t.Fatalf("isCombining(%U) = %v, want %v", tc.r, want, tc.want)
		}
	}
}
