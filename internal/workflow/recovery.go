package workflow

// StepRecord is one entry in a persisted step journal. Index is the step's
// position in the workflow definition; Name and Status mirror the StepResult
// that was recorded. Records may arrive in any order (for example when a run
// is replayed from a log), so Index — not slice position — identifies a step.
// Only StatusOK counts as completed; failed and skipped steps must be retried.
type StepRecord struct {
	Index  int
	Name   string
	Status string
}

// RecoveryStep decides where an idempotent replay should resume given the
// journal of already-recorded steps and the current step index.
//
// A step is considered completed when the journal holds at least one record
// for its index with StatusOK; failed and skipped records do not count. The
// function walks forward from current and returns the first index that is not
// completed as resumeFrom. skipCompleted reports whether that walk advanced
// past current (the step at current, and possibly more, was already done and
// should be skipped instead of re-run). If every step from current through the
// last recorded index is completed, resumeFrom is one past that last index,
// which signals that there is nothing left to replay.
//
// The function is pure: it never mutates journal and does not depend on slice
// order. A negative current is treated as zero. Records with a negative Index
// are ignored.
func RecoveryStep(journal []StepRecord, current int) (resumeFrom int, skipCompleted bool) {
	if current < 0 {
		current = 0
	}
	completed := make(map[int]bool, len(journal))
	for _, rec := range journal {
		if rec.Index < 0 || rec.Status != StatusOK {
			continue
		}
		completed[rec.Index] = true
	}
	resumeFrom = current
	for completed[resumeFrom] {
		resumeFrom++
	}
	return resumeFrom, resumeFrom > current
}
