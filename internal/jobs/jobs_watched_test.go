package jobs

import (
	"errors"
	"testing"
	"time"
)

// TestStopAndRemoveLeaveWatchedExitCodeToWatcher 确定了一处
// 责任划分。对被观察的作业（带有
// 进程内 reaper/watchLive 的作业），真实退出码属于该观察者：
// 死的或被回收的 pid 绝不能被 Stop 或 Remove 变成。身份守卫只拒绝给无法识别的
// pid 发信号；它必须让作业保持运行给观察者。对未被观察的作业
// （从磁盘重载，无观察者），没有别人能恢复代码，因此
// 保留现有的退役为 -1 行为。
func TestStopAndRemoveLeaveWatchedExitCodeToWatcher(t *testing.T) {
	const deadPID = 0x7FFFFFF0 // almost certainly dead on both platforms
	const watcherCode = 7

	tests := []struct {
		name       string
		watched    bool
		op         string // "stop" or "remove"
		wantStatus Status
	}{
		{"stop-watched", true, "stop", StatusRunning},
		{"stop-unwatched", false, "stop", StatusExited},
		{"remove-watched", true, "remove", StatusRunning},
		{"remove-unwatched", false, "remove", StatusExited},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRegistry(t)
			job := &Job{
				ID:        "job1",
				Command:   "identity-test",
				StartedAt: time.Now().UTC(),
				Status:    StatusRunning,
				ExitCode:  -1,
				LogPath:   r.Dir() + "/job1.log",
				PID:       deadPID,
				watched:   tt.watched,
			}
			r.mu.Lock()
			r.jobs[job.ID] = job
			r.mu.Unlock()

			var err error
			switch tt.op {
			case "stop":
				err = r.Stop(job.ID)
			case "remove":
				err = r.Remove(job.ID)
			default:
				t.Fatalf("unknown op %q", tt.op)
			}
			if !errors.Is(err, errProcessIdentity) {
				t.Fatalf("%s err = %v, want a process-identity refusal", tt.op, err)
			}

			got, getErr := r.Get(job.ID)
			if getErr != nil {
				t.Fatalf("get: %v", getErr)
			}
			if got.Status != tt.wantStatus || got.ExitCode != -1 {
				t.Fatalf("after %s: status=%q exit=%d, want %q/-1",
					tt.op, got.Status, got.ExitCode, tt.wantStatus)
			}

			if !tt.watched {
				return
			}
			// 观察者仍拥有终态快照。一旦它记录了
			// 观察者仍拥有终态快照。一旦它记录了真实代码，Get 必须报告该代码 —— 绝不是这里过早退役会冻结的 -1。
			// 这里过早退役会冻结的 -1。
			r.mu.Lock()
			stored := r.jobs[job.ID]
			stored.Status = StatusExited
			stored.ExitCode = watcherCode
			snap, version := r.changedLocked()
			r.mu.Unlock()
			_ = r.persist(snap, version)

			got, getErr = r.Get(job.ID)
			if getErr != nil {
				t.Fatalf("get after watcher: %v", getErr)
			}
			if got.Status != StatusExited || got.ExitCode != watcherCode {
				t.Fatalf("after watcher: status=%q exit=%d, want exited/%d",
					got.Status, got.ExitCode, watcherCode)
			}
		})
	}
}
