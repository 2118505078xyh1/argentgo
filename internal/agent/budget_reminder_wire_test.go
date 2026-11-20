package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// reminderState builds a run state with the reminder window seeded the same way
// newRunState seeds it, so the first check is not mistaken for a new window.
func reminderState(window int) *runState {
	return &runState{compactThreshold: window, reminderWindow: window}
}

func countReminderMessages(messages []llm.Message) int {
	n := 0
	for _, msg := range messages {
		if msg.Role == "user" && strings.HasPrefix(msg.Content, budgetReminderPrefix) {
			n++
		}
	}
	return n
}

func TestBudgetReminderFiresOncePerTier(t *testing.T) {
	s := reminderState(1000)

	// Below the first milestone: silent, nothing recorded.
	s.promptTokens = 400
	s.maybeBudgetReminder()
	if got := countReminderMessages(s.messages); got != 0 {
		t.Fatalf("below half: %d reminders, want 0", got)
	}
	if len(s.reminderDelivered) != 0 {
		t.Fatalf("below half: delivered = %v, want empty", s.reminderDelivered)
	}

	// Crossing 50% announces tier 1 exactly once.
	s.promptTokens = 600
	s.maybeBudgetReminder()
	if got := countReminderMessages(s.messages); got != 1 {
		t.Fatalf("crossing 50%%: %d reminders, want 1", got)
	}
	if !s.reminderDelivered[1] {
		t.Fatalf("tier 1 not recorded in %v", s.reminderDelivered)
	}
	// Staying in the same tier must not repeat, even with a higher usage.
	s.promptTokens = 700
	s.maybeBudgetReminder()
	if got := countReminderMessages(s.messages); got != 1 {
		t.Fatalf("same tier: %d reminders, want 1", got)
	}

	// Crossing 75% announces tier 2; the delivered map holds both announcements.
	s.promptTokens = 800
	s.maybeBudgetReminder()
	if got := countReminderMessages(s.messages); got != 2 {
		t.Fatalf("crossing 75%%: %d reminders, want 2", got)
	}
	if !s.reminderDelivered[2] {
		t.Fatalf("tier 2 not recorded in %v", s.reminderDelivered)
	}
	// Every injected reminder is also surfaced as a frontend event.
	events := s.eventsSnapshot()
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	for _, ev := range events {
		if ev.Kind != EventBudgetReminder {
			t.Fatalf("event kind = %q, want %q", ev.Kind, EventBudgetReminder)
		}
		if !strings.HasPrefix(ev.Text, budgetReminderPrefix) {
			t.Fatalf("event text %q missing prefix %q", ev.Text, budgetReminderPrefix)
		}
	}
}

func TestBudgetReminderNewWindowResetsAfterCompaction(t *testing.T) {
	s := reminderState(1000)
	s.promptTokens = 600
	s.maybeBudgetReminder()
	if got := countReminderMessages(s.messages); got != 1 {
		t.Fatalf("first window: %d reminders, want 1", got)
	}

	// Compaction produces a fresh context window: the same occupancy may warn
	// again, then is deduplicated within the new window.
	s.resetBudgetReminder()
	s.promptTokens = 600
	s.maybeBudgetReminder()
	if got := countReminderMessages(s.messages); got != 2 {
		t.Fatalf("new window: %d reminders, want 2", got)
	}
	s.maybeBudgetReminder()
	if got := countReminderMessages(s.messages); got != 2 {
		t.Fatalf("new window, same tier: %d reminders, want 2", got)
	}
}

func TestBudgetReminderWindowSizeChangeIsNewWindow(t *testing.T) {
	s := reminderState(1000)
	s.promptTokens = 600
	s.maybeBudgetReminder() // tier 1 delivered
	if got := countReminderMessages(s.messages); got != 1 {
		t.Fatalf("initial: %d reminders, want 1", got)
	}

	// A changed window invalidates the old deliveries: ShouldRemind treats it as
	// a new window and warns even at tier 0 (600/2000 is below the first tier).
	s.compactThreshold = 2000
	s.maybeBudgetReminder()
	if got := countReminderMessages(s.messages); got != 2 {
		t.Fatalf("window change: %d reminders, want 2", got)
	}
	if !s.reminderDelivered[0] || len(s.reminderDelivered) != 1 {
		t.Fatalf("new window deliveries = %v, want only tier 0", s.reminderDelivered)
	}
	if s.reminderWindow != 2000 {
		t.Fatalf("reminderWindow = %d, want 2000", s.reminderWindow)
	}
}

func TestBudgetReminderUnconfiguredIsNoop(t *testing.T) {
	s := &runState{
		messages:     []llm.Message{{Role: "system", Content: "rules"}, {Role: "user", Content: "q"}},
		promptTokens: 5000, // would cross every tier if a window existed
	}
	before := append([]llm.Message(nil), s.messages...)
	s.maybeBudgetReminder()
	if len(s.messages) != len(before) {
		t.Fatalf("messages changed: %d -> %d", len(before), len(s.messages))
	}
	for i := range before {
		if s.messages[i].Role != before[i].Role || s.messages[i].Content != before[i].Content {
			t.Fatalf("message %d changed: %+v -> %+v", i, before[i], s.messages[i])
		}
	}
	if len(s.eventsSnapshot()) != 0 {
		t.Fatalf("unconfigured run emitted events: %+v", s.eventsSnapshot())
	}
	if s.reminderDelivered != nil || s.reminderWindow != 0 {
		t.Fatalf("unconfigured run touched reminder state: delivered=%v window=%d",
			s.reminderDelivered, s.reminderWindow)
	}

	// A trim-only MaxContextTokens is not a reminder window: configuring it
	// must keep messages byte-for-byte identical too.
	trimOnly := &runState{opts: Options{MaxContextTokens: 1000}, promptTokens: 5000}
	trimOnly.maybeBudgetReminder()
	if len(trimOnly.messages) != 0 || len(trimOnly.eventsSnapshot()) != 0 {
		t.Fatalf("trim-only run injected a reminder: messages=%+v events=%+v",
			trimOnly.messages, trimOnly.eventsSnapshot())
	}
}

func TestBudgetReminderReachesModelInNextRequest(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			reply := toolReply(toolCall("c1", "read", `{}`))
			reply.Usage = &llm.Usage{PromptTokens: 600}
			return reply, nil
		}
		return textReply("final"), nil
	}}
	var events []Event
	runner := &Runner{Opts: Options{
		Model:            "m",
		Chatter:          chat,
		Tools:            tools,
		MaxTurns:         3,
		CompactThreshold: 1000,
		Emit: func(e Event) {
			events = append(events, e)
		},
	}}

	if _, err := runner.Run(context.Background(), "inspect"); err != nil {
		t.Fatalf("run: %v", err)
	}
	requests := chat.requestsSeen()
	if len(requests) < 2 {
		t.Fatalf("requests = %d, want at least 2", len(requests))
	}
	// The model must actually read the reminder on the turn after it was injected.
	if got := countReminderMessages(requests[1]); got != 1 {
		t.Fatalf("second request carried %d reminders, want 1", got)
	}
	var emitted int
	for _, e := range events {
		if e.Kind == EventBudgetReminder {
			emitted++
		}
	}
	if emitted != 1 {
		t.Fatalf("EventBudgetReminder emitted %d times, want 1", emitted)
	}
}

func TestBudgetReminderUnconfiguredRunNeverReachesModel(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			reply := toolReply(toolCall("c1", "read", `{}`))
			reply.Usage = &llm.Usage{PromptTokens: 600}
			return reply, nil
		}
		return textReply("final"), nil
	}}
	runner := &Runner{Opts: Options{
		Model:    "m",
		Chatter:  chat,
		Tools:    tools,
		MaxTurns: 3,
	}}

	if _, err := runner.Run(context.Background(), "inspect"); err != nil {
		t.Fatalf("run: %v", err)
	}
	for i, request := range chat.requestsSeen() {
		if got := countReminderMessages(request); got != 0 {
			t.Fatalf("request %d carried %d reminders on an unconfigured run", i, got)
		}
	}
}
