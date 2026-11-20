package proc

import (
	"os"
	"testing"
	"time"
)

func TestWithinStartTimeToleranceBoundary(t *testing.T) {
	tests := []struct {
		name      string
		observed  int64
		startedAt int64
		tolerance int64
		want      bool
	}{
		{name: "exact match with zero tolerance", observed: 1000, startedAt: 1000, tolerance: 0, want: true},
		{name: "difference at tolerance is same process", observed: 1500, startedAt: 1000, tolerance: 500, want: true},
		{name: "difference one past tolerance is reused pid", observed: 1501, startedAt: 1000, tolerance: 500, want: false},
		{name: "negative skew is symmetric", observed: 500, startedAt: 1000, tolerance: 500, want: true},
		{name: "negative skew one past tolerance", observed: 499, startedAt: 1000, tolerance: 500, want: false},
		{name: "large skew is rejected", observed: 1000 + 86_400_000, startedAt: 1000, tolerance: 1000, want: false},
		{name: "negative tolerance clamps to zero", observed: 1001, startedAt: 1000, tolerance: -1, want: false},
		{name: "negative tolerance still accepts equality", observed: 1000, startedAt: 1000, tolerance: -1, want: true},
		{name: "extreme endpoints do not overflow", observed: -1 << 63, startedAt: 1<<63 - 1, tolerance: 1000, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := withinStartTimeTolerance(tt.observed, tt.startedAt, tt.tolerance); got != tt.want {
				t.Fatalf("withinStartTimeTolerance(%d, %d, %d) = %v, want %v",
					tt.observed, tt.startedAt, tt.tolerance, got, tt.want)
			}
		})
	}
}

// liveStartTime probes the current process, skipping on platforms with no
// /proc (or a restricted one) where the identity probe is best-effort.
func liveStartTime(t *testing.T) (int, int64) {
	t.Helper()
	pid := os.Getpid()
	startedAt, err := ProcessStartTime(pid)
	if err != nil {
		t.Skipf("process start time unavailable on this host: %v", err)
	}
	return pid, startedAt
}

func TestProcessStartTimeIsEpochMilliseconds(t *testing.T) {
	_, startedAt := liveStartTime(t)
	// A raw /proc tick counter (~1e9) or a raw FILETIME (~1.3e17) would fail
	// this: callers compare against a millisecond tolerance.
	if startedAt <= 1_000_000_000_000 {
		t.Fatalf("start time %d is not milliseconds since the Unix epoch", startedAt)
	}
	if startedAt > time.Now().UnixMilli()+1000 {
		t.Fatalf("start time %d is in the future", startedAt)
	}
}

func TestIsSameProcessAcceptsLiveIncarnationAndRejectsReuse(t *testing.T) {
	pid, startedAt := liveStartTime(t)

	if !IsSameProcess(pid, startedAt, DefaultStartTimeToleranceMS) {
		t.Fatal("a live pid with its recorded start time must be the same process")
	}
	// A different process that inherited the same PID started much later.
	if IsSameProcess(pid, startedAt-86_400_000, DefaultStartTimeToleranceMS) {
		t.Fatal("a mismatched start time must be rejected as PID reuse")
	}
	// No recorded identity: liveness is the only thing we can assert.
	if !IsSameProcess(pid, 0, DefaultStartTimeToleranceMS) {
		t.Fatal("an unknown start time must still accept a live pid")
	}
}

func TestIsSameProcessRejectsDeadPIDEvenWithoutRecordedStart(t *testing.T) {
	// PIDs are at most 22 bits on Linux and 32 bits on Windows; this value is
	// well outside either range so no live process can occupy it.
	const deadPID = 2_147_483_647
	if IsSameProcess(deadPID, 0, DefaultStartTimeToleranceMS) {
		t.Fatal("a dead pid must be rejected even when the recorded start is unknown")
	}
}

func TestProcessStartTimeRejectsInvalidPID(t *testing.T) {
	for _, pid := range []int{0, -1} {
		if _, err := ProcessStartTime(pid); err == nil {
			t.Fatalf("ProcessStartTime(%d) succeeded, want an error", pid)
		}
	}
}
