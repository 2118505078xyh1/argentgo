package session

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// TestUserMessagesReadOnly 验证 UserMessages 列出整棵会话树的用户消息
// （含不在活动分支上的记录），且不修改会话的活动指针或记录。
func TestUserMessagesReadOnly(t *testing.T) {
	s := newTestSession(t, t.TempDir(), "user-messages")
	first, err := s.AppendRecord(KindUser, "第一条")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendRecord(KindUser, "第二条（主分支）"); err != nil {
		t.Fatal(err)
	}
	// 切回第一条，在分支上追加另一条用户消息：第二条因此离开活动路径。
	if err := s.Checkout(first.ID); err != nil {
		t.Fatal(err)
	}
	branched, err := s.AppendRecord(KindUser, "第三条（分支）")
	if err != nil {
		t.Fatal(err)
	}

	before := s.Messages()
	got := s.UserMessages()
	want := []UserMessage{
		{ID: first.ID, Text: "第一条"},
		{ID: "n000003", Text: "第二条（主分支）"},
		{ID: branched.ID, Text: "第三条（分支）"},
	}
	if len(got) != len(want) {
		t.Fatalf("UserMessages count = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("UserMessages[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	after := s.Messages()
	if len(after) != len(before) {
		t.Fatalf("UserMessages changed active messages: %d -> %d", len(before), len(after))
	}
	for i := range before {
		if before[i].ID != after[i].ID {
			t.Fatalf("active message %d changed: %q -> %q", i, before[i].ID, after[i].ID)
		}
	}
}

// TestForkBeforeRestoresPromptAndTruncates 验证 ForkBefore 在指定用户消息处分叉：
// 返回该消息文本、新会话的对话停在分叉点、原会话保持不变。
func TestForkBeforeRestoresPromptAndTruncates(t *testing.T) {
	dir := t.TempDir()
	s := newTestSession(t, dir, "fork-before")
	if _, err := s.AppendRecord(KindUser, "第一轮提问"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendRecord(KindAssistant, "第一轮回答"); err != nil {
		t.Fatal(err)
	}
	second, err := s.AppendRecord(KindUser, "第二轮提问")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendRecord(KindAssistant, "第二轮回答"); err != nil {
		t.Fatal(err)
	}
	third, err := s.AppendRecord(KindUser, "第三轮提问")
	if err != nil {
		t.Fatal(err)
	}

	newID, prompt, err := s.ForkBefore(second.ID)
	if err != nil {
		t.Fatalf("ForkBefore: %v", err)
	}
	if prompt != "第二轮提问" {
		t.Fatalf("restoredPrompt = %q, want %q", prompt, "第二轮提问")
	}
	if newID == "" || newID == s.ID() {
		t.Fatalf("newID = %q, want a fresh session id", newID)
	}

	fork, err := OpenSession(dir, newID)
	if err != nil {
		t.Fatalf("OpenSession(%q): %v", newID, err)
	}
	messages := fork.Messages()
	// 分叉点的路径上没有后续记录：对话只到第二轮提问（标题记录不是消息）。
	if len(messages) != 3 {
		t.Fatalf("fork messages = %+v, want 3 (user, assistant, user)", messages)
	}
	if messages[len(messages)-1].ID != second.ID || messages[len(messages)-1].Content != prompt {
		t.Fatalf("fork active message = %+v, want %q/%q", messages[len(messages)-1], second.ID, prompt)
	}
	for _, message := range messages {
		if message.ID == third.ID {
			t.Fatal("fork active path still contains the message after the fork point")
		}
	}
	if got := len(s.Messages()); got != 5 {
		t.Fatalf("original messages = %d, want 5 (unchanged)", got)
	}
}

// TestForkBeforeRejectsInvalidID 验证空 id、未知 id 与非用户消息都会被拒绝，
// 且失败时不会创建新会话文件。
func TestForkBeforeRejectsInvalidID(t *testing.T) {
	dir := t.TempDir()
	s := newTestSession(t, dir, "fork-before-invalid")
	assistant, err := s.AppendRecord(KindAssistant, "一条助手回复")
	if err != nil {
		t.Fatal(err)
	}
	before, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"", "does-not-exist", assistant.ID} {
		newID, prompt, err := s.ForkBefore(id)
		if err == nil {
			t.Fatalf("ForkBefore(%q) unexpectedly succeeded (newID=%q)", id, newID)
		}
		if newID != "" || prompt != "" {
			t.Fatalf("ForkBefore(%q) returned %q/%q on error, want empty", id, newID, prompt)
		}
	}
	after, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("failed ForkBefore created %d session file(s)", len(after)-len(before))
	}
}

// TestForkBeforeCJKAndMultibyte 验证 CJK/emoji 等多字节文本能无损地列出并还原，
// 且写入的 JSONL 保留原始 UTF-8（不会被转义成 \\uXXXX）。
func TestForkBeforeCJKAndMultibyte(t *testing.T) {
	dir := t.TempDir()
	s := newTestSession(t, dir, "fork-before-cjk")
	text := "你好，世界 🌍 —— 这是一条包含多字节字符的提问"
	record, err := s.AppendRecord(KindUser, text)
	if err != nil {
		t.Fatal(err)
	}

	listed := s.UserMessages()
	if len(listed) != 1 || listed[0].ID != record.ID || listed[0].Text != text {
		t.Fatalf("UserMessages = %+v, want one entry with %q", listed, text)
	}
	newID, prompt, err := s.ForkBefore(record.ID)
	if err != nil {
		t.Fatalf("ForkBefore: %v", err)
	}
	if prompt != text {
		t.Fatalf("restoredPrompt = %q, want %q", prompt, text)
	}

	fork, err := OpenSession(dir, newID)
	if err != nil {
		t.Fatalf("OpenSession(%q): %v", newID, err)
	}
	if got := fork.Messages(); len(got) != 1 || got[0].Content != text {
		t.Fatalf("fork messages = %+v, want the original CJK text", got)
	}
	data, err := os.ReadFile(fork.path())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`\u`)) {
		t.Fatalf("CJK text was escaped as \\uXXXX: %s", data)
	}
	if !strings.Contains(string(data), text) {
		t.Fatalf("CJK text missing from fork transcript: %s", data)
	}
}
