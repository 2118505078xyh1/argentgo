package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"example.com/argentgo/internal/jobs"
	"example.com/argentgo/internal/proc"
	"example.com/argentgo/internal/tools"
)

// jobsWireFakeChild 是一个永不自行结束的内存子进程，用于驱动提升路径。
// 它只在 Kill 时关闭 done，因此 StopAll 的效果可被直接断言。
type jobsWireFakeChild struct {
	pid    int
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	killed bool
	proc   *proc.Process
}

func (f *jobsWireFakeChild) Pid() int { return f.pid }
func (f *jobsWireFakeChild) Wait() int {
	<-f.done
	return -1
}
func (f *jobsWireFakeChild) Kill() {
	f.mu.Lock()
	f.killed = true
	f.mu.Unlock()
	f.once.Do(func() { close(f.done) })
}
func (f *jobsWireFakeChild) wasKilled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.killed
}

// installJobsWireStarter 安装一个返回固定伪子进程的启动器，替代真实 shell 派生。
func installJobsWireStarter(t *testing.T, child *jobsWireFakeChild) {
	t.Helper()
	proc.SetStarter(func(proc.Command) (*proc.Process, error) {
		p := proc.NewProcess(child)
		child.proc = p
		return p, nil
	})
	t.Cleanup(func() { proc.SetStarter(nil) })
}

func TestJobsOptInEnabledDefaultOff(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"nil env", nil},
		{"empty", map[string]string{}},
		{"jobs off", map[string]string{"ARGENT_JOBS": "0"}},
		{"jobs false", map[string]string{"ARGENT_JOBS": "false"}},
		{"other opt-in", map[string]string{"ARGENT_OPT_INS": "cli_hints,grep_ex"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if jobsOptInEnabled(envFrom(tc.env)) {
				t.Fatal("未显式 opt-in 时必须默认关闭")
			}
		})
	}
}

func TestJobsOptInEnabledOn(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"dedicated env", map[string]string{"ARGENT_JOBS": "1"}},
		{"dedicated env padded", map[string]string{"ARGENT_JOBS": " 1 "}},
		{"unified list", map[string]string{"ARGENT_OPT_INS": "cli_hints, jobs "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !jobsOptInEnabled(envFrom(tc.env)) {
				t.Fatal("显式 opt-in 时应开启后台作业注册表")
			}
		})
	}
}

// 默认关闭时：不建目录、不装注册表、stop 为空操作，逐字节不变。
func TestWireJobsDefaultOffIsNoop(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "sess")
	registry := tools.New(t.TempDir(), true)
	got, stop, err := wireJobs(envFrom(nil), sessionDir, []*tools.Registry{registry})
	if err != nil {
		t.Fatalf("wireJobs: %v", err)
	}
	if got != nil {
		t.Fatalf("默认关闭时 registry = %v, want nil", got)
	}
	if err := stop(context.Background()); err != nil {
		t.Fatalf("默认关闭时 stop 应为 no-op, 得到 %v", err)
	}
	if _, err := os.Stat(jobRegistryDir(sessionDir)); !os.IsNotExist(err) {
		t.Fatalf("默认关闭时不应创建 jobs 目录: stat err = %v", err)
	}

	// 没有作业注册表时，background_after_ms 被静默忽略：长命令沿用硬超时杀死。
	child := &jobsWireFakeChild{pid: 92001, done: make(chan struct{})}
	installJobsWireStarter(t, child)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _ = registry.Run(ctx, "bash", `{"command":"long-cmd","background_after_ms":20}`)
	if child.proc != nil {
		t.Fatal("默认关闭时不应走提升路径（proc 启动器未被调用）")
	}
	if child.wasKilled() {
		t.Fatal("默认关闭时超时命令应走原来的杀死路径，但子进程被提升路径误杀")
	}
}

// opt-in 开启时：注册表建立、SetJobs 生效、退出 stop 调用 StopAll。
func TestWireJobsOptInOnPromotesAndStops(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "sess")
	registry := tools.New(t.TempDir(), true)
	jobReg, stop, err := wireJobs(
		envFrom(map[string]string{"ARGENT_JOBS": "1"}),
		sessionDir,
		[]*tools.Registry{registry},
	)
	if err != nil {
		t.Fatalf("wireJobs: %v", err)
	}
	if jobReg == nil {
		t.Fatal("opt-in 开启时应建立后台作业注册表")
	}
	if info, err := os.Stat(jobRegistryDir(sessionDir)); err != nil || !info.IsDir() {
		t.Fatalf("jobs 目录未建立: info = %v, err = %v", info, err)
	}

	child := &jobsWireFakeChild{pid: 92002, done: make(chan struct{})}
	installJobsWireStarter(t, child)
	out, err := registry.Run(context.Background(), "bash",
		`{"command":"long-cmd","background_after_ms":20}`)
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if !strings.Contains(out, "job1") {
		t.Fatalf("后台作业未被提升，工具输出 = %q", out)
	}

	listed, err := jobReg.List()
	if err != nil || len(listed) != 1 {
		t.Fatalf("list = %+v, err = %v, want 一个作业", listed, err)
	}
	if _, err := os.Stat(filepath.Join(jobRegistryDir(sessionDir), "jobs.json")); err != nil {
		t.Fatalf("提升后应写入 jobs.json: %v", err)
	}

	// 退出钩子调用 StopAll：仍运行的作业被发信号，子进程确实被杀死。
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := stop(stopCtx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !child.wasKilled() {
		t.Fatal("StopAll 未杀死被提升的子进程")
	}
	stopped, err := jobReg.Get(listed[0].ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stopped.Status != jobs.StatusKilled {
		t.Fatalf("作业状态 = %q, want killed", stopped.Status)
	}
}
