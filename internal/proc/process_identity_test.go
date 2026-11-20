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

// liveStartTime 探测当前进程；在没有 /proc 的平台
// （或受限制的 /proc，身份探测仅为尽力而为）上跳过。
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
	// 原始的 /proc tick 计数（约 1e9）或原始 FILETIME（约 1.3e17）会
	// 通不过此检查：调用方是按毫秒容差比较的。
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
	// 继承同一 PID 的另一个进程要晚得多才启动。
	if IsSameProcess(pid, startedAt-86_400_000, DefaultStartTimeToleranceMS) {
		t.Fatal("a mismatched start time must be rejected as PID reuse")
	}
	// 没有记录的身份：我们唯一能断言的是存活状态。
	if !IsSameProcess(pid, 0, DefaultStartTimeToleranceMS) {
		t.Fatal("an unknown start time must still accept a live pid")
	}
}

func TestIsSameProcessRejectsDeadPIDEvenWithoutRecordedStart(t *testing.T) {
	// Linux 上 PID 最多 22 位，Windows 上 32 位；该值
	// 远超任一范围，因此不可能有活进程占用它。
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
