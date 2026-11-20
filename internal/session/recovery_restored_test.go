package session

import (
	"encoding/json"
	"fmt"
	"testing"
)

// TestClassifyRecoveryRestoredRecordsNotBroken 覆盖压缩与保留标记交互产生的真实 bug：
// 带保留标记的记录在压缩后被重新发出，其 parent 指向压缩记录（在会话投影中表现为
// 系统摘要条目），而不是投影中紧邻的前一条消息。朴素的相邻链接检查会把这条合法链接
// 算作断链，使健康的会话在恢复时被误判为 degraded_history 并禁用自动继续。
func TestClassifyRecoveryRestoredRecordsNotBroken(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.NewSession("restored")
	if err != nil {
		t.Fatal(err)
	}

	// 前缀中的一条消息标记为 RetainAlways，压缩时会随其他被丢弃的消息一起重放。
	for i := 0; i < 5; i++ {
		if i == 2 {
			payload, _ := json.Marshal(map[string]any{"role": "user", "content": "pinned"})
			if _, err := s.AppendWithRetention(KindMessage, payload, RetainAlways); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := s.Append(KindUser, fmt.Sprintf("m%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Compact(1); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	// 压缩之后继续追加消息，使重放记录夹在摘要与后续消息之间。
	for i := 5; i < 9; i++ {
		if err := s.Append(KindUser, fmt.Sprintf("m%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	msgs := s.Messages()
	// 投影必须先出现系统摘要，随后出现重放记录，构成被测场景。
	foundSummary, foundRestored := false, false
	for _, e := range msgs {
		if e.Role == "system" && e.Content != "" {
			foundSummary = true
		}
		if e.Content == "pinned" && e.Role == "user" && e.ID != "" {
			foundRestored = true
		}
	}
	if !foundSummary || !foundRestored {
		t.Fatalf("投影缺少摘要或重放记录：%+v", msgs)
	}

	plan := ClassifyRecovery(msgs)
	if plan.BrokenLinks != 0 {
		t.Errorf("断链数 = %d，want 0（重放记录指向压缩摘要是合法链接）", plan.BrokenLinks)
	}
	// 场景末尾是一条用户消息，因此正确的类别是 interrupted_prompt；关键是不应被
	// 误判为 degraded_history（那会禁用自动继续并要求人工确认）。
	if plan.Category == RecoveryDegradedHistory || plan.Category != RecoveryInterruptedPrompt {
		t.Errorf("类别 = %q，want %q", plan.Category, RecoveryInterruptedPrompt)
	}
	if !plan.CanAutoContinue || plan.RequiresUserConfirmation {
		t.Errorf("该会话不应要求人工确认：canContinue=%v needConfirm=%v",
			plan.CanAutoContinue, plan.RequiresUserConfirmation)
	}
}

// TestCountBrokenLinksSystemBoundaryIsValid 直接验证 countBrokenLinks：
// 指向系统摘要条目的 parent 有效，指向真正缺失记录的 parent 仍须被判为断链。
func TestCountBrokenLinksSystemBoundaryIsValid(t *testing.T) {
	messages := []Entry{
		{ID: "summary", Role: "system", Content: "## Compaction\nsummary"},
		{ID: "kept", Parent: "old", Role: "user", Content: "kept"},
		{ID: "restored", Parent: "summary", Role: "user", Content: "restored"},
	}
	if got := countBrokenLinks(messages); got != 0 {
		t.Errorf("断链数 = %d，want 0", got)
	}

	messages = append(messages, Entry{ID: "bad", Parent: "missing", Role: "user", Content: "bad"})
	if got := countBrokenLinks(messages); got != 1 {
		t.Errorf("含真实缺失 parent 时断链数 = %d，want 1", got)
	}
}
