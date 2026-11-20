package session

import (
	"crypto/sha256"
	"encoding/hex"
)

const (
	// abortedResultText is the content of a synthetic tool result inserted at
	// replay time for a tool call that never produced output.
	abortedResultText = "[aborted] tool did not run"

	// abortedIDPrefix namespaces deterministic synthetic ids so they cannot
	// collide with ids minted for real records.
	abortedIDPrefix = "session:aborted-tool-result"

	// maxSyntheticResults bounds how many aborted results a single replay may
	// insert, keeping a corrupted transcript from ballooning the projection.
	maxSyntheticResults = 100
)

// abortedResultID derives a stable synthetic result id from a tool call id.
// Replaying the same session twice yields the same id, which matters because
// these ids travel to the model as the partner of a real tool call.
func abortedResultID(callID string) string {
	sum := sha256.Sum256([]byte(abortedIDPrefix + ":" + callID))
	return hex.EncodeToString(sum[:])
}

// ReconcileToolPairs repairs tool call/result pairing in a projected
// conversation. It drops tool results whose call is absent and inserts a
// synthetic aborted result for every tool call that has no result, mirroring
// the replay-time reconciliation performed when a JSONL session is resumed.
//
// The input is never mutated: existing entries are value-copied into a fresh
// slice and synthetic results are appended to that copy. Entries without any
// tool-call metadata pass through unchanged (the input slice is returned
// directly in that case), so clean sessions keep a byte-identical projection.
func ReconcileToolPairs(entries []Entry) []Entry {
	if len(entries) == 0 {
		return entries
	}

	// Pass 1: collect the ids declared by assistant calls and by tool results.
	calls := make(map[string]struct{})
	results := make(map[string]struct{})
	for _, entry := range entries {
		switch entry.Role {
		case "assistant":
			for _, id := range toolCallIDs(entry) {
				calls[id] = struct{}{}
			}
		case "tool":
			if entry.ToolCallID != "" {
				results[entry.ToolCallID] = struct{}{}
			}
		}
	}
	if len(calls) == 0 && len(results) == 0 {
		// No tool metadata at all: nothing to reconcile.
		return entries
	}

	// Pass 2: keep results whose call exists, synthesize results for calls that
	// have none. A synthetic result follows the assistant entry that declares
	// the call (in declaration order) and is inserted at most once per id.
	out := make([]Entry, 0, len(entries)+len(calls))
	synthesized := make(map[string]struct{})
	inserted := 0
	for _, entry := range entries {
		if entry.Role == "tool" && entry.ToolCallID != "" {
			if _, ok := calls[entry.ToolCallID]; !ok {
				// Orphaned result: the call it answers is not in the transcript.
				continue
			}
		}
		out = append(out, entry)

		if entry.Role != "assistant" {
			continue
		}
		for _, id := range toolCallIDs(entry) {
			if _, ok := results[id]; ok {
				continue
			}
			if _, ok := synthesized[id]; ok {
				continue
			}
			if inserted >= maxSyntheticResults {
				continue
			}
			synthesized[id] = struct{}{}
			inserted++
			out = append(out, Entry{
				ID:         abortedResultID(id),
				Role:       "tool",
				Content:    abortedResultText,
				ToolCallID: id,
			})
		}
	}
	return out
}

// RecoveryCategory is the state a transcript was found in when a session is
// resumed. The four values are ordered by how much operator oversight the
// state needs: clean needs none, degraded_history needs a human decision.
type RecoveryCategory string

const (
	RecoveryClean             RecoveryCategory = "clean"
	RecoveryInterruptedPrompt RecoveryCategory = "interrupted_prompt"
	RecoveryInterruptedTurn   RecoveryCategory = "interrupted_turn"
	RecoveryDegradedHistory   RecoveryCategory = "degraded_history"
)

// Visible announcements carried by a RecoveryPlan. They are plain, stable
// strings so this stdlib-only package needs no i18n dependency; a frontend is
// free to localize them by keying off Category instead.
const (
	announceInterruptedPrompt = "Session recovery: the last user prompt had no reply before the run stopped; it will be answered on resume."
	announceInterruptedTurn   = "Session recovery: the previous turn stopped mid-execution; unanswered tool calls will be answered with an aborted result before continuing."
	announceDegradedHistory   = "Session recovery: history is degraded (broken record link or orphaned tool result); automatic continuation is disabled — please confirm before resuming."
)

// RecoveryPlan is a structured, read-only account of the state a conversation
// is in when it is resumed. It deliberately changes nothing: tool call/result
// pairing is still repaired at replay time by ReconcileToolPairs, and physical
// damage is still handled by ValidateAndRepair. A caller uses the plan only to
// decide whether to continue automatically, ask the user, and what to announce.
//
// The detail fields are deduplicated and in first-seen order so two callers
// drawing conclusions from the same transcript see the same values.
type RecoveryPlan struct {
	Category RecoveryCategory

	// CanAutoContinue reports whether the run may proceed without a fresh
	// operator decision. It is false only for degraded_history.
	CanAutoContinue bool
	// RequiresUserConfirmation reports whether an operator must explicitly
	// approve continuation. It is true only for degraded_history.
	RequiresUserConfirmation bool

	// Announcement is the user-visible notice for this plan. It is empty for
	// clean sessions, where there is nothing to report.
	Announcement string

	// OrphanToolCalls lists assistant tool calls that have no real result and
	// are therefore answered by ReconcileToolPairs with a synthetic aborted
	// result. Synthetic results already present in the input do not count as
	// answers, so a replayed transcript classifies the same as a raw one.
	OrphanToolCalls []string
	// OrphanToolResults lists tool results whose call is absent from the
	// transcript; unlike a missing result, a missing call means lost history.
	OrphanToolResults []string
	// BrokenLinks counts parent links that do not point at the preceding
	// message on the linear path.
	BrokenLinks int
	// PendingPrompt is true when the transcript ends with an unanswered user
	// prompt.
	PendingPrompt bool
	// InterruptedAssistant is true when the transcript ends in the middle of
	// an assistant turn (a dangling call or a result with no final answer).
	InterruptedAssistant bool
}

// ClassifyRecovery inspects a projected conversation and returns the recovery
// plan for resuming it. It is pure: the input slice and its entries are never
// mutated. Classification is conservative — a history that cannot be trusted
// is reported as degraded_history so automatic continuation is disabled.
//
// Decision order, most severe first:
//   - a broken parent link or an orphaned tool result => degraded_history;
//   - a trailing unanswered user prompt             => interrupted_prompt;
//   - a trailing tool result or an unanswered call   => interrupted_turn;
//   - anything else                                 => clean.
//
// Synthetic aborted results produced by ReconcileToolPairs are skipped when
// checking parent links (they carry no parent of their own) and never count
// as real answers, so classifying a replayed transcript is equivalent to
// classifying the raw history before replay.
func ClassifyRecovery(messages []Entry) RecoveryPlan {
	plan := RecoveryPlan{Category: RecoveryClean, CanAutoContinue: true}
	if len(messages) == 0 {
		return plan
	}

	// Index declarations and real answers. A synthetic aborted result is not
	// an answer: it is the recovery mechanism itself, not something the model
	// produced. Deduplicate declarations while keeping first-seen order.
	calls := make(map[string]struct{})
	callOrder := make([]string, 0)
	results := make(map[string]struct{})
	for _, entry := range messages {
		switch entry.Role {
		case "assistant":
			for _, id := range toolCallIDs(entry) {
				if id == "" {
					continue
				}
				if _, ok := calls[id]; !ok {
					calls[id] = struct{}{}
					callOrder = append(callOrder, id)
				}
			}
		case "tool":
			if entry.ToolCallID == "" || isSyntheticAborted(entry) {
				continue
			}
			results[entry.ToolCallID] = struct{}{}
		}
	}

	for _, id := range callOrder {
		if _, ok := results[id]; !ok {
			plan.OrphanToolCalls = append(plan.OrphanToolCalls, id)
		}
	}
	seenOrphanResult := make(map[string]struct{})
	for _, entry := range messages {
		if entry.Role != "tool" || isSyntheticAborted(entry) || entry.ToolCallID == "" {
			continue
		}
		if _, ok := calls[entry.ToolCallID]; ok {
			continue
		}
		if _, ok := seenOrphanResult[entry.ToolCallID]; ok {
			continue
		}
		seenOrphanResult[entry.ToolCallID] = struct{}{}
		plan.OrphanToolResults = append(plan.OrphanToolResults, entry.ToolCallID)
	}

	plan.BrokenLinks = countBrokenLinks(messages)
	if plan.BrokenLinks > 0 || len(plan.OrphanToolResults) > 0 {
		plan.Category = RecoveryDegradedHistory
		plan.CanAutoContinue = false
		plan.RequiresUserConfirmation = true
		plan.Announcement = announceDegradedHistory
		return plan
	}

	last := lastRealEntry(messages)
	if last == nil {
		return plan
	}
	switch {
	case last.Role == "user":
		plan.PendingPrompt = true
		plan.Category = RecoveryInterruptedPrompt
		plan.Announcement = announceInterruptedPrompt
	case last.Role == "tool" || len(plan.OrphanToolCalls) > 0:
		plan.InterruptedAssistant = true
		plan.Category = RecoveryInterruptedTurn
		plan.Announcement = announceInterruptedTurn
	}
	return plan
}

// isSyntheticAborted recognizes the aborted results ReconcileToolPairs inserts
// at replay time. They are bookkeeping, not user-visible history.
func isSyntheticAborted(entry Entry) bool {
	return entry.Role == "tool" && entry.Content == abortedResultText
}

// lastRealEntry returns the newest non-synthetic entry, or nil if the
// conversation consists only of synthetic results.
func lastRealEntry(messages []Entry) *Entry {
	for i := len(messages) - 1; i >= 0; i-- {
		if isSyntheticAborted(messages[i]) {
			continue
		}
		return &messages[i]
	}
	return nil
}

// countBrokenLinks checks that each message's parent id is the id of the
// message before it on the linear path. The first entry is a boundary (its
// parent may be a header or a compacted-away record) and is skipped, as is an
// entry following a system compaction summary. Entries with an empty id or
// parent carry no evidence either way and are skipped rather than guessed at.
// Synthetic results are skipped entirely so the real message before them stays
// the link target.
func countBrokenLinks(messages []Entry) int {
	broken := 0
	var prev *Entry
	for i := range messages {
		entry := &messages[i]
		if isSyntheticAborted(*entry) {
			continue
		}
		if prev != nil && prev.Role != "system" &&
			entry.Parent != "" && prev.ID != "" && entry.Parent != prev.ID {
			broken++
		}
		prev = entry
	}
	return broken
}
