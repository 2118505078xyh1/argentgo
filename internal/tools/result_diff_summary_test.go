package tools

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSummarizeEditResultLineCounts(t *testing.T) {
	got := SummarizeEditResult(
		map[string]string{"a.txt": "a\nb\nc\n"},
		map[string]string{"a.txt": "a\nB\nc\n"},
	)
	want := DiffSummary{Added: 1, Removed: 1, Files: 1, Preview: "~ a.txt (+1/-1)"}
	if got != want {
		t.Fatalf("SummarizeEditResult() = %+v, want %+v", got, want)
	}
}

func TestSummarizeEditResultAddedDeletedFiles(t *testing.T) {
	// 仅 after 存在的文件算新增，仅 before 存在的文件算删除。
	got := SummarizeEditResult(
		map[string]string{"old.txt": "x\ny\n"},
		map[string]string{"new.txt": "p\nq\nr\n"},
	)
	if got.Added != 3 || got.Removed != 2 || got.Files != 2 {
		t.Fatalf("counts = (+%d/-%d, %d files), want (+3/-2, 2 files)", got.Added, got.Removed, got.Files)
	}
	lines := strings.Split(got.Preview, "\n")
	if len(lines) != 2 {
		t.Fatalf("preview lines = %d, want 2: %q", len(lines), got.Preview)
	}
	// 路径按字典序排列：new.txt 在 old.txt 之前。
	if lines[0] != "+ new.txt (+3/-0)" {
		t.Fatalf("preview[0] = %q, want %q", lines[0], "+ new.txt (+3/-0)")
	}
	if lines[1] != "- old.txt (+0/-2)" {
		t.Fatalf("preview[1] = %q, want %q", lines[1], "- old.txt (+0/-2)")
	}
}

func TestSummarizeEditResultMultipleFiles(t *testing.T) {
	before := map[string]string{
		"a.txt": "a\nb\n", // 修改：追加一行
		"b.txt": "x\n",    // 未变：应被忽略
		"c.txt": "p\nq\n", // 删除
	}
	after := map[string]string{
		"a.txt": "a\nb\nc\n",
		"b.txt": "x\n",
		"d.txt": "z\n", // 新增
	}
	got := SummarizeEditResult(before, after)
	want := DiffSummary{
		Added:   2, // a(+1) + d(+1)
		Removed: 2, // c(-2)
		Files:   3, // a, c, d
		Preview: strings.Join([]string{
			"~ a.txt (+1/-0)",
			"- c.txt (+0/-2)",
			"+ d.txt (+1/-0)",
		}, "\n"),
	}
	if got != want {
		t.Fatalf("SummarizeEditResult() = %+v, want %+v", got, want)
	}
}

func TestSummarizeEditResultPreviewBounded(t *testing.T) {
	// 构造 6 个变更文件，每个路径都超过上限，验证预览最多 4 行且路径被截断。
	before := make(map[string]string, 6)
	after := make(map[string]string, 6)
	for i := 0; i < 6; i++ {
		name := strings.Repeat("x", diffSummaryPathRunes+20) + "-" + string(rune('a'+i))
		before[name] = "a\n"
		after[name] = "b\n"
	}
	got := SummarizeEditResult(before, after)
	if got.Files != 6 {
		t.Fatalf("Files = %d, want 6", got.Files)
	}
	lines := strings.Split(got.Preview, "\n")
	if len(lines) != diffSummaryPreviewLines {
		t.Fatalf("preview lines = %d, want %d: %q", len(lines), diffSummaryPreviewLines, got.Preview)
	}
	for _, line := range lines {
		if !strings.Contains(line, "…") {
			t.Fatalf("preview line not truncated: %q", line)
		}
		// 形态为 "<marker> <path> (+A/-R)"，校验路径部分的 rune 长度有界。
		rest, ok := strings.CutPrefix(line, "~ ")
		if !ok {
			t.Fatalf("unexpected preview line: %q", line)
		}
		path, _, ok := strings.Cut(rest, " (+")
		if !ok {
			t.Fatalf("unexpected preview line: %q", line)
		}
		if n := utf8.RuneCountInString(path); n > diffSummaryPathRunes {
			t.Fatalf("path rune count = %d, want <= %d: %q", n, diffSummaryPathRunes, path)
		}
	}
}

func TestSummarizeEditResultCJK(t *testing.T) {
	before := map[string]string{"中文目录/文件名.go": "第一行\n第二行\n"}
	after := map[string]string{"中文目录/文件名.go": "第一行\n修改行\n"}
	got := SummarizeEditResult(before, after)
	if got.Added != 1 || got.Removed != 1 || got.Files != 1 {
		t.Fatalf("counts = (+%d/-%d, %d files), want (+1/-1, 1 file)", got.Added, got.Removed, got.Files)
	}
	if !utf8.ValidString(got.Preview) {
		t.Fatalf("preview is not valid UTF-8: %q", got.Preview)
	}
	if got.Preview != "~ 中文目录/文件名.go (+1/-1)" {
		t.Fatalf("preview = %q, want intact CJK path", got.Preview)
	}
}

func TestSummarizeEditResultEmpty(t *testing.T) {
	for name, tc := range map[string]struct {
		before, after map[string]string
	}{
		"both nil":    {nil, nil},
		"both empty":  {map[string]string{}, map[string]string{}},
		"empty map":   {map[string]string{}, nil},
		"empty files": {map[string]string{"e.txt": ""}, map[string]string{"e.txt": ""}},
	} {
		t.Run(name, func(t *testing.T) {
			got := SummarizeEditResult(tc.before, tc.after)
			if got != (DiffSummary{}) {
				t.Fatalf("SummarizeEditResult() = %+v, want zero value", got)
			}
		})
	}
}

func TestSummarizeEditResultNoMutation(t *testing.T) {
	before := map[string]string{"a.txt": "a\nb\n", "gone.txt": "z\n"}
	after := map[string]string{"a.txt": "a\nb\nc\n", "new.txt": "q\n"}
	beforeCopy := make(map[string]string, len(before))
	for k, v := range before {
		beforeCopy[k] = v
	}
	afterCopy := make(map[string]string, len(after))
	for k, v := range after {
		afterCopy[k] = v
	}
	_ = SummarizeEditResult(before, after)
	if !reflect.DeepEqual(before, beforeCopy) {
		t.Fatalf("before mutated: got %v, want %v", before, beforeCopy)
	}
	if !reflect.DeepEqual(after, afterCopy) {
		t.Fatalf("after mutated: got %v, want %v", after, afterCopy)
	}
}

func TestSummarizeEditResultDeterministic(t *testing.T) {
	build := func(reverse bool) (map[string]string, map[string]string) {
		keys := []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt"}
		if reverse {
			for i, j := 0, len(keys)-1; i < j; i, j = i+1, j-1 {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
		before := make(map[string]string)
		after := make(map[string]string)
		for i, k := range keys {
			before[k] = "line\n"
			after[k] = "line\nchanged\n" + string(rune('a'+i)) + "\n"
		}
		return before, after
	}
	want := SummarizeEditResult(build(false))
	for i := 0; i < 100; i++ {
		before, after := build(i%2 == 0)
		got := SummarizeEditResult(before, after)
		if got != want {
			t.Fatalf("iteration %d = %+v, want %+v", i, got, want)
		}
	}
}

func TestDiffSummaryTruncatePath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{name: "short unchanged", in: "a/b.go", max: 40, want: "a/b.go"},
		{name: "cjk bounded", in: strings.Repeat("字", 10), max: 4, want: "字字字…"},
		{name: "ascii bounded", in: strings.Repeat("x", 10), max: 5, want: "xxxx…"},
		{name: "zero max", in: "abc", max: 0, want: ""},
		// 组合序列 c + U+0301 应作为一个字素整体保留或丢弃：切点不能落在
		// 组合标记之前，否则会把 "c\u0301" 拆成两半。
		{name: "combining boundary respected", in: "abc\u0301def", max: 4, want: "ab…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := diffSummaryTruncatePath(tc.in, tc.max)
			if got != tc.want {
				t.Fatalf("truncatePath(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("result is not valid UTF-8: %q", got)
			}
			if r := utf8.RuneCountInString(got); r > tc.max && tc.max > 0 {
				t.Fatalf("result rune count = %d, want <= %d: %q", r, tc.max, got)
			}
		})
	}
}
