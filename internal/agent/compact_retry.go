package agent

import "github.com/2118505078xyh1/argentgo/internal/llm"

// MaxCompactRetries is the bounded number of compact-and-retry attempts the
// loop may spend on a provider-reported context overflow. One is enough: if the
// same oversized request still overflows after compaction, replaying it again
// only burns the turn budget without making progress.
const MaxCompactRetries = 1

// CompactRetryDecision is the loop's choice after a failed model request.
// "retry" compacts the transcript once and resends the same turn; "stop" keeps
// the pre-existing failure path unchanged.
type CompactRetryDecision string

const (
	CompactRetry CompactRetryDecision = "retry"
	CompactStop  CompactRetryDecision = "stop"
)

// DecideCompactRetry is the pure decision layer for compact-and-retry. It never
// compacts, emits or mutates state; the caller owns those side effects.
//
// Retry is granted only for a provider-reported context overflow that still has
// retry budget. maxRetries <= 0 means "no retries at all", so an unconfigured
// caller keeps a stop. Every other failure kind is final here: transport and
// rate-limit errors already have their own model-switch path in
// chatWithFallback, and replaying the same messages cannot fix an auth, config
// or validation failure.
func DecideCompactRetry(kind llm.FailureKind, retriesSoFar int, maxRetries int) CompactRetryDecision {
	if maxRetries <= 0 || kind != llm.FailureContextOverflow || retriesSoFar >= maxRetries {
		return CompactStop
	}
	return CompactRetry
}
