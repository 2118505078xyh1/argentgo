package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// assertStoredWatched 检查 registry 自己的副本：那是 refreshLocked 遍历的那一个，因此标志必须设在那里（而非仅在返回给调用方的值上）。
// 那是 refreshLocked 遍历的那一个，因此标志必须设在那里（而非仅在
// 返回给调用方的值上）。
func assertStoredWatched(t *testing.T, r *Registry, id string) {
	t.Helper()
	r.mu.Lock()
	job, ok := r.jobs[id]
	watched := ok && job.watched
	r.mu.Unlock()
	if !watched {
		t.Fatalf("job %q must be marked watched", id)
	}
}

// TestStartAndPromoteMarkJobsWatched 在源头守护该修复：两条路径
// 派生进程内完成观察者必须标记存储的作业，使
// TestStartAndPromoteMarkJobsWatched 在源头守护该修复：两条派生进程内完成观察者的路径都必须标记存储的作业，使 refreshLocked 把其退出码留给该观察者。
func TestStartAndPromoteMarkJobsWatched(t *testing.T) {
	t.Run("start", func(t *testing.T) {
		r := newRegistry(t)
		job, err := r.Start(shellCommand("echo watched-start"))
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		assertStoredWatched(t, r, job.ID)
		waitForTerminal(t, r, job.ID)
		waitPersisted(t, r)
	})

	t.Run("promote", func(t *testing.T) {
		r := newRegistry(t)
		live := newFakeLive(0x7FFFFFF1)
		job, err := r.PromoteFromTimeout("watched-promote", live, filepath.Join(t.TempDir(), "job.log"))
		if err != nil {
			t.Fatalf("promote: %v", err)
		}
		assertStoredWatched(t, r, job.ID)
		live.finish(0)
		waitForTerminal(t, r, job.ID)
		waitPersisted(t, r)
	})
}

// TestWatchedJobNeverShowsPrematureTerminalExit 确定性地复现竞态窗口：
// 子进程已死（pid 无法恢复）但 reaper 尚未记录真实退出码。读者不得
// 在那个窗口发布终态快照 —— 只有 reaper 能使
// TestWatchedJobNeverShowsPrematureTerminalExit 确定性地复现竞态窗口：子进程已死（pid 无法恢复）但 reaper 尚未记录真实退出码。读者不得在那个窗口发布终态 Exited/-1 快照 —— 只有 reaper 能使作业进入终态，且它带有真实代码。
// 作业进入终态，且它带有真实代码。
func TestWatchedJobNeverShowsPrematureTerminalExit(t *testing.T) {
	r := newRegistry(t)
	const realCode = 7
	stored := Job{
		ID:        "job1",
		Command:   "window-test",
		StartedAt: time.Now().UTC(),
		Status:    StatusRunning,
		ExitCode:  -1,
		LogPath:   filepath.Join(r.Dir(), "job1.log"),
		PID:       0x7FFFFFF2, // almost certainly dead on both platforms
		watched:   true,
	}
	r.mu.Lock()
	r.jobs[stored.ID] = &stored
	r.mu.Unlock()

	// Get 不得把死掉的 pid 变成终态 -1 快照。List 也不得如此。一旦 reaper 记录了真实代码，终态快照才有效且带有该确切代码，绝不是 -1 哨兵。
	got, err := r.Get(stored.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != StatusRunning || got.ExitCode != -1 {
		t.Fatalf("window Get = %+v, want running/-1 (no premature terminal)", got)
	}

	// List 也不得如此。
	listed, err := r.List()
	if err != nil || len(listed) != 1 {
		t.Fatalf("list = %+v, err = %v", listed, err)
	}
	if listed[0].Status != StatusRunning || listed[0].ExitCode != -1 {
		t.Fatalf("window List = %+v, want running/-1 (no premature terminal)", listed[0])
	}

	// 一旦 reaper 记录了真实代码，终态快照才有效
	// 且带有该确切代码，绝不是 -1 哨兵。
	r.mu.Lock()
	job := r.jobs[stored.ID]
	job.Status = StatusExited
	job.ExitCode = realCode
	snap, version := r.changedLocked()
	r.mu.Unlock()
	_ = r.persist(snap, version)

	got, err = r.Get(stored.ID)
	if err != nil {
		t.Fatalf("get after reap: %v", err)
	}
	if got.Status != StatusExited || got.ExitCode != realCode {
		t.Fatalf("reaped Get = %+v, want exited/%d", got, realCode)
	}
}

// TestWatchedFlagIsNotPersisted 保持磁盘 JSON 形态不变，并验证预期结果：从磁盘重新打开的 registry 没有活观察者，因此它不得继承标记且仍必须刷新死 pid。
// 验证预期结果：从磁盘重新打开的 registry 没有活
// 观察者，因此它不得继承标记且仍必须刷新死 pid。
func TestWatchedFlagIsNotPersisted(t *testing.T) {
	r := newRegistry(t)
	job, err := r.Start(shellCommand("echo persist-check"))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForTerminal(t, r, job.ID)
	waitPersisted(t, r)

	data, err := os.ReadFile(filepath.Join(r.Dir(), storeFile))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if strings.Contains(string(data), `"watched"`) {
		t.Fatalf("index leaked the internal watched flag: %s", data)
	}

	second, err := New(r.Dir())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	second.mu.Lock()
	loaded, ok := second.jobs[job.ID]
	reloadedWatched := ok && loaded.watched
	second.mu.Unlock()
	if reloadedWatched {
		t.Fatal("a registry reopened from disk must not inherit the watched flag")
	}
}
