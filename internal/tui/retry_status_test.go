package tui

import (
	"strings"
	"testing"
	"time"
)

// TestRetryEventPublishesSeverityStatusNotice 检查 EventRetry 被
// 镜像到按严重度排序的状态行（NoticeStack），并在一个可见字符串中
// 携带尝试次数、退避延迟和原因。把它同时放在状态行
// 和记录中，才使退避成为实时反馈
// 而不是永久滚动的历史行。
func TestRetryEventPublishesSeverityStatusNotice(t *testing.T) {
	m := New(Config{})
	m.HandleEvent(Event{
		Kind:         EventRetry,
		RetryReason:  "timeout",
		RetryAttempt: 1,
		RetryMax:     3,
		RetryWaitMS:  500,
	})

	n, ok := m.notices.Peek()
	if !ok {
		t.Fatal("EventRetry did not publish a status notice")
	}
	if n.Severity != SeverityWarn {
		t.Fatalf("severity = %v, want SeverityWarn", n.Severity)
	}
	want := "⟳ retry timeout in 500ms (1/3)"
	if n.Text != want {
		t.Fatalf("status text = %q, want %q", n.Text, want)
	}
	for _, field := range []string{"timeout", "500ms", "1/3"} {
		if !strings.Contains(n.Text, field) {
			t.Fatalf("status notice %q missing field %q", n.Text, field)
		}
	}
}

// TestRetryStatusAndTranscriptRedactSecret 端到端验证传输安全承诺：
// 形如凭据的原因在状态通知和记录条目中都被清洗，
// 绝不逐字绘制。
func TestRetryStatusAndTranscriptRedactSecret(t *testing.T) {
	m := New(Config{})
	m.HandleEvent(Event{
		Kind:         EventRetry,
		RetryReason:  "key sk-proj-abcdefghijklmnop rejected",
		RetryAttempt: 2,
		RetryMax:     5,
		RetryWaitMS:  1500,
	})

	const secret = "sk-proj-abcdefghijklmnop"
	n, ok := m.notices.Peek()
	if !ok {
		t.Fatal("missing status notice")
	}
	if strings.Contains(n.Text, secret) {
		t.Fatalf("status notice leaked secret: %q", n.Text)
	}
	if !strings.Contains(n.Text, "[redacted]") {
		t.Fatalf("status notice missing redaction marker: %q", n.Text)
	}
	entries := m.Transcript()
	if len(entries) != 1 || strings.Contains(entries[0].Text, secret) {
		t.Fatalf("transcript leaked secret: %+v", entries)
	}
}

// TestRetryStatusNoticePaintedInFrame 检查实时重试提示到达
// 渲染后的画面，而不只是模型的内部栈。
func TestRetryStatusNoticePaintedInFrame(t *testing.T) {
	m := New(Config{})
	m.HandleEvent(Event{
		Kind:         EventRetry,
		RetryReason:  "sse reset",
		RetryAttempt: 1,
		RetryMax:     3,
		RetryWaitMS:  750,
	})

	text := frameText(m, 100, 24)
	want := "⟳ retry sse reset in 750ms (1/3)"
	if !strings.Contains(text, want) {
		t.Fatalf("retry hint not painted in frame:\n%s", text)
	}
}

// TestRetryStatusNoticeExpiresOnTTL 检查实时提示在其 TTL 后
// 自行退休，使瞬时退避提示不会永远留在状态行。
func TestRetryStatusNoticeExpiresOnTTL(t *testing.T) {
	m := New(Config{})
	m.HandleEvent(Event{
		Kind:         EventRetry,
		RetryReason:  "timeout",
		RetryAttempt: 1,
		RetryMax:     3,
		RetryWaitMS:  500,
	})
	n, ok := m.notices.Peek()
	if !ok {
		t.Fatal("missing status notice")
	}

	// 到期前一刻仍然存活。
	m.PruneNotices(n.At.Add(DefaultNoticeTTL - time.Nanosecond))
	if got, ok := m.notices.Peek(); !ok || got.Seq != n.Seq {
		t.Fatal("retry notice should still be live before TTL")
	}
	// 在 TTL 边界处消失。
	m.PruneNotices(n.At.Add(DefaultNoticeTTL))
	if _, ok := m.notices.Peek(); ok {
		t.Fatal("retry notice should have expired at its TTL")
	}
}

// TestRetryEventDoesNotDisturbToolEvents 防止回归：与工具事件
// 交错的重试不得重排、丢弃或修改那些任务。
func TestRetryEventDoesNotDisturbToolEvents(t *testing.T) {
	m := New(Config{})
	m.HandleEvent(Event{Kind: EventToolStart, ToolCallID: "1", ToolName: "read", Args: `{"path":"a.go"}`})
	if len(m.tasks) != 1 || m.tasks[0].Status != TaskRunning {
		t.Fatalf("tool start did not create a running task: %+v", m.tasks)
	}

	m.HandleEvent(Event{Kind: EventRetry, RetryReason: "timeout", RetryAttempt: 1, RetryMax: 3, RetryWaitMS: 500})
	if len(m.tasks) != 1 || m.tasks[0].Status != TaskRunning {
		t.Fatalf("retry disturbed the task list: %+v", m.tasks)
	}
	entries := m.Transcript()
	if len(entries) != 1 || entries[0].Text != "⟳ retry timeout in 500ms (1/3)" {
		t.Fatalf("retry entry = %+v", entries)
	}

	m.HandleEvent(Event{Kind: EventToolEnd, ToolCallID: "1", ToolName: "read", DurationMS: 12})
	if len(m.tasks) != 1 || m.tasks[0].Status != TaskOK {
		t.Fatalf("tool end did not complete the task: %+v", m.tasks)
	}
	entries = m.Transcript()
	if len(entries) != 1 || !strings.Contains(entries[0].Text, "retry timeout") {
		t.Fatalf("later tool event altered the retry entry: %+v", entries)
	}
}
