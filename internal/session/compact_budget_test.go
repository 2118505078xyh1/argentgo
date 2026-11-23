package session

import (
	"strings"
	"testing"
)

func TestAdjustCutNeverSplitsToolPair(t *testing.T) {
	entries := []Entry{
		{Role: "system", Content: "s"},
		{Role: "user", Content: "u"},
		{Role: "assistant", Content: "a1"},
		{Role: "tool", ToolCallID: "c1", Content: "t1"},
		{Role: "tool", ToolCallID: "c2", Content: "t2"},
		{Role: "assistant", Content: "a2"},
		{Role: "tool", ToolCallID: "c3", Content: "t3"},
		{Role: "assistant", Content: "a3"},
	}
	cases := []struct {
		start int
		want  int
	}{
		{3, 2}, // 第一个工具结果：回退跳过整个工具运行段
		{4, 2}, // 同一运行段的第二个结果
		{6, 5}, // 单个工具结果：回退到它的 assistant
		{5, 5}, // assistant：不变
		{7, 7}, // 末尾的 assistant：不变
	}
	for _, tc := range cases {
		if got := adjustCut(entries, tc.start); got != tc.want {
			t.Errorf("adjustCut(%d) = %d, want %d", tc.start, got, tc.want)
		}
	}
}

func TestKeepForBudgetAndCutForBudget(t *testing.T) {
	entries := []Entry{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: strings.Repeat("a", 200)},
		{Role: "user", Content: strings.Repeat("b", 200)},
		{Role: "user", Content: strings.Repeat("c", 200)},
		{Role: "user", Content: strings.Repeat("d", 200)},
		{Role: "user", Content: strings.Repeat("e", 200)},
	}
	// 每条填充条目约耗 4 + 50 个 token；120 容得下最后两条。
	if keep := KeepForBudget(entries, 120); keep != 2 {
		t.Fatalf("KeepForBudget = %d, want 2", keep)
	}
	if start := CutForBudget(entries, 120); start != 4 {
		t.Fatalf("CutForBudget = %d, want 4", start)
	}
	if got := CutForBudget(entries, 100000); got != 0 {
		t.Fatalf("CutForBudget with a huge budget = %d, want 0 (no-op)", got)
	}
	if got := CutForBudget(entries, 0); got != 0 {
		t.Fatalf("CutForBudget with zero budget = %d, want 0", got)
	}
}

func TestCompactBudgetKeepsTokenDerivedTail(t *testing.T) {
	// 本测试锁定压缩预算行为；关闭默认开启的 schema 补全与多保真视图以隔离变量。
	t.Setenv(summarySchemaEnv, "0")
	t.Setenv(foveatedArchiveEnv, "0")
	dir := t.TempDir()
	sess := newTestSession(t, dir, "compact-budget")
	for _, text := range []string{
		strings.Repeat("a", 200), strings.Repeat("b", 200), strings.Repeat("c", 200),
		strings.Repeat("d", 200), strings.Repeat("e", 200),
	} {
		if err := sess.Append(KindUser, text); err != nil {
			t.Fatal(err)
		}
	}
	if err := sess.CompactBudget(250); err != nil {
		t.Fatalf("CompactBudget: %v", err)
	}
	messages := sess.Messages()
	if len(messages) != 3 {
		t.Fatalf("messages after budget compaction = %d, want 3 (summary + 2)", len(messages))
	}
	if messages[0].Role != "system" {
		t.Fatalf("summary role = %q", messages[0].Role)
	}
	if !strings.Contains(messages[0].Content, "dropped 3 message(s)") {
		t.Fatalf("summary missing drop count:\n%s", messages[0].Content)
	}
	// 已经符合预算的压缩历史是空操作。
	before, _ := sess.Records()
	if err := sess.CompactBudget(100000); err != nil {
		t.Fatal(err)
	}
	after, _ := sess.Records()
	if len(after) != len(before) {
		t.Fatalf("second budget compaction appended %d record(s)", len(after)-len(before))
	}
}

func TestCompactBudgetRejectsNonPositiveBudget(t *testing.T) {
	dir := t.TempDir()
	sess := newTestSession(t, dir, "bad-budget")
	if err := sess.CompactBudget(0); err == nil {
		t.Fatal("CompactBudget(0) succeeded")
	}
}
