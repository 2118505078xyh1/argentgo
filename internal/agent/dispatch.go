package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/2118505078xyh1/argent-go/internal/llm"
)

// Tool gating, dispatch and bookkeeping for one turn's batch.
//
// A read-only batch may run concurrently, but every decision (repeat guard,
// approval, ordering) is taken on the loop goroutine and results are recorded
// in call order, so the transcript never depends on which tool finished first.

// runTools executes one batch, returning a stop reason only when the run ended
// early (cancellation).
func (s *runState) runTools(ctx context.Context, turn int, calls []llm.ToolCall) string {
	parallel := len(calls) > 1 && s.maxParallel() > 1
	for _, call := range calls {
		// Mutating calls need approval and the delegate tool owns nested state;
		// neither belongs on a worker goroutine. A call the repeat guard would
		// refuse must also be recorded in call order, so keep the batch
		// sequential in that case.
		if s.mutating(call.Function.Name) ||
			call.Function.Name == TaskToolName ||
			s.guard.wouldBlock(call.Function.Name, call.Function.Arguments) {
			parallel = false
			break
		}
	}
	if parallel {
		return s.runParallelCalls(ctx, turn, calls)
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
		if hit, blocked := s.guard.check(call.Function.Name, call.Function.Arguments); hit {
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
func (s *runState) runParallelCalls(ctx context.Context, turn int, calls []llm.ToolCall) string {
	// The repeat guard is a decision, so it runs in call order before workers
	// spawn; only calls that survive it reach the goroutines.
	blocked := make([]string, len(calls))
	for i, call := range calls {
		if hit, message := s.guard.check(call.Function.Name, call.Function.Arguments); hit {
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
	output, err := s.tools.Run(ctx, call.Function.Name, call.Function.Arguments)
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
