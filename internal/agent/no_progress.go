package agent

import (
	"fmt"
	"hash/fnv"
	"strconv"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// No-progress detection is a pure, state-free complement to the event-based
// StuckDetector and the dispatch repeat guard. It answers one narrow question:
// have the most recent tool calls been the same call returning the same result,
// i.e. is the model spinning without learning anything new?
//
// The decision here is independent of timers, I/O and the loop, so it can be
// unit-tested on its own and wired in behind an opt-in flag without changing an
// unconfigured run.

// DefaultNoProgressThreshold is the tail length checked by DetectNoProgress.
// Three matches stay in step with the event detector's repeat threshold and
// remain conservative: two identical calls are a normal read-verify pattern.
const DefaultNoProgressThreshold = 3

// ToolCallSignature is the stable identity of one tool call plus a compact
// summary of the result it produced. Volatile fields (call id, turn, duration,
// usage) are deliberately absent: two calls differing only in those fields are
// the same call for loop detection.
//
// Args must be a normalized argument summary; callers can use NormalizeArgs, or
// SignatureFromToolCall / SignaturesFromEvents which apply it for them. Result
// is a hash of the output; an empty Result means "no result recorded", which
// still counts as a stable (non-changing) value when every call in the window
// shares it. Two calls with different results are progress, never a loop.
type ToolCallSignature struct {
	Name   string
	Args   string
	Result string
}

// NoProgressConfig enables the opt-in early-stop form of no-progress detection.
// When Options.NoProgress is nil the loop never calls the detector, so existing
// runs are unchanged.
type NoProgressConfig struct {
	// Threshold is how many identical trailing calls trigger a stop. Zero uses
	// DefaultNoProgressThreshold.
	Threshold int
}

// DetectNoProgress reports whether the trailing DefaultNoProgressThreshold
// calls are all the same call with the same result. It is the pure, no-config
// form; callers that need a different window use DetectNoProgressWithThreshold.
func DetectNoProgress(calls []ToolCallSignature) (stuck bool, reason string) {
	return DetectNoProgressWithThreshold(calls, DefaultNoProgressThreshold)
}

// DetectNoProgressWithThreshold is the configurable-threshold form. threshold
// is the number of trailing calls that must all match; values below 1 fall back
// to DefaultNoProgressThreshold so a misconfigured caller cannot flag a run on
// an empty or single-call history.
func DetectNoProgressWithThreshold(calls []ToolCallSignature, threshold int) (stuck bool, reason string) {
	if threshold < 1 {
		threshold = DefaultNoProgressThreshold
	}
	if len(calls) < threshold {
		return false, ""
	}
	window := calls[len(calls)-threshold:]
	first := window[0]
	// An empty tool name is malformed input, not evidence of a loop.
	if first.Name == "" {
		return false, ""
	}
	for _, c := range window[1:] {
		if c.Name != first.Name || c.Args != first.Args || c.Result != first.Result {
			return false, ""
		}
	}
	return true, fmt.Sprintf(
		"no progress: %q was called %d times in a row with identical arguments and no new result",
		first.Name, threshold)
}

// SignatureFromToolCall projects one dispatched call before its result exists.
// Args are normalized so JSON key order and whitespace cannot hide a repeat.
func SignatureFromToolCall(call llm.ToolCall) ToolCallSignature {
	return ToolCallSignature{Name: call.Function.Name, Args: NormalizeArgs(call.Function.Arguments)}
}

// ToolCallSignatures pairs dispatched calls with their results by index. A
// shorter results slice leaves the trailing calls without a result summary; a
// nil results slice yields result-free signatures suitable for a pre-execution
// check.
func ToolCallSignatures(calls []llm.ToolCall, results []callResult) []ToolCallSignature {
	out := make([]ToolCallSignature, 0, len(calls))
	for i, call := range calls {
		sig := SignatureFromToolCall(call)
		if i < len(results) {
			sig.Result = summarizeResult(results[i].output)
		}
		out = append(out, sig)
	}
	return out
}

// SignaturesFromEvents builds signatures from a run's recorded tool_end events.
// Each tool_end carries both Args and Output, so no pairing with tool_start is
// needed and unmatched starts are naturally ignored.
func SignaturesFromEvents(events []Event) []ToolCallSignature {
	out := make([]ToolCallSignature, 0, len(events))
	for _, e := range events {
		if e.Kind != EventToolEnd {
			continue
		}
		out = append(out, ToolCallSignature{
			Name:   e.ToolName,
			Args:   NormalizeArgs(e.Args),
			Result: summarizeResult(e.Output),
		})
	}
	return out
}

// summarizeResult hashes an output into a compact, stable token. Hashing keeps
// the signature small even for large tool results, mirroring the dedupe guard's
// output window.
func summarizeResult(output string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(output))
	return strconv.FormatUint(h.Sum64(), 16)
}
