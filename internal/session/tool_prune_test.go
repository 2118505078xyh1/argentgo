package session

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// pruneToolEntry 构造一条工具结果 Entry，供裁剪测试使用。
func pruneToolEntry(id, content string) Entry {
	return Entry{ID: id, Role: "tool", Name: "bash", ToolCallID: id, Content: content}
}

// TestPruneToolResultsOversized 覆盖超长结果裁剪：保留 head/tail，中间换成标记。
func TestPruneToolResultsOversized(t *testing.T) {
	content := strings.Repeat("A", 10) + strings.Repeat("M", 40) + strings.Repeat("Z", 10)
	entries := []Entry{pruneToolEntry("c1", content)}

	pruned, count := PruneToolResults(entries, 20)
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	want := strings.Repeat("A", 10) + "… [middle pruned, 40 runes] …" + strings.Repeat("Z", 10)
	if pruned[0].Content != want {
		t.Fatalf("pruned content = %q, want %q", pruned[0].Content, want)
	}
	// 头部与尾部上下文仍在。
	if !strings.HasPrefix(pruned[0].Content, strings.Repeat("A", 10)) ||
		!strings.HasSuffix(pruned[0].Content, strings.Repeat("Z", 10)) {
		t.Fatal("head/tail context lost after pruning")
	}
}

// TestPruneToolResultsSmallUnchanged 覆盖小结果不动：字节完全一致，计数为 0。
func TestPruneToolResultsSmallUnchanged(t *testing.T) {
	entries := []Entry{
		pruneToolEntry("c1", "ok"),
		pruneToolEntry("c2", strings.Repeat("x", 9)), // 恰好等于 maxRunes
	}
	pruned, count := PruneToolResults(entries, 10)
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
	if !reflect.DeepEqual(pruned, entries) {
		t.Fatalf("small results changed: got %+v, want %+v", pruned, entries)
	}
}

// TestPruneToolResultsRuneSafeCJKEmoji 覆盖 CJK/emoji：切点按 rune，绝不切坏 UTF-8。
func TestPruneToolResultsRuneSafeCJKEmoji(t *testing.T) {
	content := "界世界🙂🚀" + strings.Repeat("中", 20) + "文🙂🚀界"
	entries := []Entry{pruneToolEntry("c1", content)}

	const max = 9
	pruned, count := PruneToolResults(entries, max)
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	got := pruned[0].Content
	if !utf8.ValidString(got) {
		t.Fatalf("pruned content is not valid UTF-8: %q", got)
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("pruned content contains replacement rune: %q", got)
	}

	runes := []rune(content)
	total := len(runes)
	head, tail := max/2, max-max/2
	want := string(runes[:head]) + pruneMarkerText(total-head-tail) + string(runes[total-tail:])
	if got != want {
		t.Fatalf("pruned content = %q, want %q", got, want)
	}
	// 头部恰为前 4 个 rune，尾部恰为后 5 个 rune，CJK/emoji 均未损坏。
	if !strings.HasPrefix(got, string(runes[:head])) ||
		!strings.HasSuffix(got, string(runes[total-tail:])) {
		t.Fatal("head/tail rune slices are not preserved")
	}
}

// TestPruneToolResultsCountAccurate 覆盖 N 计数：每条裁剪结果的标记数字等于被裁 rune 数。
func TestPruneToolResultsCountAccurate(t *testing.T) {
	lengths := []int{21, 100, 7, 55}
	entries := make([]Entry, 0, len(lengths))
	for i, n := range lengths {
		entries = append(entries, pruneToolEntry(
			"c"+string(rune('a'+i)),
			strings.Repeat(string(rune('界')), n),
		))
	}
	const max = 20
	pruned, count := PruneToolResults(entries, max)
	wantCount := 0
	for _, n := range lengths {
		if n > max {
			wantCount++
		}
	}
	if count != wantCount {
		t.Fatalf("count = %d, want %d", count, wantCount)
	}

	head, tail := max/2, max-max/2
	for i, n := range lengths {
		if n <= max {
			if pruned[i].Content != entries[i].Content {
				t.Fatalf("entry #%d should be untouched", i)
			}
			continue
		}
		removed := n - head - tail
		wantMarker := "… [middle pruned, " + strconv.Itoa(removed) + " runes] …"
		if !strings.Contains(pruned[i].Content, wantMarker) {
			t.Fatalf("entry #%d marker = %q, want %q", i, pruned[i].Content, wantMarker)
		}
	}
}

// TestPruneToolResultsDoesNotModifyInput 覆盖不改入参：原切片与原 Entry 均保持不变。
func TestPruneToolResultsDoesNotModifyInput(t *testing.T) {
	entries := []Entry{
		pruneToolEntry("c1", strings.Repeat("a", 50)),
		pruneToolEntry("c2", "short"),
	}
	original := append([]Entry(nil), entries...)
	originalContents := []string{entries[0].Content, entries[1].Content}

	pruned, count := PruneToolResults(entries, 10)
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if !reflect.DeepEqual(entries, original) {
		t.Fatalf("input mutated: got %+v, want %+v", entries, original)
	}
	if entries[0].Content != originalContents[0] || entries[1].Content != originalContents[1] {
		t.Fatal("input Content mutated")
	}
	// 返回的切片是新分配：改动它不应影响入参。
	pruned[0].ID = "changed"
	pruned[0].Content = "changed"
	if entries[0].ID != "c1" || entries[0].Content != originalContents[0] {
		t.Fatal("returned slice shares storage with input")
	}
}

// TestPruneToolResultsIdempotent 覆盖幂等：对已裁剪结果再裁一次不再变化。
func TestPruneToolResultsIdempotent(t *testing.T) {
	entries := []Entry{pruneToolEntry("c1", strings.Repeat("M", 80))}
	p1, c1 := PruneToolResults(entries, 20)
	if c1 != 1 {
		t.Fatalf("first count = %d, want 1", c1)
	}
	p2, c2 := PruneToolResults(p1, 20)
	if c2 != 0 {
		t.Fatalf("second count = %d, want 0", c2)
	}
	if !reflect.DeepEqual(p1, p2) {
		t.Fatalf("not idempotent:\nfirst  = %+v\nsecond = %+v", p1, p2)
	}
}

// TestPruneToolResultsOnlyToolRole 覆盖角色过滤：非工具结果即使超长也不裁剪。
func TestPruneToolResultsOnlyToolRole(t *testing.T) {
	long := strings.Repeat("x", 200)
	entries := []Entry{
		{Role: "user", Content: long},
		{Role: "assistant", Content: long},
		{Role: "system", Content: long},
	}
	pruned, count := PruneToolResults(entries, 20)
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
	if !reflect.DeepEqual(pruned, entries) {
		t.Fatal("non-tool entries were changed")
	}
}
