package session

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// foveatedTestEntries 构造 n 条内容可预测的短消息，便于逐字比对两端。
func foveatedTestEntries(n int) []Entry {
	out := make([]Entry, n)
	for i := range out {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		out[i] = Entry{ID: fmt.Sprintf("m%04d", i), Role: role, Content: fmt.Sprintf("消息 %04d 正文", i)}
	}
	return out
}

func TestFoveatedArchivePreservesEdges(t *testing.T) {
	entries := foveatedTestEntries(1000)
	budget := 1000 // 远小于总 rune，强制触发稀疏化
	got := foveatedArchive(entries, budget)

	if len(got) >= len(entries) {
		t.Fatalf("archive did not shrink: got %d entries from %d", len(got), len(entries))
	}

	// 头部和尾部必须逐字保留（含 ID、Role、Content）。按配对单元计算期望区间。
	plan := buildProjectionPlan(entries)
	headCount := foveatedTakeUnits(entries, plan, 0, len(plan.order), budget*2/5, false)
	tailCount := foveatedTakeUnits(entries, plan, 0, len(plan.order)-headCount, budget*2/5, true)
	var head, tail []Entry
	for u := 0; u < headCount; u++ {
		for _, i := range plan.units[plan.order[u]] {
			head = append(head, entries[i])
		}
	}
	for u := len(plan.order) - tailCount; u < len(plan.order); u++ {
		for _, i := range plan.units[plan.order[u]] {
			tail = append(tail, entries[i])
		}
	}
	if len(head) == 0 || len(tail) == 0 {
		t.Fatal("test setup: head/tail must be non-empty")
	}
	if len(got) < len(head)+len(tail) {
		t.Fatalf("archive shorter than head+tail: %d < %d", len(got), len(head)+len(tail))
	}
	for i := range head {
		if !reflect.DeepEqual(got[i], head[i]) {
			t.Fatalf("head entry %d changed:\n got %+v\nwant %+v", i, got[i], head[i])
		}
	}
	off := len(got) - len(tail)
	for i := range tail {
		if !reflect.DeepEqual(got[off+i], tail[i]) {
			t.Fatalf("tail entry %d changed:\n got %+v\nwant %+v", i, got[off+i], tail[i])
		}
	}
}

func TestFoveatedArchiveSparseMiddleAndGap(t *testing.T) {
	entries := foveatedTestEntries(1000)
	got := foveatedArchive(entries, 1000)

	// gap 标记应存在且可解析，省略数等于中段未保留的 dense 条数。
	gapIdx := -1
	omitted := 0
	for i, e := range got {
		if n, ok := foveatedParseGap(e.Content); ok {
			gapIdx, omitted = i, n
			break
		}
	}
	if gapIdx < 0 {
		t.Fatalf("no gap marker found in %d archived entries", len(got))
	}
	if omitted <= 0 {
		t.Fatalf("gap marker omitted count = %d, want > 0", omitted)
	}

	// 中段稀疏条目应带 "角色: " 前缀且正文被压缩到上限内。
	sparseCount := 0
	for _, e := range got {
		if e.Role == "system" {
			continue
		}
		if idx := strings.Index(e.Content, ": "); idx >= 0 {
			body := e.Content[idx+2:]
			if utf8.RuneCountInString(body) <= foveatedSparseRuneLimit {
				sparseCount++
			}
		}
	}
	if sparseCount == 0 {
		t.Fatal("no sparse middle entries produced")
	}
	if omitted+sparseCount >= 1000 {
		t.Fatalf("sparse view did not drop dense middle entries: omitted=%d sparse=%d", omitted, sparseCount)
	}
}

func TestFoveatedArchiveGapParsing(t *testing.T) {
	if n, ok := foveatedParseGap("[… 42 messages omitted …]"); !ok || n != 42 {
		t.Fatalf("parse gap = (%d,%v), want (42,true)", n, ok)
	}
	for _, bad := range []string{"", "regular message", "[… x messages omitted …]"} {
		if _, ok := foveatedParseGap(bad); ok {
			t.Errorf("non-gap text %q parsed as gap", bad)
		}
	}
}

func TestFoveatedArchiveWithinBudgetIsNoop(t *testing.T) {
	entries := foveatedTestEntries(5)
	got := foveatedArchive(entries, 100000)
	if !reflect.DeepEqual(got, entries) {
		t.Fatal("within-budget archive changed entries")
	}
	// 同一切片（零拷贝）：通过修改返回值反映到入参来验证。
	if &got[0] != &entries[0] {
		t.Fatal("within-budget archive should return the same backing slice")
	}
}

func TestFoveatedArchiveKeepsToolCallIDs(t *testing.T) {
	entries := []Entry{
		{ID: "a", Role: "assistant", Content: strings.Repeat("调用工具 ", 200), ToolCallID: "call-1"},
		{ID: "b", Role: "tool", Content: strings.Repeat("工具结果 ", 200), ToolCallID: "call-1"},
	}
	// 用极小预算强制两端都进入稀疏段。
	got := foveatedArchive(entries, 40)
	for _, e := range got {
		if e.Role == "assistant" || e.Role == "tool" {
			if e.ToolCallID != "call-1" {
				t.Fatalf("tool call id lost in sparse entry %+v", e)
			}
		}
	}
}

func TestFoveatedGapRespectsToolPairBoundary(t *testing.T) {
	// 构造 400 对 assistant(tool_call) + tool(result)，每对独立 id。
	entries := make([]Entry, 0, 800)
	for i := 0; i < 400; i++ {
		id := fmt.Sprintf("call-%03d", i)
		entries = append(entries,
			Entry{ID: fmt.Sprintf("a%03d", i), Role: "assistant", Content: fmt.Sprintf("调用工具 %d", i), ToolCallID: id},
			Entry{ID: fmt.Sprintf("t%03d", i), Role: "tool", Content: fmt.Sprintf("工具结果 %d", i), ToolCallID: id},
		)
	}
	got := foveatedArchive(entries, 700)
	for i, e := range got {
		if !isFoveatedGap(e) {
			continue
		}
		if i == 0 || i+1 >= len(got) {
			continue
		}
		before, after := got[i-1].ToolCallID, got[i+1].ToolCallID
		if before != "" && before == after {
			t.Fatalf("gap at %d split tool pair %q", i, before)
		}
	}
}

func TestFoveatedArchiveViaMessages(t *testing.T) {
	// 默认开启：空值也应走归档视图。
	t.Setenv(foveatedArchiveEnv, "")
	t.Setenv(budgetProjectionBudgetEnv, "600")
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("fovea-wire")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		if err := sess.Append(KindUser, fmt.Sprintf("消息 %04d 正文", i)); err != nil {
			t.Fatal(err)
		}
	}
	got := sess.Messages()

	gapCount := 0
	omitted := 0
	for _, e := range got {
		if !isFoveatedGap(e) {
			continue
		}
		gapCount++
		if n, ok := foveatedParseGap(e.Content); ok {
			omitted += n
		}
	}
	if gapCount != 1 {
		t.Fatalf("gap count = %d, want 1 (entry count %d)", gapCount, len(got))
	}
	if omitted <= 0 {
		t.Fatalf("gap omitted = %d, want > 0", omitted)
	}
	if len(got) >= 1000 {
		t.Fatalf("Messages() not archived: %d entries", len(got))
	}
	// 两端逐字保留。
	if got[0].Content != "消息 0000 正文" {
		t.Fatalf("first entry = %q", got[0].Content)
	}
	if got[len(got)-1].Content != "消息 0999 正文" {
		t.Fatalf("last entry = %q", got[len(got)-1].Content)
	}
}

func TestFoveatedArchiveDisabledKeepsMessagesByteIdentical(t *testing.T) {
	// 仅 =0 关闭时，Messages() 必须与未接线时逐字节一致。
	t.Setenv(foveatedArchiveEnv, "0")
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("fovea-off")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(KindUser, "用户消息"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(KindAssistant, "助手消息"); err != nil {
		t.Fatal(err)
	}
	got := sess.Messages()
	// 期望 = 未接线的基线路径：messageEntries(path) 后直接 SettleOrphanToolCalls。
	// 这精确断言 =0 关闭时 Messages() 与接线前逐字节一致。
	records, err := sess.Records()
	if err != nil {
		t.Fatal(err)
	}
	want := SettleOrphanToolCalls(messageEntries(sess.activePath(records)), SettleInterrupted)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Messages() changed with =0 off:\n got %+v\nwant %+v", got, want)
	}
	for _, e := range got {
		if _, ok := foveatedParseGap(e.Content); ok {
			t.Fatal("gap marker appeared with =0 off")
		}
	}
}
