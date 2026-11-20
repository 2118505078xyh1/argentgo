package eval

import (
	"strings"
	"time"
	"unicode"
)

// Summary is the order-independent aggregate of a set of graded Results.
// It is a value produced by AggregateScores; callers should treat it as
// read-only (in particular the FailureCounts map is owned by the Summary).
type Summary struct {
	// Total is the number of results handed to AggregateScores.
	Total int `json:"total"`
	// Valid is the number of structurally sane results that were aggregated.
	Valid int `json:"valid"`
	// Skipped is the number of invalid results ignored (Total - Valid).
	Skipped int `json:"skipped"`
	// Passed and Failed are counted over valid results only.
	Passed int `json:"passed"`
	Failed int `json:"failed"`
	// PassRate is Passed*100/Valid, in [0,100]. It is 0 when Valid == 0,
	// never NaN or +Inf.
	PassRate float64 `json:"pass_rate"`
	// AverageDuration is the mean Duration over valid results. It is 0 when
	// Valid == 0. A valid result without timing data (Duration == 0) counts
	// as zero, so the mean is taken over the same set as the pass rate.
	AverageDuration time.Duration `json:"average_duration"`
	// FailureCounts groups failed valid results by their normalized reason.
	// Keys are rune-safe (CJK and emoji are preserved), whitespace-stripped
	// reasons truncated to failureReasonKeyRunes runes; the map is never nil.
	FailureCounts map[string]int `json:"failure_counts"`
}

// failureReasonKeyRunes bounds the length of a FailureCounts key without
// ever cutting a UTF-8 rune in half (see clip for the UTF-8 rationale).
const failureReasonKeyRunes = 80

// valid reports whether a Result is structurally sane enough to aggregate.
// Results from Run carry a 1-based Index, a non-negative turn count and a
// non-negative duration; anything else is a malformed record and is ignored.
func (r Result) valid() bool {
	return r.Index > 0 && r.Turns >= 0 && r.Duration >= 0
}

// AggregateScores folds results into a Summary. It is pure: it never mutates
// results, and it depends only on the multiset of valid results, not on their
// order. Results gathered concurrently may therefore be passed in any order.
func AggregateScores(results []Result) Summary {
	s := Summary{
		Total:         len(results),
		FailureCounts: make(map[string]int),
	}
	var durationSum time.Duration
	for _, r := range results {
		if !r.valid() {
			s.Skipped++
			continue
		}
		s.Valid++
		if r.Passed {
			s.Passed++
		} else {
			s.Failed++
			s.FailureCounts[failureReasonKey(r.Reason)]++
		}
		durationSum += r.Duration
	}
	s.PassRate = passRate(s.Passed, s.Valid)
	if s.Valid > 0 {
		s.AverageDuration = durationSum / time.Duration(s.Valid)
	}
	return s
}

// failureReasonKey normalizes a failure reason into a stable, rune-safe map
// key. All Unicode whitespace is removed first so that reasons differing only
// in spacing (common with CJK text) fall into the same bucket; clip then
// truncates by runes, so CJK and emoji are never split mid-rune and the key
// stays valid UTF-8.
func failureReasonKey(reason string) string {
	stripped := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, reason)
	return clip(stripped, failureReasonKeyRunes)
}
