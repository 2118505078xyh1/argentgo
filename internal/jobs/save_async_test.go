package jobs

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// hangGuard 是一个宽松的安全网，使回归不会挂住整个
// 套件。实际的编排用 channel 完成，而非紧时序。
// thresholds.
const hangGuard = 10 * time.Second

// waitPersisted 等待直到每个内存修改都到达磁盘。派生 reaper 的测试用它，使目录拆卸永不与写入竞争。
// 派生 reaper 的测试用它，使目录拆卸永不与写入竞争。
func waitPersisted(t *testing.T, r *Registry) {
	t.Helper()
	deadline := time.Now().Add(hangGuard)
	for {
		r.mu.Lock()
		caughtUp := r.persisted >= r.version && !r.dirty
		r.mu.Unlock()
		if caughtUp {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("registry did not persist its state")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestListDoesNotBlockAWrite 检查重构的核心：磁盘
// 写入进行中时，List 可以等待它但必须释放 mu，使并发的
// Get 从不被阻塞。握手而非时间阈值驱动测试。
func TestListDoesNotBlockAWrite(t *testing.T) {
	r := newRegistry(t)

	// 持有 ioMu 以模拟缓慢的磁盘写入。Start 将在 mu 下注册其作业，
	// 然后在 persist 中阻塞于 ioMu。
	r.ioMu.Lock()

	startDone := make(chan error, 1)
	go func() {
		_, err := r.Start(shellCommand("echo async-list"))
		startDone <- err
	}()

	// 等待直到 Start 注册了作业（它已增加 inflight 且
	// 即将阻塞于 ioMu）。
	deadline := time.Now().Add(hangGuard)
	for {
		r.mu.Lock()
		inserted := len(r.jobs) > 0
		r.mu.Unlock()
		if inserted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Start did not register its job")
		}
		runtime.Gosched()
	}

	// List 将等待进行中的写入落地。
	listDone := make(chan struct{})
	go func() {
		defer close(listDone)
		_, _ = r.List()
	}()

	// 给 List 时间进入其等待，然后证明 mu 可供另一个
	// 调用方使用 —— 这正是并发 Start/Get 依赖的保证。
	runtime.Gosched()
	muAcquired := make(chan struct{})
	go func() {
		r.mu.Lock()
		r.mu.Unlock()
		close(muAcquired)
	}()
	select {
	case <-muAcquired:
		// List 在等待磁盘时未持有 mu。
	case <-time.After(hangGuard):
		t.Fatal("List held mu while waiting for a disk write")
	}

	// TestListDoesNotBlockAWrite 检查重构的核心：磁盘写入进行中时，List 可以等待它但必须释放 mu，使并发的 Start/Get 从不被阻塞。握手而非时间阈值驱动测试。
	r.ioMu.Unlock()
	select {
	case <-listDone:
	case <-time.After(hangGuard):
		t.Fatal("List did not finish after the write completed")
	}
	select {
	case err := <-startDone:
		if err != nil {
			t.Fatalf("start: %v", err)
		}
	case <-time.After(hangGuard):
		t.Fatal("Start deadlocked with a concurrent List")
	}

	jobs, err := r.List()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("list = %+v, err = %v", jobs, err)
	}
	// 此后 registry 完全静止（其目录可安全拆卸）。
	waitForTerminal(t, r, jobs[0].ID)
	waitPersisted(t, r)
}

// TestListAndStartRunConcurrently 同时压测两条路径：无死锁、
// 无作业丢失，且最终只有一个一致的索引。
func TestListAndStartRunConcurrently(t *testing.T) {
	r := newRegistry(t)
	const starts = 6

	var wg sync.WaitGroup
	errs := make(chan error, starts*2)
	for i := 0; i < starts; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if _, err := r.Start(shellCommand("echo worker-async")); err != nil {
				errs <- err
			}
		}(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				if _, err := r.List(); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent operation failed: %v", err)
	}

	listed, err := r.List()
	if err != nil || len(listed) != starts {
		t.Fatalf("listed %d jobs, want %d (err = %v)", len(listed), starts, err)
	}
	for _, job := range listed {
		waitForTerminal(t, r, job.ID)
	}
	waitPersisted(t, r)
}

// TestListAndStartRunConcurrently 同时压测两条路径：无死锁、无作业丢失，且最终只有一个一致的索引。signalLive 是一个 LiveProcess，能报告其 ExitCode 何时被读取，使测试知道完成观察者到达了其决策点。
// 使测试知道完成观察者到达了其决策点。
type signalLive struct {
	pid      int
	done     chan struct{}
	code     int
	codeRead chan struct{}
	doneOnce sync.Once
	codeOnce sync.Once
}

func newSignalLive(pid int) *signalLive {
	return &signalLive{pid: pid, done: make(chan struct{}), codeRead: make(chan struct{})}
}

func (s *signalLive) Pid() int              { return s.pid }
func (s *signalLive) Done() <-chan struct{} { return s.done }
func (s *signalLive) ExitCode() int {
	s.codeOnce.Do(func() { close(s.codeRead) })
	return s.code
}
func (s *signalLive) Kill() {
	s.doneOnce.Do(func() { close(s.done) })
}

// TestStoppedKilledStatusSurvivesWatchers 覆盖两处在 mu 下检查的站点。
// 观察者看到子进程退出绝不能把 Stop 设置的 killed 状态
// 改回 exited。
func TestStoppedKilledStatusSurvivesWatchers(t *testing.T) {
	t.Run("promoted watcher", func(t *testing.T) {
		r := newRegistry(t)
		live := newSignalLive(991)
		job, err := r.PromoteFromTimeout("watch-test", live, filepath.Join(t.TempDir(), "job.log"))
		if err != nil {
			t.Fatalf("promote: %v", err)
		}
		if err := r.Stop(job.ID); err != nil {
			t.Fatalf("stop: %v", err)
		}
		// Stop 关闭 Done；观察者现在读取退出码。等待
		// 那个信号使检查确定。
		select {
		case <-live.codeRead:
		case <-time.After(hangGuard):
			t.Fatal("completion watcher never ran")
		}
		got, err := r.Get(job.ID)
		if err != nil || got.Status != StatusKilled {
			t.Fatalf("status = %q, err = %v, want killed", got.Status, err)
		}
	})

	t.Run("start reaper", func(t *testing.T) {
		r := newRegistry(t)
		// TestStoppedKilledStatusSurvivesWatchers 覆盖两处在 mu 下检查的站点。观察者看到子进程退出绝不能把 Stop 设置的 killed 状态改回 exited。Stop 关闭 Done；观察者现在读取退出码。等待那个信号使检查确定。种入一个 killed 作业，然后给 reap 喂一个已等待的子进程，使它记录退出而不依赖任何进程时序。
		// 使它记录退出而不依赖任何进程时序。
		r.mu.Lock()
		r.jobs["job1"] = &Job{ID: "job1", Status: StatusKilled, ExitCode: -1}
		r.mu.Unlock()

		cmd := exec.Command(os.Args[0], "-test.run=^$")
		if err := cmd.Run(); err != nil {
			t.Fatalf("run helper: %v", err)
		}
		r.reap("job1", cmd)

		got, err := r.Get("job1")
		if err != nil || got.Status != StatusKilled {
			t.Fatalf("status = %q, err = %v, want killed", got.Status, err)
		}
		waitPersisted(t, r)
	})
}

// TestIndexRoundTripAfterAsyncChanges 检查写入路径的持久化在
// 重新打开后保持磁盘索引一致，包括 Stop 设置的状态。
func TestIndexRoundTripAfterAsyncChanges(t *testing.T) {
	dir := t.TempDir()
	first, err := New(dir)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	live := newSignalLive(992)
	job, err := first.PromoteFromTimeout("persist-test", live, filepath.Join(dir, "job.log"))
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if err := first.Stop(job.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	waitPersisted(t, first)

	second, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	reloaded, err := second.Get(job.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if reloaded.Status != StatusKilled {
		t.Fatalf("reloaded status = %q, want killed", reloaded.Status)
	}
	if reloaded.Command != job.Command || reloaded.LogPath != job.LogPath {
		t.Fatalf("reloaded job = %+v", reloaded)
	}

	// TestIndexRoundTripAfterAsyncChanges 检查写入路径的持久化在重新打开后保持磁盘索引一致，包括 Stop 设置的状态。下一个 id 不得与持久化的 id 冲突。
	nextLive := newSignalLive(993)
	next, err := second.PromoteFromTimeout("second", nextLive, filepath.Join(dir, "job2.log"))
	if err != nil {
		t.Fatalf("promote second: %v", err)
	}
	if next.ID == job.ID {
		t.Fatalf("id %q was reused after reopen", next.ID)
	}
}

// TestConcurrentListsLeaveAtomicIndex 检查重构的磁盘侧：并发读者绝不产生半写的临时文件，且每次写入都留下完整可解析的索引。
// 并发读者绝不产生半写的临时文件，且每次写入
// 都留下完整可解析的索引。
func TestConcurrentListsLeaveAtomicIndex(t *testing.T) {
	r := newRegistry(t)
	const starts = 8

	var wg sync.WaitGroup
	errs := make(chan error, starts*2)
	for i := 0; i < starts; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			if _, err := r.Start(shellCommand("echo atomic")); err != nil {
				errs <- err
			}
		}(i)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				if _, err := r.List(); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent operation failed: %v", err)
	}

	listed, err := r.List()
	if err != nil || len(listed) != starts {
		t.Fatalf("listed %d jobs, want %d (err = %v)", len(listed), starts, err)
	}
	for _, job := range listed {
		waitForTerminal(t, r, job.ID)
	}
	waitPersisted(t, r)

	data, err := os.ReadFile(filepath.Join(r.Dir(), storeFile))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	var stored index
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("index is not a complete JSON document: %v", err)
	}
	if len(stored.Jobs) != starts {
		t.Fatalf("index has %d jobs, want %d", len(stored.Jobs), starts)
	}

	entries, err := os.ReadDir(r.Dir())
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".jobs-") && strings.HasSuffix(name, ".tmp") {
			t.Fatalf("leftover temporary index file: %s", name)
		}
	}
}

// TestStaleSnapshotDoesNotOverwriteNewerState 守护版本检查：一个
// 在较新快照之后到达 ioMu 的较旧快照必须被跳过，而非
// 覆盖较新的索引。
func TestStaleSnapshotDoesNotOverwriteNewerState(t *testing.T) {
	r := newRegistry(t)

	r.mu.Lock()
	r.jobs["a"] = &Job{ID: "a", Status: StatusExited}
	r.jobs["b"] = &Job{ID: "b", Status: StatusExited}
	newSnap, newVersion := r.changedLocked()
	r.mu.Unlock()
	if err := r.persist(newSnap, newVersion); err != nil {
		t.Fatalf("persist newest: %v", err)
	}

	// 在 "b" 存在之前捕获但之后写入的快照。经过
	// TestStaleSnapshotDoesNotOverwriteNewerState 守护版本检查：在较新快照之后到达 ioMu 的较旧快照必须被跳过，而非覆盖较新的索引。在 "b" 存在之前捕获但之后写入的快照。经过 changedLocked 使进行中记账保持平衡。
	oldSnap := index{Jobs: []Job{{ID: "a", Status: StatusExited}}}
	r.mu.Lock()
	_, staleVersion := r.changedLocked()
	r.mu.Unlock()
	if err := r.persist(oldSnap, staleVersion-2); err != nil {
		t.Fatalf("persist stale: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(r.Dir(), storeFile))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	var stored index
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("index is not a complete JSON document: %v", err)
	}
	if len(stored.Jobs) != 2 {
		t.Fatalf("a stale snapshot overwrote the newer index: %+v", stored.Jobs)
	}
}
