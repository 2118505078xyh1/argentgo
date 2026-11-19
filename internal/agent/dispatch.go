package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

const (
	// truncatedToolReason is the error result written for every call carried by
	// a reply the provider ended for length: its JSON arguments may be cut off
	// mid-token, so executing it could act on a half-specified request.
	truncatedToolReason = "tool arguments may be truncated (finish reason: length)"

	// duplicateIDReason is the error result for a batch whose call IDs are not
	// unique and non-empty. Without a stable ID the transcript cannot pair each
	// result with its call, so none of the calls are safe to run.
	duplicateIDReason = "duplicate or empty tool call ID"

	// toolProgressKey is the context key for the gated progress emitter. The
	// literal is mirrored by toolProgressKey in internal/tools: the two packages
	// cannot share a typed key without tools importing agent, which the layering
	// rules forbid. Both agree on a plain func(string) value.
	toolProgressKey = "argent.tool.progress"
)

// Tool gating, dispatch and bookkeeping for one turn's batch.
//
// A read-only batch may run concurrently, but every decision (repeat guard,
// approval, ordering) is taken on the loop goroutine and results are recorded
// in call order, so the transcript never depends on which tool finished first.

// finishReasonReporter is the optional seam through which a Chatter exposes the
// provider's finish/stop reason for its most recent reply.
//
// LIMITATION: llm.Message carries no finish-reason field (the wire clients in
// internal/llm discard finish_reason / stop_reason while assembling the
// message), and Result.Stopped is the loop's own stop reason written after the
// run, not a per-reply provider signal. Neither can tell the loop that a reply
// was cut off for length. A client that does parse "length" (or the equivalent
// "max_tokens") must implement this interface for truncation detection to work.
// Chatters that do not implement it keep the previous behaviour: their calls
// are dispatched normally.
type finishReasonReporter interface {
	FinishReason() string
}

// isLengthReason reports whether a provider finish reason means output-length
// truncation. The list covers the spellings used by the supported protocols.
func isLengthReason(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length", "max_tokens", "max_output_tokens", "max_tokens_reached", "model_length":
		return true
	default:
		return false
	}
}

// replyTruncated reports whether the reply that just arrived ended because the
// provider ran out of output tokens. It reads the per-reply finish reason that
// the wire clients attach to llm.Message, falling back to the optional Chatter
// seam (used by fakes) when the message does not carry one.
func (s *runState) replyTruncated(reply llm.Message) bool {
	if isLengthReason(reply.FinishReason) {
		return true
	}
	reporter, ok := s.opts.Chatter.(finishReasonReporter)
	return ok && isLengthReason(reporter.FinishReason())
}

// hasInvalidCallIDs reports whether any ID in a batch is empty or repeats. A
// batch with such IDs must be answered wholesale: a duplicate ID would make two
// results indistinguishable in the transcript, and an empty one cannot be
// paired with its call at all.
func hasInvalidCallIDs(calls []llm.ToolCall) bool {
	seen := make(map[string]bool, len(calls))
	for _, call := range calls {
		if strings.TrimSpace(call.ID) == "" || seen[call.ID] {
			return true
		}
		seen[call.ID] = true
	}
	return false
}

// progressGate rejects updates once the tool call that owns them has returned.
// A goroutine a streaming tool leaves behind must not append stale transcript
// rows after the final result has been recorded.
type progressGate struct {
	accepting atomic.Bool
}

// close stops accepting updates. It must run as soon as execute returns.
func (g *progressGate) close() { g.accepting.Store(false) }

// gate wraps emit so callbacks fired after close are silent no-ops.
func (g *progressGate) gate(emit func(string)) func(string) {
	return func(text string) {
		if g.accepting.Load() && emit != nil {
			emit(text)
		}
	}
}

// withProgressGate installs a progress emitter in ctx and returns the gate that
// controls it. The caller must close the gate as soon as execute returns.
func withProgressGate(ctx context.Context, emit func(string)) (context.Context, *progressGate) {
	gate := &progressGate{}
	gate.accepting.Store(true)
	return context.WithValue(ctx, toolProgressKey, gate.gate(emit)), gate
}

// runTools executes one batch, returning a stop reason only when the run ended
// early (cancellation).
func (s *runState) runTools(ctx context.Context, turn int, calls []llm.ToolCall) string {
	// Validate before the checkpoint or any dispatch decision: one bad ID
	// invalidates the whole batch, so no call in it may be announced as runnable.
	if hasInvalidCallIDs(calls) {
		s.fillUnrun(calls, duplicateIDReason)
		return ""
	}

	// The checkpoint is intentionally announced before the batch is reordered
	// or any guard can block a call: it describes what the model asked for.
	if ctx.Err() == nil {
		if cp := s.newCheckpoint(turn, calls); cp != nil {
			s.emit(Event{Kind: EventCheckpointReady, Turn: turn, Checkpoint: cp})
			if s.opts.Checkpoint != nil {
				s.opts.Checkpoint(*cp)
			}
		}
	}
	// Normalize each call's arguments once. The repeat guard is queried both
	// here (wouldBlock) and again when the call is dispatched (check); sharing
	// this table saves a full JSON decode/encode per query without changing
	// what the guard sees or records.
	normalized := make([]string, len(calls))
	for i, call := range calls {
		normalized[i] = NormalizeArgs(call.Function.Arguments)
	}

	parallel := len(calls) > 1 && s.maxParallel() > 1
	for i, call := range calls {
		// Mutating calls need approval and the delegate tool owns nested state;
		// neither belongs on a worker goroutine. A call the repeat guard would
		// refuse must also be recorded in call order, so keep the batch
		// sequential in that case.
		if s.mutating(call.Function.Name) ||
			call.Function.Name == TaskToolName ||
			s.guard.wouldBlockNormalized(call.Function.Name, normalized[i]) {
			parallel = false
			break
		}
	}
	if parallel {
		return s.runParallelCalls(ctx, turn, calls, normalized)
	}

	for i, call := range calls {
		if ctx.Err() != nil {
			for j := i; j < len(calls); j++ {
				pending := calls[j]
				s.emitToolStart(turn, pending)
				s.finishCall(turn, pending, callResult{output: cancelledCallReason, isError: true})
				s.appendTool(pending, cancelledCallReason)
			}
			return StopCancelled
		}
		s.emitToolStart(turn, call)
		if hit, blocked := s.guard.checkNormalized(call.Function.Name, normalized[i]); hit {
			s.finishCall(turn, call, callResult{output: blocked, isError: true})
			s.appendTool(call, blocked)
			continue
		}
		res := s.invoke(ctx, call)
		s.finishCall(turn, call, res)
		s.appendTool(call, res.output)
	}
	return ""
}

// runParallelCalls runs a read-only batch concurrently while keeping the
// transcript in call order. Every call is announced before any worker starts,
// and results are recorded only after the chunk finishes, so the event stream
// and the history never depend on which tool returned first.
func (s *runState) runParallelCalls(ctx context.Context, turn int, calls []llm.ToolCall, normalized []string) string {
	// The repeat guard is a decision, so it runs in call order before workers
	// spawn; only calls that survive it reach the goroutines.
	blocked := make([]string, len(calls))
	for i, call := range calls {
		if hit, message := s.guard.checkNormalized(call.Function.Name, normalized[i]); hit {
			blocked[i] = message
		}
	}
	for _, call := range calls {
		s.emitToolStart(turn, call)
	}
	results := make([]callResult, len(calls))
	size := s.maxParallel()
	for start := 0; start < len(calls); start += size {
		end := min(start+size, len(calls))
		var wg sync.WaitGroup
		for i := start; i < end; i++ {
			if blocked[i] != "" {
				continue
			}
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				results[i] = s.invoke(ctx, calls[i])
			}(i)
		}
		wg.Wait()
	}
	for i, call := range calls {
		res := results[i]
		if blocked[i] != "" {
			res = callResult{output: blocked[i], isError: true}
		}
		s.finishCall(turn, call, res)
		s.appendTool(call, res.output)
	}
	if ctx.Err() != nil {
		return StopCancelled
	}
	return ""
}

// callResult is what one tool invocation produced.
type callResult struct {
	output   string
	isError  bool
	duration time.Duration
}

// invoke gates and runs a single call. A tool error becomes an error result for
// the model; it never aborts the run.
func (s *runState) invoke(ctx context.Context, call llm.ToolCall) callResult {
	start := time.Now()
	// A guarded toolset already runs the security chain, which asks the same
	// approver. Asking again in the loop would show a second modal for one call.
	if guarded, ok := s.opts.Tools.(interface{ Guarded() bool }); !ok || !guarded.Guarded() {
		if s.needsApproval(call.Function.Name) {
			allowed, err := s.opts.Approve.Ask(ctx, call.Function.Name, call.Function.Arguments)
			switch {
			case err != nil:
				return callResult{
					output:   fmt.Sprintf("error: approval for %q failed: %v", call.Function.Name, err),
					isError:  true,
					duration: time.Since(start),
				}
			case !allowed:
				return callResult{
					output:   fmt.Sprintf("error: the operator denied %q; do not repeat this call without changing the request", call.Function.Name),
					isError:  true,
					duration: time.Since(start),
				}
			}
		}
	}
	return s.runTool(ctx, call, start)
}

func (s *runState) runTool(ctx context.Context, call llm.ToolCall, start time.Time) callResult {
	// A streaming tool may publish intermediate rows through the emitter in ctx.
	// The gate is open only until execute returns: any callback a leftover
	// goroutine fires afterwards is dropped instead of entering the transcript.
	turn := s.turns - 1
	if turn < 0 {
		turn = 0
	}
	ctx, gate := withProgressGate(ctx, func(text string) {
		s.emit(Event{Kind: EventNotice, Turn: turn, Text: text})
		s.record("tool_progress", map[string]string{
			"id": call.ID, "name": call.Function.Name, "text": text,
		})
	})
	defer gate.close()

	output, meta, err := s.callTool(ctx, call.Function.Name, call.Function.Arguments)
	s.setToolMeta(call.ID, meta)
	if err != nil {
		return callResult{output: "error: " + err.Error(), isError: true, duration: time.Since(start)}
	}
	return callResult{output: output, duration: time.Since(start)}
}

func (s *runState) emitToolStart(turn int, call llm.ToolCall) {
	s.emit(Event{
		Kind:       EventToolStart,
		Turn:       turn,
		ToolCallID: call.ID,
		ToolName:   call.Function.Name,
		Args:       call.Function.Arguments,
	})
}

func (s *runState) finishCall(turn int, call llm.ToolCall, res callResult) {
	s.emit(Event{
		Kind:       EventToolEnd,
		Turn:       turn,
		ToolCallID: call.ID,
		ToolName:   call.Function.Name,
		Args:       call.Function.Arguments,
		Output:     res.output,
		IsError:    res.isError,
		DurationMS: res.duration.Milliseconds(),
	})
	s.record("tool", map[string]string{
		"id":        call.ID,
		"name":      call.Function.Name,
		"arguments": call.Function.Arguments,
		"output":    res.output,
		"is_error":  fmt.Sprint(res.isError),
	})
}

func (s *runState) appendTool(call llm.ToolCall, output string) {
	s.messages = append(s.messages, llm.Message{
		Role:       "tool",
		ToolCallID: call.ID,
		Content:    output,
	})
}

// fillUnrun writes an error result for every call that will never run, so the
// transcript keeps every tool call paired with a result.
func (s *runState) fillUnrun(calls []llm.ToolCall, reason string) {
	turn := s.turns - 1
	if turn < 0 {
		turn = 0
	}
	for _, call := range calls {
		s.emitToolStart(turn, call)
		s.finishCall(turn, call, callResult{output: reason, isError: true})
		s.appendTool(call, reason)
	}
}
