package tools

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// earlierMarkerRE 解析 [N earlier lines] 标记。
var earlierMarkerRE = regexp.MustCompile(`\[(\d+) earlier lines\]`)

// collapseLineCount 按与 collapseSplitLines 相同的口径统计文本行数：
// 末尾换行是终止符，不额外记一行。
func collapseLineCount(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n") + 1
	if strings.HasSuffix(s, "\n") {
		n--
	}
	return n
}

// TestCollapseOutputNoCollapseWhenWithinBudget 覆盖"未超限即原样返回"：
// 逐字节相同、EarlyLines 为 0、FullPath 为空，且不改动入参。
func TestCollapseOutputNoCollapseWhenWithinBudget(t *testing.T) {
	cases := []struct {
		name     string
		s        string
		maxLines int
		maxChars int
	}{
		{"short ascii", "line1\nline2\nline3", 100, 1000},
		{"exactly at line budget", "a\nb\nc", 3, 100},
		{"exactly at char budget", "abcde", 100, 5},
		{"empty", "", 10, 10},
		{"single line", "only one line", 1, 1000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.s
			got := CollapseOutput(tc.s, tc.maxLines, tc.maxChars)
			if got.Text != tc.s {
				t.Fatalf("Text = %q, want unchanged %q", got.Text, tc.s)
			}
			if got.EarlyLines != 0 {
				t.Errorf("EarlyLines = %d, want 0", got.EarlyLines)
			}
			if got.FullPath != "" {
				t.Errorf("FullPath = %q, want empty", got.FullPath)
			}
			if tc.s != before {
				t.Fatal("input string was mutated")
			}
		})
	}
}

// TestCollapseOutputKeepsTailErrorLines 验证尾部错误行被保留、早期行被隐藏，
// 且标记与 EarlyLines 一致。
func TestCollapseOutputKeepsTailErrorLines(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&sb, "step %d: ok\n", i)
	}
	sb.WriteString("ERROR: boom\n")
	sb.WriteString("stack trace line 1\n")
	sb.WriteString("stack trace line 2\n")
	s := sb.String()

	got := CollapseOutput(s, 8, 1000)
	m := earlierMarkerRE.FindStringSubmatch(got.Text)
	if m == nil {
		t.Fatalf("missing [N earlier lines] marker in %q", got.Text)
	}
	n, _ := strconv.Atoi(m[1])
	if n != got.EarlyLines {
		t.Fatalf("marker N=%d but EarlyLines=%d", n, got.EarlyLines)
	}
	if !strings.Contains(got.Text, "ERROR: boom") ||
		!strings.Contains(got.Text, "stack trace line 2") {
		t.Fatalf("tail error lines not preserved: %q", got.Text)
	}
	// 头部允许保留少量上下文（step 0），但中段必须被折叠掉。
	if strings.Contains(got.Text, "step 20: ok") {
		t.Fatalf("middle line leaked into output: %q", got.Text)
	}
	if n <= 0 {
		t.Fatalf("expected a positive number of omitted lines, got %d", n)
	}
	if got.FullPath != "" {
		t.Errorf("FullPath = %q, want empty (pure function)", got.FullPath)
	}
}

// TestCollapseOutputDualConstraints 验证双约束：折叠后行数<=maxLines 且
// rune 数<=maxChars（标记行与换行都计入），并覆盖两个维度分别触发的场景。
func TestCollapseOutputDualConstraints(t *testing.T) {
	cases := []struct {
		name     string
		s        string
		maxLines int
		maxChars int
	}{
		{"only lines overflow", strings.Repeat("x\n", 100), 12, 10000},
		{"only chars overflow", strings.Repeat("abcdefghij\n", 8), 1000, 30},
		{"both overflow", strings.Repeat("abcdefghij\n", 40), 9, 60},
		{"tiny char budget", strings.Repeat("中文错误行错误行错误行\n", 12), 1000, 25},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CollapseOutput(tc.s, tc.maxLines, tc.maxChars)
			if !utf8.ValidString(got.Text) {
				t.Fatalf("output is not valid UTF-8: %q", got.Text)
			}
			if lc := collapseLineCount(got.Text); lc > tc.maxLines {
				t.Fatalf("line count %d exceeds maxLines %d: %q", lc, tc.maxLines, got.Text)
			}
			if rc := utf8.RuneCountInString(got.Text); rc > tc.maxChars {
				t.Fatalf("rune count %d exceeds maxChars %d: %q", rc, tc.maxChars, got.Text)
			}
			if earlierMarkerRE.FindStringSubmatch(got.Text) == nil {
				t.Fatalf("expected a collapse marker: %q", got.Text)
			}
			// 标记中的 N 必须等于被省略的行数。
			total := collapseLineCount(tc.s)
			kept := collapseLineCount(got.Text) - 1 // 去掉标记行
			m := earlierMarkerRE.FindStringSubmatch(got.Text)
			n, _ := strconv.Atoi(m[1])
			if n != total-kept {
				t.Fatalf("marker N=%d, but total-kept=%d (total=%d, kept=%d)", n, total-kept, total, kept)
			}
		})
	}
}

// TestCollapseOutputRunesSafeCJKEmoji 验证 CJK/emoji 场景下不切坏 UTF-8，
// 且不产生 U+FFFD 替换字符；保留的尾部内容原样出现。
func TestCollapseOutputRunesSafeCJKEmoji(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&sb, "进度 %d 😀 正常\n", i)
	}
	sb.WriteString("错误 😀💥 发生\n")
	sb.WriteString("结尾 中文😀\n")
	s := sb.String()

	got := CollapseOutput(s, 6, 40)
	if !utf8.ValidString(got.Text) {
		t.Fatalf("output is not valid UTF-8: %q", got.Text)
	}
	if strings.ContainsRune(got.Text, utf8.RuneError) {
		t.Fatalf("output contains U+FFFD (broken rune): %q", got.Text)
	}
	if !strings.Contains(got.Text, "结尾 中文😀") {
		t.Fatalf("CJK/emoji tail line not preserved: %q", got.Text)
	}
	if rc := utf8.RuneCountInString(got.Text); rc > 40 {
		t.Fatalf("rune count %d exceeds 40: %q", rc, got.Text)
	}
}

// TestCollapseOutputEarlierMarkerCount 用多种行数验证标记中的 N 精确等于
// 被省略行数，且 EarlyLines 与标记一致。
func TestCollapseOutputEarlierMarkerCount(t *testing.T) {
	for _, total := range []int{10, 37, 128, 1001} {
		var sb strings.Builder
		for i := 0; i < total; i++ {
			fmt.Fprintf(&sb, "L%04d payload\n", i)
		}
		// 行数预算必须小于总行数，才能真正触发折叠。
		got := CollapseOutput(sb.String(), total/2, 100000)
		kept := collapseLineCount(got.Text) - 1
		want := total - kept
		m := earlierMarkerRE.FindStringSubmatch(got.Text)
		if m == nil {
			t.Fatalf("total=%d: missing marker: %q", total, got.Text)
		}
		n, _ := strconv.Atoi(m[1])
		if n != want || got.EarlyLines != want {
			t.Fatalf("total=%d: marker N=%d, EarlyLines=%d, want %d", total, n, got.EarlyLines, want)
		}
	}
}

// TestCollapseOutputTrailingNewlinePreserved 验证折叠保留原尾随换行形态。
func TestCollapseOutputTrailingNewlinePreserved(t *testing.T) {
	body := strings.Repeat("line\n", 20) // 以换行结尾
	got := CollapseOutput(body, 5, 1000)
	if !strings.HasSuffix(got.Text, "\n") {
		t.Fatalf("trailing newline lost: %q", got.Text)
	}
	bodyNoNL := strings.TrimSuffix(body, "\n")
	got2 := CollapseOutput(bodyNoNL, 5, 1000)
	if strings.HasSuffix(got2.Text, "\n") {
		t.Fatalf("unexpected trailing newline: %q", got2.Text)
	}
}

// TestCollapseOutputDefaultsDoNotChangeLegacyFrame 验证 maxLines/maxChars<=0
// 使用默认值：普通输出原样返回、FullPath 为空；同时确认既有 clipTo 帧未被改动
// （本原语不接线，默认输出帧自然不变）。
func TestCollapseOutputDefaultsDoNotChangeLegacyFrame(t *testing.T) {
	s := "a short, ordinary output\nsecond line\n"
	for _, p := range [][2]int{{0, 0}, {-1, -5}, {0, -1}} {
		got := CollapseOutput(s, p[0], p[1])
		if got.Text != s || got.EarlyLines != 0 || got.FullPath != "" {
			t.Fatalf("CollapseOutput(%v) on small input changed it: %+v", p, got)
		}
	}

	// 不接线保证：clipTo 仍然输出它自己的截断标记，而不是本原语的标记。
	big := strings.Repeat("x", maxToolOutput+100)
	legacy := clipTo(big, maxToolOutput)
	if !strings.Contains(legacy, "... [truncated] ...") {
		t.Fatalf("legacy clipTo frame changed: %q", legacy)
	}
	if strings.Contains(legacy, "earlier lines") {
		t.Fatal("legacy clipTo frame was rewired to the new marker")
	}
}

// TestCollapseOutputConcurrent 验证并发调用结果与串行一致（无共享可变状态）。
func TestCollapseOutputConcurrent(t *testing.T) {
	s := strings.Repeat("context line\n", 50) + "ERROR: final\n"
	want := CollapseOutput(s, 7, 120)

	const workers = 64
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got := CollapseOutput(s, 7, 120)
			if got != want {
				errs <- fmt.Sprintf("concurrent result differs: got %+v want %+v", got, want)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}
