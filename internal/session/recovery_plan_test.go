package session

import (
	"encoding/json"
	"reflect"
	"testing"
)

// recoveryLink attaches a stable id/parent pair to an entry so parent-chain
// checks have something to compare.
func recoveryLink(entry Entry, id, parent string) Entry {
	entry.ID = id
	entry.Parent = parent
	return entry
}

// recoveryCall builds an assistant entry that declares one or more tool calls.
func recoveryCall(id, parent string, callIDs ...string) Entry {
	return recoveryLink(reconcilerCall(callIDs...), id, parent)
}

// recoveryResult builds a real (non-synthetic) tool result entry.
func recoveryResult(id, parent, callID, output string) Entry {
	return recoveryLink(reconcilerResult(callID, output), id, parent)
}

func TestClassifyRecoveryClean(t *testing.T) {
	messages := []Entry{
		{ID: "n1", Role: "user", Content: "read a file"},
		recoveryCall("n2", "n1", "c1"),
		recoveryResult("n3", "n2", "c1", "file contents"),
		{ID: "n4", Parent: "n3", Role: "assistant", Content: "done"},
	}
	plan := ClassifyRecovery(messages)

	if plan.Category != RecoveryClean {
		t.Errorf("category = %q, want clean", plan.Category)
	}
	if !plan.CanAutoContinue {
		t.Error("clean plan must allow automatic continuation")
	}
	if plan.RequiresUserConfirmation {
		t.Error("clean plan must not require user confirmation")
	}
	if plan.Announcement != "" {
		t.Errorf("clean announcement = %q, want empty", plan.Announcement)
	}
	if plan.BrokenLinks != 0 || len(plan.OrphanToolCalls) != 0 || len(plan.OrphanToolResults) != 0 {
		t.Errorf("clean plan has anomalies: %+v", plan)
	}
	if plan.PendingPrompt || plan.InterruptedAssistant {
		t.Errorf("clean plan reports an interruption: %+v", plan)
	}
}

func TestClassifyRecoveryEmptyIsClean(t *testing.T) {
	for name, messages := range map[string][]Entry{
		"nil":   nil,
		"empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			plan := ClassifyRecovery(messages)
			if plan.Category != RecoveryClean || !plan.CanAutoContinue ||
				plan.RequiresUserConfirmation || plan.Announcement != "" {
				t.Fatalf("empty transcript classified as %+v, want clean", plan)
			}
		})
	}
}

func TestClassifyRecoveryInterruptedPrompt(t *testing.T) {
	messages := []Entry{
		{ID: "n1", Role: "user", Content: "first question"},
		{ID: "n2", Parent: "n1", Role: "assistant", Content: "first answer"},
		{ID: "n3", Parent: "n2", Role: "user", Content: "second question"},
	}
	plan := ClassifyRecovery(messages)

	if plan.Category != RecoveryInterruptedPrompt {
		t.Errorf("category = %q, want interrupted_prompt", plan.Category)
	}
	if !plan.CanAutoContinue || plan.RequiresUserConfirmation {
		t.Errorf("interrupted prompt flags = (auto %v, confirm %v), want (true, false)",
			plan.CanAutoContinue, plan.RequiresUserConfirmation)
	}
	if plan.Announcement != announceInterruptedPrompt {
		t.Errorf("announcement = %q, want %q", plan.Announcement, announceInterruptedPrompt)
	}
	if !plan.PendingPrompt {
		t.Error("pending prompt not reported")
	}
	if plan.BrokenLinks != 0 || len(plan.OrphanToolCalls) != 0 {
		t.Errorf("unexpected anomalies: %+v", plan)
	}
}

func TestClassifyRecoveryInterruptedTurnDanglingCall(t *testing.T) {
	messages := []Entry{
		{ID: "n1", Role: "user", Content: "read a file"},
		recoveryCall("n2", "n1", "c1"),
	}
	plan := ClassifyRecovery(messages)

	if plan.Category != RecoveryInterruptedTurn {
		t.Errorf("category = %q, want interrupted_turn", plan.Category)
	}
	if !plan.CanAutoContinue || plan.RequiresUserConfirmation {
		t.Errorf("interrupted turn flags = (auto %v, confirm %v), want (true, false)",
			plan.CanAutoContinue, plan.RequiresUserConfirmation)
	}
	if plan.Announcement != announceInterruptedTurn {
		t.Errorf("announcement = %q, want %q", plan.Announcement, announceInterruptedTurn)
	}
	if !plan.InterruptedAssistant {
		t.Error("interrupted assistant not reported")
	}
	if !reflect.DeepEqual(plan.OrphanToolCalls, []string{"c1"}) {
		t.Errorf("orphan calls = %v, want [c1]", plan.OrphanToolCalls)
	}
}

func TestClassifyRecoveryInterruptedTurnTrailingResult(t *testing.T) {
	// The tool batch finished but the final assistant answer was never written.
	messages := []Entry{
		{ID: "n1", Role: "user", Content: "read a file"},
		recoveryCall("n2", "n1", "c1"),
		recoveryResult("n3", "n2", "c1", "file contents"),
	}
	plan := ClassifyRecovery(messages)

	if plan.Category != RecoveryInterruptedTurn {
		t.Errorf("category = %q, want interrupted_turn", plan.Category)
	}
	if !plan.InterruptedAssistant {
		t.Error("trailing tool result must be reported as an interrupted assistant")
	}
	if len(plan.OrphanToolCalls) != 0 {
		t.Errorf("paired call must not be an orphan: %v", plan.OrphanToolCalls)
	}
}

func TestClassifyRecoveryDegradedHistoryBrokenLink(t *testing.T) {
	messages := []Entry{
		{ID: "n1", Role: "user", Content: "question"},
		{ID: "n2", Parent: "n1", Role: "assistant", Content: "answer"},
		{ID: "n3", Parent: "missing-parent", Role: "assistant", Content: "later answer"},
	}
	plan := ClassifyRecovery(messages)

	if plan.Category != RecoveryDegradedHistory {
		t.Errorf("category = %q, want degraded_history", plan.Category)
	}
	if plan.CanAutoContinue {
		t.Error("degraded history must disable automatic continuation")
	}
	if !plan.RequiresUserConfirmation {
		t.Error("degraded history must require user confirmation")
	}
	if plan.Announcement != announceDegradedHistory {
		t.Errorf("announcement = %q, want %q", plan.Announcement, announceDegradedHistory)
	}
	if plan.BrokenLinks != 1 {
		t.Errorf("broken links = %d, want 1", plan.BrokenLinks)
	}
}

func TestClassifyRecoveryDegradedHistoryOrphanResult(t *testing.T) {
	messages := []Entry{
		{ID: "n1", Role: "user", Content: "question"},
		recoveryResult("n2", "n1", "ghost-call", "stray output"),
		{ID: "n3", Parent: "n2", Role: "assistant", Content: "answer"},
	}
	plan := ClassifyRecovery(messages)

	if plan.Category != RecoveryDegradedHistory {
		t.Errorf("category = %q, want degraded_history", plan.Category)
	}
	if plan.CanAutoContinue || !plan.RequiresUserConfirmation {
		t.Errorf("degraded flags = (auto %v, confirm %v), want (false, true)",
			plan.CanAutoContinue, plan.RequiresUserConfirmation)
	}
	if !reflect.DeepEqual(plan.OrphanToolResults, []string{"ghost-call"}) {
		t.Errorf("orphan results = %v, want [ghost-call]", plan.OrphanToolResults)
	}
}

func TestClassifyRecoveryDeduplicatesOrphans(t *testing.T) {
	// The same call id is declared twice (a retry) and two results refer to a
	// call that never existed. Both lists must be deduplicated and stable.
	messages := []Entry{
		{ID: "n1", Role: "user", Content: "question"},
		recoveryCall("n2", "n1", "c1", "c1"),
		recoveryCall("n3", "n2", "c1"),
		recoveryResult("n4", "n3", "ghost", "one"),
		recoveryResult("n5", "n4", "ghost", "two"),
	}
	plan := ClassifyRecovery(messages)

	if !reflect.DeepEqual(plan.OrphanToolCalls, []string{"c1"}) {
		t.Errorf("orphan calls = %v, want [c1]", plan.OrphanToolCalls)
	}
	if !reflect.DeepEqual(plan.OrphanToolResults, []string{"ghost"}) {
		t.Errorf("orphan results = %v, want [ghost]", plan.OrphanToolResults)
	}
	if plan.Category != RecoveryDegradedHistory {
		t.Errorf("orphan results must degrade history, got %q", plan.Category)
	}
}

func TestClassifyRecoveryReplayedTranscriptKeepsOrphanAndChain(t *testing.T) {
	sess := newTestSession(t, t.TempDir(), "recovery-replay")
	callData, _ := json.Marshal(map[string]any{
		"role":       "assistant",
		"content":    "calling a tool",
		"tool_calls": []any{map[string]any{"id": "replay-call", "type": "function"}},
	})
	if _, err := sess.AppendRecord(KindAssistant, json.RawMessage(callData)); err != nil {
		t.Fatalf("AppendRecord(assistant): %v", err)
	}

	messages := sess.Messages()
	if len(messages) != 2 || !isSyntheticAborted(messages[1]) {
		t.Fatalf("replay did not synthesize an aborted result: %+v", messages)
	}

	plan := ClassifyRecovery(messages)
	if plan.Category != RecoveryInterruptedTurn {
		t.Errorf("category = %q, want interrupted_turn", plan.Category)
	}
	if !reflect.DeepEqual(plan.OrphanToolCalls, []string{"replay-call"}) {
		t.Errorf("synthetic result must not hide the orphan call: %v", plan.OrphanToolCalls)
	}
	if plan.BrokenLinks != 0 {
		t.Errorf("synthetic result broke the parent chain: %d links", plan.BrokenLinks)
	}
}

func TestClassifyRecoveryCompactionBoundaryIsNotBroken(t *testing.T) {
	// The kept suffix after compaction points at pre-compaction parents, not
	// at the compaction summary that now precedes it. That boundary is not a
	// broken link.
	messages := []Entry{
		{ID: "c1", Parent: "n0", Role: "system", Content: "## Compaction\nsummary"},
		{ID: "n5", Parent: "n4", Role: "assistant", Content: "final answer"},
	}
	plan := ClassifyRecovery(messages)

	if plan.BrokenLinks != 0 {
		t.Errorf("compaction boundary counted as %d broken links", plan.BrokenLinks)
	}
	if plan.Category != RecoveryClean {
		t.Errorf("category = %q, want clean", plan.Category)
	}
}

func TestClassifyRecoveryDoesNotMutateInput(t *testing.T) {
	messages := []Entry{
		{ID: "n1", Role: "user", Content: "question"},
		recoveryCall("n2", "n1", "c1"),
		recoveryResult("n3", "n2", "ghost", "stray"),
	}
	before := append([]Entry(nil), messages...)

	_ = ClassifyRecovery(messages)

	if !reflect.DeepEqual(messages, before) {
		t.Fatalf("input was mutated:\n got %+v\nwant %+v", messages, before)
	}
}

func TestClassifyRecoveryAutoContinueSemantics(t *testing.T) {
	cases := []struct {
		name      string
		messages  []Entry
		want      RecoveryCategory
		auto      bool
		confirm   bool
		announced bool
	}{
		{
			name:     "clean",
			messages: []Entry{{ID: "n1", Role: "assistant", Content: "done"}},
			want:     RecoveryClean, auto: true, confirm: false, announced: false,
		},
		{
			name:     "interrupted prompt",
			messages: []Entry{{ID: "n1", Role: "user", Content: "pending"}},
			want:     RecoveryInterruptedPrompt, auto: true, confirm: false, announced: true,
		},
		{
			name:     "interrupted turn",
			messages: []Entry{{ID: "n1", Role: "assistant", Content: "calling"}, recoveryCall("n2", "n1", "c1")},
			want:     RecoveryInterruptedTurn, auto: true, confirm: false, announced: true,
		},
		{
			name:     "degraded history",
			messages: []Entry{{ID: "n1", Role: "assistant", Content: "one"}, {ID: "n2", Parent: "wrong", Role: "assistant", Content: "two"}},
			want:     RecoveryDegradedHistory, auto: false, confirm: true, announced: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := ClassifyRecovery(tc.messages)
			if plan.Category != tc.want {
				t.Errorf("category = %q, want %q", plan.Category, tc.want)
			}
			if plan.CanAutoContinue != tc.auto {
				t.Errorf("CanAutoContinue = %v, want %v", plan.CanAutoContinue, tc.auto)
			}
			if plan.RequiresUserConfirmation != tc.confirm {
				t.Errorf("RequiresUserConfirmation = %v, want %v", plan.RequiresUserConfirmation, tc.confirm)
			}
			if (plan.Announcement != "") != tc.announced {
				t.Errorf("Announcement = %q, announced = %v, want %v", plan.Announcement, plan.Announcement != "", tc.announced)
			}
		})
	}
}
