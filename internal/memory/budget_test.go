package memory

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// buildBudgetChain 沿以 base 为根的链在每个目录写入一个 AGENTS.md。
// sizes 按最近的优先：sizes[0] 位于最深的目录，
// sizes[len-1] 位于 base。它返回最深的目录（即 "cwd"）。
func buildBudgetChain(t *testing.T, base string, sizes []int) string {
	t.Helper()
	current := base
	for i, n := range sizes {
		if err := os.MkdirAll(current, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(current, "AGENTS.md"), []byte(strings.Repeat("x", n)), 0o644); err != nil {
			t.Fatal(err)
		}
		if i < len(sizes)-1 {
			current = filepath.Join(current, "d"+strconv.Itoa(i))
		}
	}
	return current
}

func sumTextBytes(texts ...string) int {
	total := 0
	for _, text := range texts {
		total += len(text)
	}
	return total
}

// budgetMarkerCases 将遍历剩余预算放在一个狭窄窗口内
// [DefaultLimit, DefaultLimit+len(TruncationMarker))，位于一个正文恰好为
// DefaultLimit 字节且被单文件上限裁剪的文件之前。这样的
// 文档会被追加一个 TruncationMarker；该标记必须从预算中预留，
// 否则合并文本会悄悄超过 totalBudget。最后一种情况是
// 标记仍放得下的对照。
//
// 由 budgetMarkerChain 构建的链（最近的优先）是：三个超限文档
// （每个 DefaultLimit+标记字节），一个小的未裁剪 "filler" 文档，然后
// 一个最终的超限文档。filler 的大小经过选择，使恰好在最终文档
// 计费之前，剩余预算等于 tc.remaining。
func budgetMarkerCases() []struct {
	name      string
	remaining int
} {
	marker := len(TruncationMarker)
	return []struct {
		name      string
		remaining int
	}{
		{"remaining equals per-file cap", DefaultLimit},
		{"remaining inside marker window", DefaultLimit + 10},
		{"remaining one byte under marker window", DefaultLimit + marker - 1},
		{"remaining at marker window edge", DefaultLimit + marker}, // control: fits exactly
	}
}

// budgetMarkerChain 构建一个五目录链，其从近到_file 大小驱动某个受限文档
// 进入上述标记窗口。
func budgetMarkerChain(t *testing.T, base string, remaining int) string {
	t.Helper()
	over := DefaultLimit + 1000 // any file above the cap yields a DefaultLimit-byte body
	// 三个超限文档花费 3*(DefaultLimit+标记)；然后 filler 使
	// 已用总量达到 totalBudget-remaining。
	filler := totalBudget - 3*(DefaultLimit+len(TruncationMarker)) - remaining
	if filler <= 0 || filler > DefaultLimit {
		t.Fatalf("filler = %d out of range for remaining = %d", filler, remaining)
	}
	return buildBudgetChain(t, base, []int{over, over, over, filler, over})
}

func docTextLengths(docs []Doc) []string {
	texts := make([]string, len(docs))
	for i, doc := range docs {
		texts[i] = doc.Text
	}
	return texts
}

func TestLoadInTotalBudgetAccountsForTruncationMarker(t *testing.T) {
	for _, tc := range budgetMarkerCases() {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			cwd := budgetMarkerChain(t, base, tc.remaining)

			docs, err := LoadIn(cwd, base, DefaultLimit)
			if err != nil {
				t.Fatalf("LoadIn: %v", err)
			}
			if len(docs) != 5 {
				t.Fatalf("docs = %d, want 5: %#v", len(docs), docs)
			}
			if !docs[4].Truncated {
				t.Error("outermost (capped) document must be marked truncated")
			}
			if total := sumTextBytes(docTextLengths(docs)...); total > totalBudget {
				t.Errorf("combined text = %d bytes, budget = %d (remaining was %d)", total, totalBudget, tc.remaining)
			}
		})
	}
}

func TestLoadForDirsTotalBudgetAccountsForTruncationMarker(t *testing.T) {
	for _, tc := range budgetMarkerCases() {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			cwd := budgetMarkerChain(t, base, tc.remaining)
			rel, err := filepath.Rel(base, cwd)
			if err != nil {
				t.Fatal(err)
			}

			docs := LoadForDirs(base, []string{rel})
			if len(docs) != 5 {
				t.Fatalf("docs = %d, want 5: %#v", len(docs), docs)
			}
			if !docs[4].Truncated {
				t.Error("outermost (capped) document must be marked truncated")
			}
			if total := sumTextBytes(docTextLengths(docs)...); total > totalBudget {
				t.Errorf("combined text = %d bytes, budget = %d (remaining was %d)", total, totalBudget, tc.remaining)
			}
		})
	}
}

func TestLoadWithBoundaryTotalBudgetAccountsForTruncationMarker(t *testing.T) {
	for _, tc := range budgetMarkerCases() {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			cwd := budgetMarkerChain(t, base, tc.remaining)
			if err := os.MkdirAll(filepath.Join(base, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}

			insts, err := LoadWithBoundary(cwd, true, LoadOptions{Home: t.TempDir()})
			if err != nil {
				t.Fatalf("LoadWithBoundary: %v", err)
			}
			if len(insts) != 5 {
				t.Fatalf("insts = %d, want 5: %#v", len(insts), insts)
			}
			if !insts[0].Truncated {
				t.Error("outermost (capped) instruction must be marked truncated")
			}
			texts := make([]string, len(insts))
			for i, inst := range insts {
				texts[i] = inst.Text
			}
			if total := sumTextBytes(texts...); total > totalBudget {
				t.Errorf("combined text = %d bytes, budget = %d (remaining was %d)", total, totalBudget, tc.remaining)
			}
		})
	}
}
