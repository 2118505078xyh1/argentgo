package proc

// DefaultStartTimeToleranceMS is the allowed skew between a recorded and an
// observed process start time. It matches the tolerance used by argent's
// daemon: large enough to absorb boot-time/clock-read skew, small enough that a
// PID reused seconds later is still detected as a different incarnation.
const DefaultStartTimeToleranceMS int64 = 1000

// ProcessStartTime returns the start time of pid as milliseconds since the
// Unix epoch, or an error when the process is gone, cannot be queried, or the
// platform has no probe. The value is an identity token: two incarnations of
// the same PID carry different values, which is what lets callers detect PID
// reuse instead of trusting the bare number.
func ProcessStartTime(pid int) (int64, error) {
	return processStartTime(pid)
}

// IsSameProcess reports whether pid is alive and is still the incarnation that
// started at startedAt. A startedAt <= 0 means "unknown": the liveness probe
// still runs, but identity cannot be disproved, so a live pid is accepted.
func IsSameProcess(pid int, startedAt int64, toleranceMS int64) bool {
	observed, err := ProcessStartTime(pid)
	if err != nil {
		// A missing/unreadable process is never the one we recorded.
		return false
	}
	if startedAt <= 0 {
		return true
	}
	return withinStartTimeTolerance(observed, startedAt, toleranceMS)
}

// withinStartTimeTolerance is the pure comparison core. Keeping it separate
// from the OS probe lets the boundary behaviour be tested without depending on
// a live process or on the host clock.
func withinStartTimeTolerance(observed, startedAt, toleranceMS int64) bool {
	if toleranceMS < 0 {
		toleranceMS = 0
	}
	// Take the magnitude in the unsigned domain after ordering the operands:
	// this avoids signed overflow at the int64 endpoints without treating the
	// value range as a ring (a plain uint64 subtraction would report 2^63 apart
	// values as 1).
	var diff uint64
	if observed >= startedAt {
		diff = uint64(observed) - uint64(startedAt)
	} else {
		diff = uint64(startedAt) - uint64(observed)
	}
	return diff <= uint64(toleranceMS)
}
