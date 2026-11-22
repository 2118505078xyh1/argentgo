package agent

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestInjectFollowUpDrainsIntoConversation 验证 follow-up 通道中的消息被作为
// 新用户轮次加入对话，并产生可观测事件与记录。
func TestInjectFollowUpDrainsIntoConversation(t *testing.T) {
	ch := make(chan string, 3)
	ch <- "first follow-up"
	ch <- "second follow-up"
	s := newRunState(Options{Model: "m", System: "s", FollowUp: ch}, "hi", nil)
	before := len(s.messages)

	if !s.injectFollowUp() {
		t.Fatal("injectFollowUp should report injected messages")
	}
	if len(s.messages) != before+2 {
		t.Fatalf("messages = %d, want %d", len(s.messages), before+2)
	}
	if s.messages[before].Content != "first follow-up" || s.messages[before+1].Content != "second follow-up" {
		t.Fatalf("unexpected injected messages: %+v", s.messages[before:])
	}
	// 再调一次应为空（通道已排空）。
	if s.injectFollowUp() {
		t.Fatal("second inject should be empty")
	}
}

// TestInjectFollowUpNilIsNoop 验证未配置 FollowUp 时零分配、原样结束。
func TestInjectFollowUpNilIsNoop(t *testing.T) {
	s := newRunState(Options{Model: "m", System: "s"}, "hi", nil)
	before := len(s.messages)
	if s.injectFollowUp() {
		t.Fatal("nil FollowUp must be a no-op")
	}
	if len(s.messages) != before {
		t.Fatal("nil FollowUp must not change messages")
	}
}

// TestFollowUpChargesAsUserMessage 确认注入的消息是 user 角色（与新轮次语义一致）。
func TestFollowUpChargesAsUserMessage(t *testing.T) {
	ch := make(chan string, 1)
	ch <- "late message"
	s := newRunState(Options{Model: "m", System: "s", FollowUp: ch}, "hi", nil)
	s.injectFollowUp()
	last := s.messages[len(s.messages)-1]
	if last.Role != "user" {
		t.Fatalf("role = %q, want user", last.Role)
	}
	var _ llm.Message = last
}
