package tools_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/jobs"
	"github.com/2118505078xyh1/argentgo/internal/proc"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// backgroundFakeChild 是一个内存中的进程，用于驱动 bash 工具的
// 提升路径，而无需启动真正的长时运行命令。
type backgroundFakeChild struct {
	pid       int
	code      int
	finishNow bool
	done      chan struct{}
	once      sync.Once
	mu        sync.Mutex
	killed    bool
	proc      *proc.Process
}

func newBackgroundFake(pid int, finishNow bool) *backgroundFakeChild {
	return &backgroundFakeChild{pid: pid, finishNow: finishNow, done: make(chan struct{})}
}

func (f *backgroundFakeChild) Pid() int { return f.pid }

func (f *backgroundFakeChild) Wait() int {
	if f.finishNow {
		_, _ = f.proc.Write([]byte("quick-output\n"))
		f.closeDone()
		return f.code
	}
	<-f.done
	return f.code
}

func (f *backgroundFakeChild) Kill() {
	f.mu.Lock()
	f.killed = true
	f.code = -1
	f.mu.Unlock()
	f.closeDone()
}

func (f *backgroundFakeChild) closeDone() { f.once.Do(func() { close(f.done) }) }

func (f *backgroundFakeChild) wasKilled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.killed
}

// installBackgroundFake 安装一个会对启动次数计数的 starter，并给子进程预置输出，
// 这样测试就能证明该命令没有被运行第二次。
func installBackgroundFake(t *testing.T, child *backgroundFakeChild, seed string) *int {
	t.Helper()
	var starts int
	var mu sync.Mutex
	proc.SetStarter(func(proc.Command) (*proc.Process, error) {
		mu.Lock()
		starts++
		mu.Unlock()
		p := proc.NewProcess(child)
		child.proc = p
		if seed != "" {
			_, _ = p.Write([]byte(seed))
		}
		return p, nil
	})
	t.Cleanup(func() { proc.SetStarter(nil) })
	return &starts
}

func TestBashPromotesLongCommandInsteadOfBlocking(t *testing.T) {
	registry, err := jobs.New(t.TempDir())
	if err != nil {
		t.Fatalf("new jobs registry: %v", err)
	}
	child := newBackgroundFake(9001, false)
	starts := installBackgroundFake(t, child, "partial-before-timeout\n")

	toolsRegistry := tools.New(t.TempDir(), true)
	toolsRegistry.SetJobs(registry)
	out, err := toolsRegistry.Run(context.Background(), "bash",
		`{"command":"some-long-command","background_after_ms":20}`)
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if !strings.Contains(out, "job1") {
		t.Fatalf("tool result has no job id: %q", out)
	}
	if !strings.Contains(out, "still running; check with `argent jobs`") {
		t.Fatalf("tool result has no job hint: %q", out)
	}
	if !strings.Contains(out, "partial-before-timeout") {
		t.Fatalf("tool result lost the partial output: %q", out)
	}
	if got := *starts; got != 1 {
		t.Fatalf("command started %d times, want exactly 1", got)
	}

	listed, err := registry.List()
	if err != nil || len(listed) != 1 {
		t.Fatalf("list = %+v, err = %v", listed, err)
	}
	job := listed[0]
	if job.ID != "job1" || job.Command != "some-long-command" || job.Status != jobs.StatusRunning {
		t.Fatalf("job = %+v", job)
	}
	if job.LogPath == "" {
		t.Fatal("promoted job has no log path")
	}
	tail, err := registry.Tail(job.ID, 20)
	if err != nil || !strings.Contains(tail, "partial-before-timeout") {
		t.Fatalf("log tail = %q, err = %v", tail, err)
	}

	// 通过注册表执行杀死操作，能命中工具启动的那个进程本身。
	if err := registry.Stop(job.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !child.wasKilled() {
		t.Fatal("jobs.Stop did not kill the promoted child")
	}
	stopped, err := registry.Get(job.ID)
	if err != nil || stopped.Status != jobs.StatusKilled {
		t.Fatalf("stopped = %+v, err = %v", stopped, err)
	}
}

func TestBashShortCommandIsUnaffectedByBackgroundOption(t *testing.T) {
	registry, err := jobs.New(t.TempDir())
	if err != nil {
		t.Fatalf("new jobs registry: %v", err)
	}
	child := newBackgroundFake(9002, true)
	starts := installBackgroundFake(t, child, "")

	toolsRegistry := tools.New(t.TempDir(), true)
	toolsRegistry.SetJobs(registry)
	out, err := toolsRegistry.Run(context.Background(), "bash",
		`{"command":"echo quick","background_after_ms":200}`)
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if !strings.Contains(out, "quick-output") {
		t.Fatalf("out = %q, want the command output", out)
	}
	if strings.Contains(out, "job1") || strings.Contains(out, "still running") {
		t.Fatalf("a quick command must not be promoted: %q", out)
	}
	if got := *starts; got != 1 {
		t.Fatalf("command started %d times, want 1", got)
	}
	if child.wasKilled() {
		t.Fatal("a finished command must not be killed")
	}
	listed, err := registry.List()
	if err != nil || len(listed) != 0 {
		t.Fatalf("list = %+v, err = %v, want no jobs", listed, err)
	}
}

func TestEditAmbiguityErrorIncludesCountPhrase(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "notes.txt", "dup\ndup\n")
	registry := tools.New(root, true)

	_, err := registry.Run(context.Background(), "edit",
		`{"path":"notes.txt","old":"dup","new":"done"}`)
	if err == nil || !strings.Contains(err.Error(), "appears 2 times") {
		t.Fatalf("edit err = %v, want the count phrase", err)
	}
	if err == nil || !strings.Contains(err.Error(), "not unique") {
		t.Fatalf("edit err = %v, want the uniqueness hint", err)
	}

	_, err = registry.Run(context.Background(), "multi_edit",
		`{"path":"notes.txt","edits":[{"old":"dup","new":"done"}]}`)
	if err == nil || !strings.Contains(err.Error(), "appears 2 times") {
		t.Fatalf("multi_edit err = %v, want the count phrase", err)
	}
	// 两个工具都必须让存在歧义的文件保持原样。
	data, readErr := os.ReadFile(filepath.Join(root, "notes.txt"))
	if readErr != nil || string(data) != "dup\ndup\n" {
		t.Fatalf("content = %q, err = %v", data, readErr)
	}
}
