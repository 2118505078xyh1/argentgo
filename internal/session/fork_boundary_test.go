package session

import (
	"strings"
	"testing"
)

// TestForkBoundary 验证 fork 出的会话末尾有 developer 边界指令 + 稳定边界标记，
// 分叉前的 prompt 只作为历史保留，不会被当作本轮仍待执行的指令。
func TestForkBoundary(t *testing.T) {
	dir := t.TempDir()
	s := newTestSession(t, dir, "fork-boundary")
	first, err := s.AppendRecord(KindUser, "old task: delete everything")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendRecord(KindAssistant, "old answer"); err != nil {
		t.Fatal(err)
	}
	newID, prompt, err := s.ForkBefore(first.ID)
	if err != nil {
		t.Fatalf("ForkBefore: %v", err)
	}
	if prompt != "old task: delete everything" {
		t.Fatalf("restoredPrompt = %q", prompt)
	}
	fork, err := OpenSession(dir, newID)
	if err != nil {
		t.Fatal(err)
	}
	messages := fork.Messages()
	last := messages[len(messages)-1]
	if !strings.HasPrefix(last.Content, ForkBoundaryMarker) {
		t.Fatalf("last message missing stable boundary marker: %q", last.Content)
	}
	var developer string
	for _, message := range messages {
		if message.Role == "developer" {
			developer = message.Content
		}
	}
	if !strings.Contains(developer, ForkBoundaryMarker) ||
		!strings.Contains(developer, "inherited history") {
		t.Fatalf("developer boundary instruction missing: %q", developer)
	}
	// 边界文本本身不复述旧任务，避免把旧 prompt 重新作为指令注入。
	if strings.Contains(last.Content, "delete everything") || strings.Contains(developer, "delete everything") {
		t.Fatal("boundary text repeated the inherited prompt as an instruction")
	}
}
