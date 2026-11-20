package tui

import (
	"strings"
	"testing"
	"time"
)

// TestRetryEventPublishesSeverityStatusNotice checks that an EventRetry is
// mirrored onto the severity-ranked status line (NoticeStack), carrying the
// attempt, backoff delay and reason in one visible string. Keeping it on the
// status line as well as the transcript is what makes backoff live feedback
// rather than a permanent scroll of history rows.
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

// TestRetryStatusAndTranscriptRedactSecret verifies the wire-safe promise
// end-to-end: a credential-shaped reason is redacted in both the status notice
// and the transcript entry, never painted verbatim.
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

// TestRetryStatusNoticePaintedInFrame checks that the live retry hint reaches
// the rendered frame, not just the model's internal stack.
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

// TestRetryStatusNoticeExpiresOnTTL checks that the live hint retires itself
// after its TTL, keeping a transient backoff hint off the status line forever.
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

	// Still live just before expiry.
	m.PruneNotices(n.At.Add(DefaultNoticeTTL - time.Nanosecond))
	if got, ok := m.notices.Peek(); !ok || got.Seq != n.Seq {
		t.Fatal("retry notice should still be live before TTL")
	}
	// Gone at the TTL boundary.
	m.PruneNotices(n.At.Add(DefaultNoticeTTL))
	if _, ok := m.notices.Peek(); ok {
		t.Fatal("retry notice should have expired at its TTL")
	}
}

// TestRetryEventDoesNotDisturbToolEvents guards against regressions: a retry
// interleaved with tool events must not reorder, drop or mutate those tasks.
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
