package jobs

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// LiveProcess 是工具超时移交给 registry 的运行中子进程。它是
// LiveProcess 是工具超时移交给 registry 的运行中子进程。*proc.Process 在结构上满足它；在这里声明它使 jobs 不依赖 proc。Kill 终止子进程及其后代。Done 在子进程被回收后关闭。ExitCode 在 Done 关闭后有效。
// 不依赖 proc。
type LiveProcess interface {
	Pid() int
	// Kill 终止子进程及其后代。
	Kill()
	// Done 在子进程被回收后关闭。
	Done() <-chan struct{}
	// ExitCode 在 Done 关闭后有效。
	ExitCode() int
}

// PromoteFromTimeout 把一个已在运行的子进程记录为后台作业。
//
// PromoteFromTimeout 把一个已在运行的子进程记录为后台作业。logPath 是子进程已在写入的文件：它保存超时前产生的输出并继续接收其余部分。因此提升既不重跑命令也不丢失其部分输出。返回的作业注册为运行中；观察者一俟子进程死亡就把它翻转为已退出并记录退出码。
// 超时前产生的输出并继续接收其余部分。因此提升
// 既不重跑命令也不丢失其部分输出。返回的作业
// 注册为运行中；观察者一俟子进程死亡就把它翻转为已退出并记录退出码
// 便记录退出码。
func (r *Registry) PromoteFromTimeout(command string, live LiveProcess, logPath string) (Job, error) {
	if strings.TrimSpace(command) == "" {
		return Job{}, errors.New("jobs: empty command")
	}
	if live == nil {
		return Job{}, errors.New("jobs: no live process")
	}
	id := r.allocateID("")
	job := Job{
		ID:        id,
		Command:   command,
		StartedAt: time.Now().UTC(),
		Status:    StatusRunning,
		ExitCode:  -1,
		LogPath:   logPath,
		PID:       live.Pid(),
		live:      live,
		watched:   true,
	}

	// 返回值和 registry 的指针使用分离的存储：
	// 返回值和 registry 的指针使用分离的存储：watchLive 在 mu 下修改 registry 副本，而调用方读取返回的 Job。把它们设为别名（存 &job）是数据竞争。
	// 返回的 Job。把它们设为别名（存 &job）是数据竞争。
	stored := job
	r.mu.Lock()
	r.jobs[id] = &stored
	snap, version := r.changedLocked()
	r.mu.Unlock()

	r.bgWork.Add(1)
	go func() {
		defer r.bgWork.Done()
		r.watchLive(id, live)
	}()
	if saveErr := r.persist(snap, version); saveErr != nil {
		return job, fmt.Errorf("jobs: save %s: %w", id, saveErr)
	}
	return job, nil
}

// watchLive 是提升作业的完成观察者。Stop 已把状态改为 killed，因此它不得覆盖那个终态。hangGuard 是一个宽松的安全网，使回归不会挂住整个套件。实际的编排用 channel 完成，而非紧时序。
// 状态改为 killed，因此它不得覆盖那个终态。
func (r *Registry) watchLive(id string, live LiveProcess) {
	<-live.Done()
	code := live.ExitCode()

	r.mu.Lock()
	job, ok := r.jobs[id]
	if !ok || job.Status != StatusRunning {
		r.mu.Unlock()
		return
	}
	job.Status = StatusExited
	job.ExitCode = code
	snap, version := r.changedLocked()
	r.mu.Unlock()
	_ = r.persist(snap, version)
}
