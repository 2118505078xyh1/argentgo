package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func TestStreamErrorEntryBuildsRedactedBoundedNotice(t *testing.T) {
	detail := "sse reset; key=sk-proj-abcdefghijklmnop"
	entry := StreamErrorEntry(detail, llm.RedactText)

	if entry.Kind != EntryNotice {
		t.Fatalf("kind = %v, want EntryNotice", entry.Kind)
	}
	if entry.Attempt != AttemptStreamError {
		t.Fatalf("attempt = %q, want %q", entry.Attempt, AttemptStreamError)
	}
	if strings.Contains(entry.Text, "sk-proj-abcdefghijklmnop") {
		t.Fatalf("detail leaked a secret: %q", entry.Text)
	}
	if !strings.Contains(entry.Text, "[redacted]") {
		t.Fatalf("detail missing redaction marker: %q", entry.Text)
	}
	if got := TextWidth(entry.Text); got > maxStreamErrorDetail {
		t.Fatalf("detail width = %d, want <= %d", got, maxStreamErrorDetail)
	}
}

func TestStreamErrorEntryCollapsesWhitespaceAndHonorsRedactor(t *testing.T) {
	entry := StreamErrorEntry("sse   reset\n again", func(s string) string { return "R(" + s + ")" })
	if entry.Text != "R(sse reset again)" {
		t.Fatalf("text = %q, want collapsed + redacted", entry.Text)
	}

	plain := StreamErrorEntry("boom", nil)
	if plain.Text != "boom" {
		t.Fatalf("nil redactor changed text: %q", plain.Text)
	}
}

func TestStreamErrorEntryBoundsLongMultibyteDetail(t *testing.T) {
	entry := StreamErrorEntry(strings.Repeat("字", maxStreamErrorDetail+50), nil)
	if got := TextWidth(entry.Text); got > maxStreamErrorDetail {
		t.Fatalf("width = %d, want <= %d", got, maxStreamErrorDetail)
	}
	if !strings.HasSuffix(entry.Text, "…") {
		t.Fatalf("long detail not marked truncated: %q", entry.Text)
	}
}

func TestRetryNoticeFormatIsStable(t *testing.T) {
	got := RetryNotice("sse reset", 2, 5, 1500)
	want := "⟳ retry sse reset in 1500ms (2/5)"
	if got != want {
		t.Fatalf("notice = %q, want %q", got, want)
	}
	if again := RetryNotice("sse reset", 2, 5, 1500); again != got {
		t.Fatalf("notice is not deterministic: %q != %q", again, got)
	}
}

func TestRetryNoticeRedactsBoundsAndClamps(t *testing.T) {
	redacted := RetryNotice("key sk-proj-abcdefghijklmnop", 1, 3, 100)
	if strings.Contains(redacted, "sk-proj-abcdefghijklmnop") {
		t.Fatalf("reason leaked a secret: %q", redacted)
	}

	long := RetryNotice(strings.Repeat("x", 5000), 1, 3, 100)
	if !strings.Contains(long, "…") {
		t.Fatalf("long reason not marked truncated: %q", long)
	}
	if TextWidth(long) > maxRetryReason+len("⟳ retry  in 100ms (1/3)") {
		t.Fatalf("notice too wide: %d", TextWidth(long))
	}

	clamped := RetryNotice("", -1, -2, -3)
	if clamped != "⟳ retry stream in 0ms (0/0)" {
		t.Fatalf("clamped notice = %q", clamped)
	}
}

func TestErrorTextRedactsCollapsesAndBounds(t *testing.T) {
	got := errorText("en", fmt.Errorf("sse reset api_key: sk-proj-abcdefghijklmnop\nsecond line %s", strings.Repeat("x", 400)))
	if strings.Contains(got, "sk-proj-abcdefghijklmnop") {
		t.Fatalf("errorText leaked a secret: %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("errorText kept a newline: %q", got)
	}
	if TextWidth(got) > TextWidth("error: ")+maxStreamErrorDetail {
		t.Fatalf("errorText not bounded: %d cells", TextWidth(got))
	}
}

func TestHandleEventErrorAppendsBadgedRedactedEntry(t *testing.T) {
	m := New(Config{})
	m.HandleEvent(Event{Kind: EventError, Detail: "sse reset sk-proj-abcdefghijklmnop"})

	entries := m.Transcript()
	if len(entries) != 1 {
		t.Fatalf("transcript length = %d, want 1", len(entries))
	}
	if entries[0].Kind != EntryNotice || entries[0].Attempt != AttemptStreamError {
		t.Fatalf("entry = %+v, want redacted stream-error notice", entries[0])
	}
	if strings.Contains(entries[0].Text, "sk-proj-abcdefghijklmnop") {
		t.Fatalf("transcript leaked a secret: %q", entries[0].Text)
	}
	if !m.Dirty() {
		t.Fatal("EventError did not mark the model dirty")
	}
}

func TestHandleEventRetryAppendsNoticeEntry(t *testing.T) {
	m := New(Config{})
	m.HandleEvent(Event{
		Kind:         EventRetry,
		RetryReason:  "sse reset",
		RetryAttempt: 1,
		RetryMax:     3,
		RetryWaitMS:  500,
	})

	entries := m.Transcript()
	if len(entries) != 1 {
		t.Fatalf("transcript length = %d, want 1", len(entries))
	}
	if entries[0].Kind != EntryNotice {
		t.Fatalf("kind = %v, want EntryNotice", entries[0].Kind)
	}
	if entries[0].Text != "⟳ retry sse reset in 500ms (1/3)" {
		t.Fatalf("text = %q", entries[0].Text)
	}
}

func TestStreamErrorBadgeRendersWithExistingPath(t *testing.T) {
	m := New(Config{}) // default locale is en
	m.HandleEvent(Event{Kind: EventError, Detail: "sse reset"})
	text := frameText(m, 100, 24)
	if !strings.Contains(text, "stream error") || !strings.Contains(text, "sse reset") {
		t.Fatalf("stream-error badge/detail not painted: %s", text)
	}
}
