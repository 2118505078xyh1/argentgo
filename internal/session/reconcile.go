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
