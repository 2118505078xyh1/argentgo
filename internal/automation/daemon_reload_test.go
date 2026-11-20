package automation

import (
	"context"
	"testing"
	"time"
)

// TestDaemonDoesNotRerunSlotHandledByAnotherProcess 守护共享 store 的
// 重载：一个独立的 `automation run` 进程可以处理某时隙并推进
// NextDue，而此时该 daemon 的内存副本仍显示旧的时隙。没有刷新，daemon 会再次认领并运行该时隙。
// 没有刷新，daemon 会再次认领并运行该时隙。
func TestDaemonDoesNotRerunSlotHandledByAnotherProcess(t *testing.T) {
	dir := t.TempDir()
	daemonStore, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore (daemon): %v", err)
	}
	now := mustTime(t, "2026-01-01T12:00:00Z")
	if err := daemonStore.Put(dueSpec("nightly", now)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// 模拟另一个进程打开同一个 store 文件、运行
	// 到期任务并推进其下一个时隙。它的写入只落到磁盘；
	// 模拟另一个进程打开同一个 store 文件、运行到期任务并推进其下一个时隙。它的写入只落到磁盘；daemon 缓存的 Spec 仍停在旧的 12:00 时隙。
	other, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore (other): %v", err)
	}
	if err := other.RecordRun("nightly", StatusOK, "/tmp/nightly.log", now); err != nil {
		t.Fatalf("RecordRun (other): %v", err)
	}
	advanced, _ := other.Get("nightly")
	if !advanced.NextDue.After(now) {
		t.Fatalf("fixture did not advance NextDue past now: %s", advanced.NextDue)
	}

	exec := &fakeExecutor{}
	d, err := newDaemon(Options{Store: daemonStore, Executor: exec, Clock: newFakeClock(now).Now})
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}
	d.scan(context.Background())
	d.wait()

	if exec.count() != 0 {
		t.Fatalf("daemon re-ran a slot already handled elsewhere: %d calls", exec.count())
	}
}

// TestDaemonReloadPicksUpTaskAddedByAnotherProcess 是正向对应情形：刷新必须也让另一个进程添加到文件中的任务在紧接着的扫描中
// 刷新必须也让另一个进程添加到文件中的任务在紧接着的扫描中
// 可见，而不是抑制所有工作。
func TestDaemonReloadPicksUpTaskAddedByAnotherProcess(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	now := mustTime(t, "2026-01-01T12:00:00Z")
	exec := &fakeExecutor{}
	d, err := newDaemon(Options{Store: store, Executor: exec, Clock: newFakeClock(now).Now})
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}

	// daemon 创建后另一个进程添加了一个已经到期的任务。
	other, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore (other): %v", err)
	}
	if err := other.Put(dueSpec("added", now.Add(-time.Minute))); err != nil {
		t.Fatalf("Put (other): %v", err)
	}

	d.scan(context.Background())
	d.wait()

	if exec.count() != 1 {
		t.Fatalf("executor called %d times, want 1 for the externally added task", exec.count())
	}
	if got, _ := store.Get("added"); got.LastStatus != StatusOK {
		t.Fatalf("last status = %q, want ok", got.LastStatus)
	}
}
