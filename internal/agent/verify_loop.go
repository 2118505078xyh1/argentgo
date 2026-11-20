package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// verifyReflectionHint is deliberately separate from security.chain.go's
// finalitySentence: a verification failure is a bounded, recoverable nudge,
// not an intentional policy block that must never be retried.
const verifyReflectionHint = "Reflections left: %d. Do not re-run the same check unchanged."

// VerifyLoopState is the per-run budget for post-edit verification reflections.
// The zero value is usable: Max <= 0 means DefaultMaxVerifyReflections.
type VerifyLoopState struct {
	Reflections int
	Max         int
	Root        string // workspace root; enables █ source anchoring in feedback
}

func (s VerifyLoopState) maxReflections() int {
	if s.Max > 0 {
		return s.Max
	}
	return DefaultMaxVerifyReflections
}

// CanReflect reports whether another FAIL feedback turn may be injected.
func (s VerifyLoopState) CanReflect() bool {
	return s.Reflections < s.maxReflections()
}

// Record spends one reflection, reporting whether one was available. It is a
// no-op at the cap, so a caller that records blindly cannot exceed the budget.
func (s *VerifyLoopState) Record() bool {
	if !s.CanReflect() {
		return false
	}
	s.Reflections++
	return true
}

// remaining is how many further reflections may still be spent after the one
// that is being reported. It never goes negative.
func (s VerifyLoopState) remaining() int {
	left := s.maxReflections() - s.Reflections
	if left < 0 {
		return 0
	}
	return left
}

// BuildVerifyFeedback renders one FAIL as the next user turn. It reuses the
// existing bounded, redacted VerificationFeedback and appends a short,
// verification-specific anti-retry line. The output portion is already capped
// at defaultVerifyMaxBytes, so the whole message stays bounded.
func BuildVerifyFeedback(label, output string, state VerifyLoopState) string {
	feedback := VerificationFeedbackAt(label, false, output, defaultVerifyMaxBytes, state.Root)
	return feedback + "\n" + fmt.Sprintf(verifyReflectionHint, state.remaining())
}

// IsMutatingTool reports whether a tool name is in the default mutating set.
// It mirrors the default branch of runState.mutating; a run with custom
// Mutating/MutatingTools options uses that Options-aware method instead.
func IsMutatingTool(name string) bool {
	for _, candidate := range defaultMutatingTools {
		if candidate == name {
			return true
		}
	}
	return false
}

// batchHasMutation reports whether any call in the batch would mutate under
// this run's policy, which may override the default mutating set.
func (s *runState) batchHasMutation(calls []llm.ToolCall) bool {
	for _, call := range calls {
		if s.mutating(call.Function.Name) {
			return true
		}
	}
	return false
}

// verifyChangedPaths reads the workspace paths from the checkpoint the batch
// already emitted. Reusing that event keeps the MutationPaths hook single-call
// and makes verification see exactly the paths the session snapshotted.
func (s *runState) verifyChangedPaths(turn int) []string {
	events := s.eventsSnapshot()
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Kind == EventCheckpointReady && e.Turn == turn && e.Checkpoint != nil {
			return append([]string(nil), e.Checkpoint.Paths...)
		}
	}
	return nil
}

// verifyAfterBatch runs the injected post-edit check after a mutating batch.
//
// A FAIL becomes the next user turn via injectSteering while reflections remain;
// once the cap is spent, a further FAIL ends the run with StopVerifyReflections.
// A PASS or an unrunnable check (ok == false) leaves the count untouched. PASS
// deliberately resets nothing: the reflection budget is per run.
//
// It returns (stopReason, true) only when the run must end.
func (s *runState) verifyAfterBatch(ctx context.Context, turn int, calls []llm.ToolCall) (string, bool) {
	if s.opts.Verify == nil || !s.batchHasMutation(calls) || !s.batchVerificationEligible {
		// A batch rejected wholesale (truncated arguments, a failing BeforeTool
		// hook or invalid call IDs) is answered by fillUnrun without running any
		// tool. Verifying it would spend a reflection on a FAIL and could even
		// end the run with StopVerifyReflections despite no edit having happened.
		return "", false
	}
	label, passed, output, ok := s.opts.Verify(ctx, s.verifyChangedPaths(turn))
	if !ok {
		s.emit(Event{Kind: EventNotice, Turn: turn, Text: "verify: check could not run; skipping reflection"})
		return "", false
	}
	name := strings.ToUpper(strings.TrimSpace(label))
	if name == "" {
		name = "VERIFY"
	}
	verdict := "PASS"
	if !passed {
		verdict = "FAIL"
	}
	s.emit(Event{Kind: EventNotice, Turn: turn, Text: "[verify: " + name + "] " + verdict})
	if passed {
		return "", false
	}
	if !s.verifyState.CanReflect() {
		s.emit(Event{Kind: EventNotice, Turn: turn, Text: fmt.Sprintf(
			"verification failed after %d reflection(s)", s.verifyState.Reflections,
		)})
		return StopVerifyReflections, true
	}
	s.verifyState.Record()
	s.injectSteering(BuildVerifyFeedback(label, output, s.verifyState))
	return "", false
}
