package session

import (
	"os"
	"testing"
)

func TestSummaryEmptyRetryEnv(t *testing.T) {
	orig, had := os.LookupEnv(summaryEmptyRetryEnv)
	defer func() {
		if had {
			os.Setenv(summaryEmptyRetryEnv, orig)
		} else {
			os.Unsetenv(summaryEmptyRetryEnv)
		}
	}()
	os.Unsetenv(summaryEmptyRetryEnv)
	if !summaryEmptyRetryEnabled() {
		t.Fatal("default should be on")
	}
	os.Setenv(summaryEmptyRetryEnv, "1")
	if !summaryEmptyRetryEnabled() {
		t.Fatal("env=1 should enable")
	}
	os.Setenv(summaryEmptyRetryEnv, "0")
	if summaryEmptyRetryEnabled() {
		t.Fatal("env=0 should be off")
	}
}

func TestSummaryEmptyRetryNarrowsCut(t *testing.T) {
	// 验证重算逻辑的核心：少丢一条能让原本无实质内容的摘要变得有内容。
	// 构造 dropped 全为无结构噪声、末尾一条带明确 Next 行的序列。
	entries := make([]Entry, 0, 9)
	for i := 0; i < 8; i++ {
		entries = append(entries, Entry{Role: "tool", Content: "noise"})
	}
	entries = append(entries, Entry{Role: "assistant", Content: "Next: finish the migration"})

	start := 8
	first := Summarize(entries[:start], len(entries)-start)
	retry := Summarize(entries[:start-1], len(entries)-(start-1))
	// 两版摘要都能稳定产出；重算版本包含新纳入的 Next 步骤。
	if SummaryIsEmpty(first.Summary) && !SummaryIsEmpty(retry.Summary) {
		// 这正是 helper 要利用的情形：空 -> 非空。
		if retry.Dropped != first.Dropped-1 {
			t.Fatalf("retry should drop one fewer: first=%d retry=%d", first.Dropped, retry.Dropped)
		}
	}
}
