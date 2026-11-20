package jobs

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// startHelperChild 在 registry 之外把 sleep-helper 测试二进制作为真实子进程启动。它代表崩溃后可能继承某个陈旧作业 pid 的无辜进程，使测试能证明它从不被发信号。
// registry 之外。它代表崩溃后可能继承陈旧作业 pid 的
// 无辜进程，使测试能证明它从不被发信号。
func startHelperChild(t *testing.T) (*exec.Cmd, time.Time) {
	t.Helper()
	spec := longCommand()
	cmd := exec.Command(spec.Program, spec.Args...)
	cmd.Env = append(os.Environ(), spec.Env...)
	detach(cmd)
	startedAt := time.Now().UTC()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper child: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	// 在后台回收：Unix 上僵尸的 kill(pid, 0) 仍成功，因此未回收的子进程在 Stop 后仍显得存活。
	// Unix 上僵尸的 kill(pid, 0) 仍成功，因此未回收的子进程在 Stop 后仍显得存活。
	go func() { _ = cmd.Wait() }()
	return cmd, startedAt
}

func writeJobIndex(t *testing.T, dir string, jobs ...Job) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create registry dir: %v", err)
	}
	data, err := json.Marshal(index{Next: len(jobs) + 1, Jobs: jobs})
	if err != nil {
		t.Fatalf("marshal index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, storeFile), data, 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
}

func TestSameProcessRequiresMatchingCreationTime(t *testing.T) {
	cmd, startedAt := startHelperChild(t)
	pid := cmd.Process.Pid

	if !sameProcess(pid, startedAt) {
		t.Fatalf("sameProcess(%d, real start) = false, want true", pid)
	}
	if sameProcess(pid, startedAt.Add(-time.Hour)) {
		t.Fatal("an earlier start time must not identify the same pid")
	}
	if sameProcess(pid, startedAt.Add(time.Hour)) {
		t.Fatal("a future start time must not identify the same pid")
	}
	if sameProcess(pid, time.Time{}) {
		t.Fatal("a zero start time must fail closed")
	}
	if sameProcess(0x7FFFFFF0, startedAt) {
		t.Fatal("a dead pid must not be identified")
	}
	if sameProcess(0, startedAt) {
		t.Fatal("pid 0 must fail closed")
	}
}

// TestLoadRetiresAJobWhosePidWasRecycled 检查刚重新打开索引后的状态，
// TestLoadRetiresAJobWhosePidWasRecycled 检查刚重新打开索引后的状态，在任何 Get/List 刷新可以运行之前：加载本身不得把被回收的 pid 留作正在运行。
// 被回收的 pid 留作正在运行。
func TestLoadRetiresAJobWhosePidWasRecycled(t *testing.T) {
	cmd, realStart := startHelperChild(t)
	dir := t.TempDir()
	writeJobIndex(t, dir, Job{
		ID:        "job1",
		Command:   "unrelated",
		StartedAt: realStart.Add(-time.Hour),
		Status:    StatusRunning,
		ExitCode:  -1,
		PID:       cmd.Process.Pid,
		LogPath:   filepath.Join(dir, "job1.log"),
	})

	r, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	r.mu.Lock()
	loaded := *r.jobs["job1"]
	r.mu.Unlock()
	if loaded.Status != StatusExited || loaded.ExitCode != -1 {
		t.Fatalf("loaded job = %+v, want exited/-1", loaded)
	}
	got, err := r.Get("job1")
	if err != nil || got.Status != StatusExited {
		t.Fatalf("get = %+v, err = %v, want exited", got, err)
	}
	if !alive(cmd.Process.Pid) {
		t.Fatal("loading a registry must not signal the process")
	}
}

// TestRefreshRetiresAJobWhosePidWasRecycled 直接注入陈旧作业，因此
// TestRefreshRetiresAJobWhosePidWasRecycled 直接注入陈旧作业，因此被测代码是 refreshLocked（而非 load）。陈旧作业被保留但退役，使重试杀不到任何东西。
func TestRefreshRetiresAJobWhosePidWasRecycled(t *testing.T) {
	cmd, realStart := startHelperChild(t)
	r := newRegistry(t)
	r.mu.Lock()
	r.jobs["job1"] = &Job{
		ID:        "job1",
		Command:   "unrelated",
		StartedAt: realStart.Add(-time.Hour),
		Status:    StatusRunning,
		ExitCode:  -1,
		PID:       cmd.Process.Pid,
		LogPath:   filepath.Join(r.Dir(), "job1.log"),
	}
	r.mu.Unlock()

	got, err := r.Get("job1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != StatusExited || got.ExitCode != -1 {
		t.Fatalf("refreshed job = %+v, want exited/-1", got)
	}
	listed, err := r.List()
	if err != nil || len(listed) != 1 || listed[0].Status != StatusExited {
		t.Fatalf("list = %+v, err = %v, want one exited job", listed, err)
	}
	if !alive(cmd.Process.Pid) {
		t.Fatal("a refresh must not signal the process")
	}
}

func TestStopRefusesToKillARecycledPid(t *testing.T) {
	cmd, realStart := startHelperChild(t)
	r := newRegistry(t)
	r.mu.Lock()
	r.jobs["job1"] = &Job{
		ID:        "job1",
		Command:   "unrelated",
		StartedAt: realStart.Add(-time.Hour),
		Status:    StatusRunning,
		ExitCode:  -1,
		PID:       cmd.Process.Pid,
		LogPath:   filepath.Join(r.Dir(), "job1.log"),
	}
	r.mu.Unlock()

	err := r.Stop("job1")
	if !errors.Is(err, errProcessIdentity) {
		t.Fatalf("stop err = %v, want a process-identity refusal", err)
	}
	if !alive(cmd.Process.Pid) {
		t.Fatal("Stop must not kill a process with a recycled pid")
	}
	got, err := r.Get("job1")
	if err != nil || got.Status != StatusExited || got.ExitCode != -1 {
		t.Fatalf("job after refusal = %+v, err = %v, want exited/-1", got, err)
	}
}

func TestRemoveRefusesToKillARecycledPid(t *testing.T) {
	cmd, realStart := startHelperChild(t)
	r := newRegistry(t)
	r.mu.Lock()
	r.jobs["job1"] = &Job{
		ID:        "job1",
		Command:   "unrelated",
		StartedAt: realStart.Add(-time.Hour),
		Status:    StatusRunning,
		ExitCode:  -1,
		PID:       cmd.Process.Pid,
		LogPath:   filepath.Join(r.Dir(), "job1.log"),
	}
	r.mu.Unlock()

	err := r.Remove("job1")
	if !errors.Is(err, errProcessIdentity) {
		t.Fatalf("remove err = %v, want a process-identity refusal", err)
	}
	if !alive(cmd.Process.Pid) {
		t.Fatal("Remove must not kill a process with a recycled pid")
	}
	// 陈旧作业被保留但退役，使重试杀不到任何东西。
	got, err := r.Get("job1")
	if err != nil || got.Status != StatusExited {
		t.Fatalf("job after refusal = %+v, err = %v, want a kept exited job", got, err)
	}
	if err := r.Remove("job1"); err != nil {
		t.Fatalf("retry remove: %v", err)
	}
	if !alive(cmd.Process.Pid) {
		t.Fatal("the retry must not kill the process either")
	}
}

// TestStopKillsAReloadedJobWithMatchingIdentity 证明该检查不会
// TestStopKillsAReloadedJobWithMatchingIdentity 证明该检查不会禁用合法停止：这里只使用重载作业的原始 pid 路径，且其记录的启动时间与真实子进程匹配。
// 这里，且其记录的启动时间与真实子进程匹配。
func TestStopKillsAReloadedJobWithMatchingIdentity(t *testing.T) {
	cmd, startedAt := startHelperChild(t)
	dir := t.TempDir()
	writeJobIndex(t, dir, Job{
		ID:        "job1",
		Command:   "helper",
		StartedAt: startedAt,
		Status:    StatusRunning,
		ExitCode:  -1,
		PID:       cmd.Process.Pid,
		LogPath:   filepath.Join(dir, "job1.log"),
	})

	r, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := r.Get("job1")
	if err != nil || got.Status != StatusRunning {
		t.Fatalf("matching job = %+v, err = %v, want running", got, err)
	}
	if err := r.Stop("job1"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	stopped, err := r.Get("job1")
	if err != nil || stopped.Status != StatusKilled {
		t.Fatalf("stopped job = %+v, err = %v, want killed", stopped, err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for alive(cmd.Process.Pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(cmd.Process.Pid) {
		t.Fatalf("pid %d is still running after Stop", cmd.Process.Pid)
	}
}
