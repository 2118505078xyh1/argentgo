package agent

import (
	"context"
	"fmt"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// finalAnswerPrompt is injected as a synthetic user turn after the step budget
// runs out. No tool specs accompany it, so the model is nudged to summarise
// what it already has instead of starting another tool round it cannot afford.
const finalAnswerPrompt = "The maximum number of steps has been reached. Do not call any tools. Give your best final answer now using only the information you already have."

// finalAnswer spends one last request on a tool-free answer when the turn
// budget is exhausted. It uses the normal Chatter, so it stays local and
// offline-testable. A provider failure is not fatal: the run still ends at
// StopMaxTurns, just without a new answer. Cancellation is different — it is
// returned so the loop can report StopCancelled instead of swallowing it into
// a notice and returning a successful result.
func (s *runState) finalAnswer(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	base := len(s.messages)
	s.messages = append(s.messages, llm.Message{Role: "user", Content: finalAnswerPrompt})

	savedSpecs := s.specs
	s.specs = nil // no tools: the reply cannot start another tool round
	turn := s.turns
	// One request only: no fallback chain, so the budget cannot be stretched
	// by retries after it has already been spent. Deltas are buffered and only
	// flushed on success, mirroring chatWithFallback: a cancelled or failed
	// request must not leak a half-written answer into the operator's stream.
	var deltas []string
	reply, err := s.chat(ctx, func(delta string) {
		deltas = append(deltas, delta)
	})
	s.specs = savedSpecs
	if err != nil {
		s.messages = s.messages[:base]
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		s.emit(Event{Kind: EventNotice, Text: fmt.Sprintf("could not produce a final answer: %v", err)})
		return nil
	}
	for _, delta := range deltas {
		s.emit(Event{Kind: EventTextDelta, Turn: turn, Text: delta})
	}
	if reply.Content == "" {
		// An empty answer is not worth a synthetic user turn in the transcript.
		s.messages = s.messages[:base]
		return nil
	}

	// Keep the accounting honest: this was a real model request, even though
	// it does not count as a turn against the budget that just expired.
	_, _ = s.account(reply, EstimateMessages(s.messages))

	// Only prose belongs here; a stray tool call cannot be answered after the
	// budget is gone, so it is deliberately dropped from the transcript.
	s.messages = append(s.messages, llm.Message{
		Role:      "assistant",
		Content:   reply.Content,
		Reasoning: reply.Reasoning,
	})
	s.lastText = reply.Content
	s.record("user", finalAnswerPrompt)
	s.record("assistant", reply)
	return nil
}
