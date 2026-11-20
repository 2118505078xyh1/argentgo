package automation

import (
	"context"
	"testing"
	"time"
)

// TestDaemonRunDoesNotClobberConcurrentStoreEdits 守护共享 store 故事的
// TestDaemonRunDoesNotClobberConcurrentStoreEdits 守护共享 store故事的写入侧。扫描开始时重载（见 daemon_reload_test.go）防止
// daemon 重跑别处已处理的时隙，但运行结果是由扫描时取得的内存快照稍后保存的。如果另一个进程在该运行进行中
// 由扫描时取得的内存快照稍后保存。如果另一个
// 进程在该运行进行中添加、编辑或删除任务，保存
// 添加、编辑或删除任务，从陈旧快照保存整个列表会静默丢弃新增项并
// 复活删除项。recordRun 必须在写入前从磁盘刷新。
func TestDaemonRunDoesNotClobberConcurrentStoreEdits(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	now := mustTime(t, "2026-01-01T12:00:00Z")
	if err := store.Put(dueSpec("nightly", now.Add(-time.Hour))); err != nil {
		t.Fatalf("Put nightly: %v", err)
	}
	if err := store.Put(testSpec("doomed")); err != nil {
		t.Fatalf("Put doomed: %v", err)
	}

	// 阻塞 executor，使运行完成前不再发生扫描重载：唯一在用的快照就是上面 scan() 取得的那个。
	// 完成前不再发生扫描重载：唯一在用的快照就是上面 scan() 取得的。
	started := make(chan struct{})
	release := make(chan struct{})
	exec := &fakeExecutor{run: func(ctx context.Context, spec Spec) (RunResult, error) {
		close(started)
		<-release
		return RunResult{Status: StatusOK}, nil
	}}
	d, err := newDaemon(Options{Store: store, Executor: exec, Clock: newFakeClock(now).Now})
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}
	d.scan(context.Background())
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the executor never started")
	}

	// 一个独立进程在运行进行中编辑共享任务列表。
	other, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore (other): %v", err)
	}
	if err := other.Put(testSpec("added")); err != nil {
		t.Fatalf("Put added: %v", err)
	}
	if _, err := other.Delete("doomed"); err != nil {
		t.Fatalf("Delete doomed: %v", err)
	}

	close(release)
	d.wait()

	fresh, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, ok := fresh.Get("added"); !ok {
		t.Fatal("task added by another process was lost when the daemon saved its run")
	}
	if _, ok := fresh.Get("doomed"); ok {
		t.Fatal("task deleted by another process was resurrected when the daemon saved its run")
	}
	if got, ok := fresh.Get("nightly"); !ok || got.LastStatus != StatusOK {
		t.Fatalf("the daemon's own run outcome was not recorded: %+v (ok=%v)", got, ok)
	}
}
