package app

import (
	"math"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestGitStatusHintNoHint 覆盖所有不应产生提示的情形：非 git 仓库、干净工作区、
// 以及负数形式的脏文件数，均应返回空串，使默认提示的字节保持不变。
func TestGitStatusHintNoHint(t *testing.T) {
	cases := []struct {
		name       string
		isRepo     bool
		dirtyFiles int
		fileCount  int
	}{
		{"非仓库即便有脏改动也返回空串", false, 3, 50},
		{"非仓库且大仓库也返回空串", false, 3, 2000},
		{"干净仓库返回空串", true, 0, 50},
		{"负数脏文件数返回空串", true, -1, 50},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gitStatusHint(tc.isRepo, tc.dirtyFiles, tc.fileCount); got != "" {
				t.Fatalf("gitStatusHint(%v, %d, %d) = %q, want 空串", tc.isRepo, tc.dirtyFiles, tc.fileCount, got)
			}
		})
	}
}

// TestGitStatusHintDirtyRepo 保证脏工作区返回中文提示，包含未提交改动的数量，
// 且普通规模仓库会展示文件总数。
func TestGitStatusHintDirtyRepo(t *testing.T) {
	got := gitStatusHint(true, 3, 50)
	for _, want := range []string{"工作区", "3", "未提交改动", "仓库共", "50", "个文件"} {
		if !strings.Contains(got, want) {
			t.Errorf("gitStatusHint(true, 3, 50) 缺少关键内容 %q：%q", want, got)
		}
	}
}

// TestGitStatusHintLargeRepoConcise 保证超过阈值的大仓库提示更精简：
// 仍保留未提交改动的语义，但省略仓库文件总数与明细。
func TestGitStatusHintLargeRepoConcise(t *testing.T) {
	full := gitStatusHint(true, 3, 10)
	concise := gitStatusHint(true, 3, gitStatusLargeRepoFiles+1)
	if !strings.Contains(concise, "3") || !strings.Contains(concise, "未提交改动") {
		t.Fatalf("大型仓库提示应保留改动数量与语义：%q", concise)
	}
	if !strings.Contains(concise, "大型仓库") {
		t.Errorf("大型仓库提示缺少规模标记：%q", concise)
	}
	if strings.Contains(concise, "仓库共") || strings.Contains(concise, "个文件") {
		t.Errorf("大型仓库提示应省略文件总数明细：%q", concise)
	}
	if utf8.RuneCountInString(concise) >= utf8.RuneCountInString(full) {
		t.Errorf("大型仓库提示应更精简：精简 %q（%d 字符）不短于普通 %q（%d 字符）",
			concise, utf8.RuneCountInString(concise), full, utf8.RuneCountInString(full))
	}
}

// TestGitStatusHintThresholdBoundary 固定 1000 文件的边界语义：
// 恰好 1000 视为普通仓库，1001 才切换为精简措辞。
func TestGitStatusHintThresholdBoundary(t *testing.T) {
	if got := gitStatusHint(true, 2, gitStatusLargeRepoFiles); !strings.Contains(got, "仓库共") {
		t.Errorf("恰好 %d 个文件应视为普通仓库：%q", gitStatusLargeRepoFiles, got)
	}
	if got := gitStatusHint(true, 2, gitStatusLargeRepoFiles+1); !strings.Contains(got, "大型仓库") {
		t.Errorf("超过 %d 个文件应视为大型仓库：%q", gitStatusLargeRepoFiles, got)
	}
}

// TestGitStatusHintUnknownFileCount 保证文件数未知（非正数）时只提示脏改动，
// 不输出无意义的“仓库共 0 个文件”之类文本。
func TestGitStatusHintUnknownFileCount(t *testing.T) {
	for _, fileCount := range []int{0, -5} {
		got := gitStatusHint(true, 7, fileCount)
		if !strings.Contains(got, "7") || !strings.Contains(got, "未提交改动") {
			t.Errorf("fileCount=%d 应提示改动数量与语义：%q", fileCount, got)
		}
		if strings.Contains(got, "仓库共") || strings.Contains(got, "个文件") {
			t.Errorf("fileCount=%d 不应展示文件总数：%q", fileCount, got)
		}
	}
}

// TestGitStatusHintCJKAndBounded 保证所有可能的输出都是合法 UTF-8（中文不会被截断），
// 且长度存在上界——即使传入极大的脏文件数也不会生成无限长的提示。
func TestGitStatusHintCJKAndBounded(t *testing.T) {
	cases := []struct {
		isRepo     bool
		dirtyFiles int
		fileCount  int
	}{
		{true, 1, 1},
		{true, 42, 999},
		{true, 42, 1001},
		{true, math.MaxInt, 10},
		{true, math.MaxInt, 2000},
		{false, math.MaxInt, 2000},
	}
	const maxRunes = 64
	for _, tc := range cases {
		got := gitStatusHint(tc.isRepo, tc.dirtyFiles, tc.fileCount)
		if !utf8.ValidString(got) {
			t.Errorf("gitStatusHint(%v, %d, %d) 不是合法 UTF-8：%q", tc.isRepo, tc.dirtyFiles, tc.fileCount, got)
		}
		if n := utf8.RuneCountInString(got); n > maxRunes {
			t.Errorf("gitStatusHint(%v, %d, %d) 长度 %d 超过上界 %d：%q",
				tc.isRepo, tc.dirtyFiles, tc.fileCount, n, maxRunes, got)
		}
	}
}

// TestGitStatusHintStable 保证多次调用逐字节一致（纯函数，可用于缓存稳定的提示前缀）。
func TestGitStatusHintStable(t *testing.T) {
	cases := []struct {
		isRepo     bool
		dirtyFiles int
		fileCount  int
	}{
		{true, 3, 50},
		{true, 3, 2000},
		{true, 0, 50},
		{false, 3, 50},
	}
	for _, c := range cases {
		first := gitStatusHint(c.isRepo, c.dirtyFiles, c.fileCount)
		for i := 0; i < 3; i++ {
			if got := gitStatusHint(c.isRepo, c.dirtyFiles, c.fileCount); got != first {
				t.Fatalf("gitStatusHint(%v, %d, %d) 不稳定：首次 %q，第 %d 次 %q",
					c.isRepo, c.dirtyFiles, c.fileCount, first, i+1, got)
			}
		}
	}
}
