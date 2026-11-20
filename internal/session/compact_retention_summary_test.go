package session

import (
	"encoding/json"
	"fmt"
	"testing"
)

// TestCompactionRestoresOldSummaryText 覆盖压缩+保留的交互：
// 压缩记录被标记为 RetainAlways，因此当后续
// 压缩丢弃它时，retainedForCompaction 会重新发出它。entryKind 把
// 压缩的 system Entry 映射为 KindSummary，但重新发出的记录仍保留
// Compaction 载荷（它有 "summary" 键，而不是 "text"/"content"）。如果
// messageEntry 的 KindSummary 键中没有 "summary"，恢复的消息会丢失
// 全部文本，变成一条空的系统消息。
func TestCompactionRestoresOldSummaryText(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.NewSession("chain")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := s.Append(KindUser, fmt.Sprintf("m%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Compact(1); err != nil {
		t.Fatalf("first Compact: %v", err)
	}
	first := s.Messages()
	var firstSummary string
	for _, e := range first {
		if e.Role == "system" && e.Content != "" {
			firstSummary = e.Content
			break
		}
	}
	if firstSummary == "" {
		t.Fatal("first compaction produced no summary")
	}

	// 让对话增长到第一次压缩之后，再压缩一次。此时
	// 切分会丢弃第一次压缩的 system Entry，它被标记为
	// 保留，因为 KindCompaction 被标记为 RetainAlways。
	for i := 5; i < 9; i++ {
		if err := s.Append(KindUser, fmt.Sprintf("m%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Compact(1); err != nil {
		t.Fatalf("second Compact: %v", err)
	}

	// 重新发出的摘要仍必须携带原始文本，而不是空的
	// 系统消息。现在有两条系统条目：新压缩的
	// 摘要和被恢复的那条；较早的那条必须逐字保留。
	var sawRestored, sawEmpty bool
	for _, e := range s.Messages() {
		if e.Role != "system" {
			continue
		}
		if e.Content == firstSummary {
			sawRestored = true
		}
		if e.Content == "" {
			sawEmpty = true
		}
	}
	if sawEmpty {
		t.Fatalf("a system entry was re-emitted with empty content: %+v", s.Messages())
	}
	if !sawRestored {
		t.Fatalf("first compaction summary text was lost after re-emission: %+v", s.Messages())
	}
}

// TestMessageEntrySummaryKey 记录这样一个事实：携带规范 "summary" 载荷键的
// KindSummary 与携带 "text" 的条目投影到相同的 Entry——
// 这正是 retainedForCompaction 所依赖的不变量。
func TestMessageEntrySummaryKey(t *testing.T) {
	data, _ := json.Marshal(map[string]any{"summary": "kept summary"})
	entry, ok := messageEntry(Record{Kind: KindSummary, Data: data})
	if !ok {
		t.Fatal("KindSummary with summary key did not project to an Entry")
	}
	if entry.Role != "system" || entry.Content != "kept summary" {
		t.Fatalf("entry = %+v, want system/kept summary", entry)
	}
}
