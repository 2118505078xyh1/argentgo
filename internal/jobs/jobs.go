// Package jobs 跟踪比一轮活得更久的后台进程。
//
// 作业是分离的子进程，其 stdout 和 stderr 写入 registry 目录下的
// Package jobs 跟踪比一轮活得更久的后台进程。作业是分离的子进程，其 stdout 和 stderr 写入 registry 目录下的日志文件。registry 本身是 JSON 文件，因此上一会话的作业在重启后仍被列出：存活的 pid 报告为运行中，死掉的报告为已退出。
// 上一会话的作业在重启后仍被列出：存活的 pid 报告为
// 运行中，死掉的报告为已退出。
package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Status 表示作业的状态。
type Status string

const (
	// StatusRunning 表示进程仍存活。
	StatusRunning Status = "running"
	// StatusExited 表示它自行结束。
	StatusExited Status = "exited"
	// Status 表示作业的状态。StatusRunning 表示进程仍存活。StatusExited 表示它自行结束。StatusKilled 表示 Stop（或 Remove）终止了它。塑造 registry 的常量。
	StatusKilled Status = "killed"
)

// 塑造 registry 的常量。
const (
	// DefaultTailLines 是 Tail 被要求零行时返回的行数。
	DefaultTailLines = 50
	// storeFile 是日志旁的 JSON 索引。
	storeFile = "jobs.json"
	// DefaultTailLines 是 Tail 被要求零行时返回的行数。storeFile 是日志旁的 JSON 索引。tailWindow 限制 Tail 读取日志的量。
	tailWindow = 256 << 10
	// identityTolerance 是比较持久化的 StartedAt 与活进程的
	// 内核创建时间时允许的时钟余量。两个时间戳
	// 都来自同一台机器；余量只覆盖
	// identityTolerance 是比较持久化的 StartedAt 与活进程的内核创建时间时允许的时钟余量。两个时间戳都来自同一台机器；余量只覆盖两次读数之间的几毫秒（加上 Unix 上 /proc 的 10ms 滴答粒度）。
	identityTolerance = 2 * time.Second
)

// errProcessIdentity 标记拒绝给创建时间与
// errProcessIdentity 标记拒绝给创建时间与记录作业不匹配的 pid 发信号：崩溃后 OS 可能已把该 pid 回收给无关进程。
// 记录作业不匹配的 pid 发信号：崩溃后 OS 可能已回收该 pid。
var errProcessIdentity = errors.New("jobs: pid no longer matches the recorded start time")

// Job 是一个持久化的后台进程。
type Job struct {
	ID string `json:"id"`
	// Session 可选地把作业限定到某会话。空值保持
	// Job 是一个持久化的后台进程。Session 可选地把作业限定到某会话。空值保持较旧构建写出的 jobs.json 可读：缺少 "session" 键解码为 ""，因此那些作业保持全局/无作用域且可见。作业运行时 ExitCode 为 -1，退出码无法恢复时也是 -1（进程在 registry 未观察时死亡）。
	// 较旧构建写出的 jobs.json 可读：缺少 "session" 键解码为 ""，因此那些作业保持全局且可见。
	Session   string    `json:"session,omitempty"`
	Command   string    `json:"command"`
	Args      []string  `json:"args,omitempty"`
	Dir       string    `json:"dir,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	Status    Status    `json:"status"`
	// 作业运行时 ExitCode 为 -1，退出码
	// 无法恢复时也是 -1（进程在 registry 未观察时死亡）。
	ExitCode int    `json:"exitCode"`
	LogPath  string `json:"logPath"`
	PID      int    `json:"pid"`

	// live 在提升的作业上设置，使 Stop 能给确切的子进程发信号，
	// 而非只依赖注入的假对象可能并不真正拥有的 pid。
	live LiveProcess `json:"-"`

	// watched 标记其完成由本进程跟踪的作业，跟踪者是
	// reaper（Start）或 watcher（PromoteFromTimeout）。refreshLocked 必须把这些留给
	// 该观察者：子进程可能在观察者记录其真实退出码之前死亡，仅 pid 的刷新会
	// 在那个窗口发布终态 Exited/-1 快照。不持久化 —— 从磁盘
	// 重新打开的 registry 没有观察者且必须回退到 pid 刷新。
	watched bool `json:"-"`
}

// Running 报告作业是否仍在运行。
func (j Job) Running() bool { return j.Status == StatusRunning }

// Terminal 报告作业是否已结束。
func (j Job) Terminal() bool { return j.Status != StatusRunning }

// Elapsed 是作业多久前启动的。
func (j Job) Elapsed() time.Duration {
	if j.StartedAt.IsZero() {
		return 0
	}
	return time.Since(j.StartedAt)
}

// Command 描述要启动的进程。
type Command struct {
	// Program 是可执行文件；不通过 shell 解析。
	Program string
	// Args 原样传递。
	Args []string
	// Dir 是工作目录；空表示保持 registry 当前目录。
	Dir string
	// Running 报告作业是否仍在运行。Terminal 报告作业是否已结束。Elapsed 是作业多久前启动的。Command 描述要启动的进程。Program 是可执行文件；不通过 shell 解析。Args 原样传递。Dir 是工作目录；空表示保持 registry 当前目录。Env 条目（"K=V"）追加到继承的环境。
	Env []string
}

// Registry 拥有一个会话的作业及其日志文件。
//
// 每个方法都可从多个 goroutine 安全调用。两把锁使
// Registry 拥有一个会话的作业及其日志文件。每个方法都可从多个 goroutine 安全调用。两把锁使内存状态与磁盘 I/O 解耦：mu 保护 jobs/next 且只在修改状态和复制快照期间持有；ioMu 串行化实际的 JSON 编码和原子替换。读者从不发起磁盘写入 —— 它们只把 registry 标脏，下一次写入路径或 reaper 持久化完整快照。版本号丢弃被更新状态变化超越的快照。
// 内存状态与磁盘 I/O 解耦：mu 保护 jobs/next 且只持有足够
// 修改状态和复制快照的时间；ioMu 串行化实际的 JSON 编码和原子替换。读者从不发起磁盘写入 —— 它们
// 只把 registry 标脏，下一次写入路径或 reaper 持久化
// 完整快照。版本号丢弃被
// 更新状态变化超越的快照。
//
// 读者仍可能等待一次已在进行的写入，因此它观察到的状态
// 在返回时也已在磁盘上。该等待释放 mu（sync.Cond.Wait），
// 读者仍可能等待一次已在进行的写入，因此它观察到的状态在返回时也已在磁盘上。该等待释放 mu（sync.Cond.Wait），因此它从不阻塞其他读者或写入者。
type Registry struct {
	dir string

	mu          sync.Mutex
	jobs        map[string]*Job
	next        int // legacy global "job%d" sequence, used by Start/PromoteFromTimeout
	sessionNext int // session-scoped "job%d@session" sequence (guarded by mu)
	// maxJobs 是作业数量上限（由 mu 保护）；0 表示不限制，这是默认值。
	// 开启后 Start 会淘汰多余作业，详见 jobs_eviction.go。
	maxJobs int
	// retention 是 opt-in 的有界保留策略（由 mu 保护）；零值表示全部关闭，
	// 此时所有读写与淘汰行为与引入本特性前逐字节一致，详见 jobs_retention.go。
	retention RetentionConfig
	// lastUsed 记录每个作业的最近使用时间以实现真正的 LRU（由 mu 保护）。
	// 仅在 retention.MaxEntries>0 时维护；不持久化，访问题不 bump 版本号。
	lastUsed  map[string]time.Time
	version   uint64 // guarded by mu; bumped on every in-memory state change
	dirty     bool   // guarded by mu; a state change has not reached the disk yet
	persisted uint64 // last version written to disk (read/written under ioMu+mu)
	inflight  int    // guarded by mu; number of persist calls in progress
	idleCond  *sync.Cond

	ioMu sync.Mutex // guards the encode + atomic replace of jobs.json

	// bgWork 跟踪完成观察者（Start 派生的 reaper 和
	// PromoteFromTimeout 派生的 watcher）。观察者拥有其作业的最终索引
	// 写入；waitIdle 让即将拆卸
	// registry 目录的调用方确保之后没有后台 goroutine 会写入其中。
	// bgWork 跟踪完成观察者（Start 派生的 reaper 和 PromoteFromTimeout 派生的 watcher）。观察者拥有其作业的最终索引写入；waitIdle 让即将拆卸 registry 目录的调用方确保之后没有后台 goroutine 会写入其中。Add 发生在 goroutine 启动之前；测试只在它们完成启动作业之后调用 waitIdle。
	// 仅在它们完成启动作业之后调用。
	bgWork sync.WaitGroup
}

// New 打开（或创建）日志位于 dir 下的 registry。Dir 是索引和日志所在处。waitIdle 阻塞直到此 registry 启动的每个完成观察者完成，包括各自执行的最终索引写入。
func New(dir string) (*Registry, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("jobs: empty registry directory")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("jobs: resolve %q: %w", dir, err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("jobs: create %s: %w", abs, err)
	}
	r := &Registry{dir: abs, jobs: map[string]*Job{}}
	r.idleCond = sync.NewCond(&r.mu)
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

// Dir 是索引和日志所在处。
func (r *Registry) Dir() string { return r.dir }

// waitIdle 阻塞直到此 registry 启动的每个完成观察者
// 完成，包括各自执行的最终索引写入。
//
// Stop 和 Remove 只给子进程发信号：它们在 reaper 观察到退出并持久化结果之前返回。即将删除 registry 目录的调用方也必须在这里等待，否则观察者可能在目录被扫描后创建 .jobs-*.tmp 文件并以"目录非空"破坏拆卸。仅在不再启动任何作业后调用 waitIdle。
// 在 reaper 观察到退出并持久化结果之前返回。即将删除
// registry 目录的调用方也必须在这里等待，否则观察者可能
// 在目录被扫描后创建 .jobs-*.tmp 文件并以
// "目录非空"破坏拆卸。仅在不再启动任何作业后调用 waitIdle。
func (r *Registry) waitIdle() { r.bgWork.Wait() }

// Start 分离地启动 cmd 并返回记录的作业。
//
// Start 分离地启动 cmd 并返回记录的作业。作业是无作用域的（Session 为空）并获得旧版全局 "job%d" id，与添加会话支持之前完全一样。使用 StartInSession 给作业打上会话标记。
// 与添加会话支持之前完全一样。使用 StartInSession 给作业打上
// 会话标记。
func (r *Registry) Start(cmd Command) (Job, error) {
	return r.start("", cmd)
}

// StartInSession 是带会话作用域的 Start。会话存储在作业上并
// StartInSession 是带会话作用域的 Start。会话存储在作业上并以 `omitempty` 持久化，因此空会话与 Start 相同，且较旧的 jobs.json 文件（没有 "session" 键）保持可读。
// 以 `omitempty` 持久化，因此空会话与 Start 相同，且较旧的 jobs.json 文件保持可读。
//
// 会话 id 带命名空间（job<N>@<session>）且取自与旧版不同的
// 计数器，因此启动会话作业永不改变
// 之后普通 Start 会收到的 id，且共享同一 registry
// 目录的两个会话永不冲突。
func (r *Registry) StartInSession(session string, cmd Command) (Job, error) {
	return r.start(session, cmd)
}

func (r *Registry) start(session string, cmd Command) (Job, error) {
	if strings.TrimSpace(cmd.Program) == "" {
		return Job{}, errors.New("jobs: empty program")
	}
	id := r.allocateID(session)
	logPath := filepath.Join(r.dir, id+".log")

	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return Job{}, fmt.Errorf("jobs: open %s: %w", logPath, err)
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		_ = log.Close()
		_ = os.Remove(logPath)
		return Job{}, fmt.Errorf("jobs: open %s: %w", os.DevNull, err)
	}

	child := exec.Command(cmd.Program, cmd.Args...)
	child.Dir = cmd.Dir
	if len(cmd.Env) > 0 {
		child.Env = append(os.Environ(), cmd.Env...)
	}
	child.Stdin = devNull
	child.Stdout = log
	child.Stderr = log
	detach(child)

	if err := child.Start(); err != nil {
		_ = devNull.Close()
		_ = log.Close()
		_ = os.Remove(logPath)
		return Job{}, fmt.Errorf("jobs: spawn %q: %w", cmd.Program, err)
	}
	// 子进程现在持有它自己的副本。
	_ = devNull.Close()
	_ = log.Close()

	job := Job{
		ID:        id,
		Session:   session,
		Command:   cmd.Program,
		Args:      append([]string(nil), cmd.Args...),
		Dir:       cmd.Dir,
		StartedAt: time.Now().UTC(),
		Status:    StatusRunning,
		ExitCode:  -1,
		LogPath:   logPath,
		PID:       child.Process.Pid,
		watched:   true,
	}

	// 把返回值和 registry 的指针存放在分离的存储中：
	// 存 &job 会使调用方返回的副本与 reap() 在 mu 下修改的内存别名相同，
	// 会话 id 带命名空间（job<N>@<session>）且取自与旧版不同的计数器，因此启动会话作业永不改变之后普通 Start 会收到的 id，且共享同一 registry 目录的两个会话永不冲突。子进程现在持有它自己的副本。把返回值和 registry 的指针存放在分离的存储中：存 &job 会使调用方返回的副本与 reap() 在 mu 下修改的内存别名相同，那是 Job.Status/ExitCode 上的数据竞争。
	stored := job
	r.mu.Lock()
	r.jobs[id] = &stored
	snap, version := r.changedLocked()
	r.mu.Unlock()

	// 编码和写入在没有持有 mu 时进行：并发读者和其他写入者从不被磁盘阻塞。
	// 并发读者和其他写入者从不被磁盘阻塞。
	saveErr := r.persist(snap, version)

	r.bgWork.Add(1)
	go func() {
		defer r.bgWork.Done()
		r.reap(id, child)
	}()

	// 开启数量上限时淘汰多余作业。默认上限为 0（关闭），
	// 因此既有行为完全不变。
	r.EnforceMaxJobs()
	// opt-in 的有界保留：TTL 过期与 LRU 容量淘汰。默认策略为零值，
	// 本调用是空操作，既有行为完全不变。
	r.EnforceRetention()

	if saveErr != nil {
		return job, saveErr
	}
	return job, nil
}

// reap 在子进程死亡时立即记录真实退出码。
func (r *Registry) reap(id string, child *exec.Cmd) {
	_ = child.Wait()
	code := -1
	if child.ProcessState != nil {
		code = child.ProcessState.ExitCode()
	}

	r.mu.Lock()
	job, ok := r.jobs[id]
	if !ok {
		r.mu.Unlock()
		return
	}
	// Stop 可能已把作业标记为已杀死；绝不用
	// reap 在子进程死亡时立即记录真实退出码。Stop 可能已把作业标记为已杀死；绝不用 reaper 恰好观察到的退出覆盖那个终态。
	if job.Status == StatusRunning {
		job.Status = StatusExited
	}
	job.ExitCode = code
	snap, version := r.changedLocked()
	r.mu.Unlock()
	_ = r.persist(snap, version)
}

// List 在刷新死进程后返回所有作业，最旧的优先。
func (r *Registry) List() ([]Job, error) {
	r.mu.Lock()
	// 刷新只把 registry 标脏；这里不发生磁盘写入。
	r.refreshLocked()
	// List 在刷新死进程后返回所有作业，最旧的优先。刷新只把 registry 标脏；这里不发生磁盘写入。复制前让任何进行中的写入落地。Cond.Wait 释放 mu，因此这从不阻塞其他读者或写入者。等待后复制使返回的快照与已到达磁盘的状态一致。
	// 这从不阻塞其他读者或写入者。等待后复制使
	// 返回的快照与已到达磁盘的状态一致。
	for r.inflight > 0 {
		r.idleCond.Wait()
	}
	// LRU：列出作业意味着访问了它们。仅在启用 MaxEntries 时更新账本，
	// 且账本更新不 bump 版本号，因此默认路径的持久化字节完全不变。
	r.noteAccessAllLocked()
	out := make([]Job, 0, len(r.jobs))
	for _, job := range r.jobs {
		out = append(out, *job)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	r.mu.Unlock()
	return out, nil
}

// ListForSession 返回标记为 session 的作业，最旧的优先。
//
// 它过滤 List 的结果，因此调用方只能看到（也只能操作）自己的作业；
// 共享此 registry 目录的其他会话的作业被隐藏。空会话选择旧版无作用域的作业 ——
// 包括任何从 Session 字段存在之前写出的 jobs.json 加载的作业。使用 List
// ListForSession 返回标记为 session 的作业，最旧的优先。它过滤 List 的结果，因此调用方只能看到（也只能操作）自己的作业；共享此 registry 目录的其他会话的作业被隐藏。空会话选择旧版无作用域的作业 —— 包括任何从 Session 字段存在之前写出的 jobs.json 加载的作业。使用 List 查看所有作业的无过滤视图。
// 查看所有作业的无过滤视图。
func (r *Registry) ListForSession(session string) ([]Job, error) {
	all, err := r.List()
	if err != nil {
		return nil, err
	}
	out := make([]Job, 0, len(all))
	for _, job := range all {
		if job.Session == session {
			out = append(out, job)
		}
	}
	return out, nil
}

// Get 按 id 返回一个作业。
func (r *Registry) Get(id string) (Job, error) {
	r.mu.Lock()
	// 刷新只把 registry 标脏；这里不发生磁盘写入。
	r.refreshLocked()
	job, ok := r.jobs[id]
	// 复制快照前让任何进行中的写入落地（见 List）。
	for r.inflight > 0 {
		r.idleCond.Wait()
	}
	// Get 按 id 返回一个作业。刷新只把 registry 标脏；这里不发生磁盘写入。复制快照前让任何进行中的写入落地（见 List）。Cond.Wait 释放了 mu，因此观察者可能已更新（或并发调用方可能已删除）此作业。现在重新读取 map 并复制，使返回值匹配已确定的持久化状态。
	// 可能已更新或移除此作业。现在重新读取 map 并复制，使
	// 返回值匹配已确定的持久化状态。
	job, ok = r.jobs[id]
	// LRU：仅在启用 MaxEntries 时记录本次访问（见 List）。
	if ok {
		r.noteAccessLocked(id)
	}
	var out Job
	if ok {
		out = *job
	}
	r.mu.Unlock()
	if !ok {
		return Job{}, fmt.Errorf("jobs: no job named %q", id)
	}
	return out, nil
}

// Stop 杀死一个作业及其后代。
//
// 从磁盘重载的作业没有活句柄，因此它对子进程的唯一引用
// 是原始 pid。pid 会被回收：崩溃后该号码可能现在
// 属于无关进程。这样的作业在发信号前必须按其
// 创建时间重新识别。活句柄是权威的（它指向我们启动的确切子进程），因此
// Stop 杀死一个作业及其后代。从磁盘重载的作业没有活句柄，因此它对子进程的唯一引用是原始 pid。pid 会被回收：崩溃后该号码可能现在属于无关进程。这样的作业在发信号前必须按其创建时间重新识别。活句柄是权威的（它指向我们启动的确切子进程），因此那条路径无需 pid 检查。
func (r *Registry) Stop(id string) error {
	r.mu.Lock()
	job, ok := r.jobs[id]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("jobs: no job named %q", id)
	}
	if job.Status != StatusRunning {
		r.mu.Unlock()
		return nil
	}
	pid := job.PID
	startedAt := job.StartedAt
	live := job.live
	if live == nil && !sameProcess(pid, startedAt) {
		// 被观察作业的终态和真实退出码属于其
		// 被观察作业的终态和真实退出码属于其进程内 reaper/watcher。死的或被回收的 pid 绝不能在这里被发布为 Exited/-1 —— 那是 refreshLocked 已为被观察作业避免的同样过早终态竞态。让作业保持运行，由观察者记录真实代码；我们只拒绝给无法识别的 pid 发信号。
		// 进程内观察者。死的或被回收的 pid 绝不能在这里被发布为 Exited/-1 —— 那是
		// refreshLocked 已为被观察作业避免的同样过早终态竞态。让作业
		// 保持运行，由观察者记录真实代码；我们只拒绝给
		// 无法识别的 pid 发信号。
		if job.watched {
			r.mu.Unlock()
			return fmt.Errorf("%w (job %s, pid %d); nothing was killed", errProcessIdentity, id, pid)
		}
		markStale(job)
		snap, version := r.changedLocked()
		r.mu.Unlock()
		_ = r.persist(snap, version)
		return fmt.Errorf("%w (job %s, pid %d); nothing was killed", errProcessIdentity, id, pid)
	}
	job.Status = StatusKilled
	snap, version := r.changedLocked()
	r.mu.Unlock()
	_ = r.persist(snap, version)

	if live != nil {
		live.Kill()
	} else {
		killTree(pid)
	}
	return nil
}

// StopAll 给每个运行中的作业发信号并等待其完成观察者
// 回收它。Stop 本身只给子进程发信号；Start 派生的 reaper 和
// PromoteFromTimeout 派生的 watcher 稍后记录真实退出。在这里等待
// 意味着 StopAll 返回 nil 错误时，没有被观察的作业仍在
// 被回收。
//
// 等待受 ctx 限制：传入带超时的 context 以限制总
// 耗时。每个作业都在等待开始前被发信号，因此过期的 ctx
// 绝不会让运行中的作业未被发信号 —— 只意味着观察者没有
// 及时完成。返回的错误在截止时间触发时把每个单独的 Stop 失败与 ctx.Err() 合并。
//
// StopAll 是幂等的且可安全地与读者及其他 StopAll 调用并发。
// 它用于显式拆卸：调用方不得在其运行时启动新作业，因为等待覆盖到目前为止启动的
// 每个观察者。空（或已完全停止）的 registry 是空操作。
// 空（或已完全停止）的 registry 是空操作。
func (r *Registry) StopAll(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	// 在 mu 下快照：Stop 修改状态且不能在 mu 下调用。
	r.mu.Lock()
	ids := make([]string, 0, len(r.jobs))
	for id, job := range r.jobs {
		if job.Status == StatusRunning {
			ids = append(ids, id)
		}
	}
	r.mu.Unlock()

	var errs []error
	for _, id := range ids {
		if err := r.Stop(id); err != nil {
			errs = append(errs, err)
		}
	}

	// waitIdle 没有超时，因此在 goroutine 上驱动它并用 ctx 限制此调用。
	// StopAll 是幂等的且可安全地与读者及其他 StopAll 调用并发。它用于显式拆卸：调用方不得在其运行时启动新作业，因为等待覆盖到目前为止启动的每个观察者。空（或已完全停止）的 registry 是空操作。在 mu 下快照：Stop 修改状态且不能在 mu 下调用。waitIdle 没有超时，因此在 goroutine 上驱动它并用 ctx 限制此调用。作业全部来自磁盘的 registry 没有观察者，因此它立即返回。
	// 因此它立即返回。
	reaped := make(chan struct{})
	go func() {
		r.waitIdle()
		close(reaped)
	}()
	select {
	case <-reaped:
	case <-ctx.Done():
		errs = append(errs, ctx.Err())
	}
	return errors.Join(errs...)
}

// Tail 返回作业日志的最后 n 行。
func (r *Registry) Tail(id string, n int) (string, error) {
	job, err := r.Get(id)
	if err != nil {
		return "", err
	}
	return tailFile(job.LogPath, n)
}

// Remove 忘记一个作业并删除其日志。
//
// Tail 返回作业日志的最后 n 行。Remove 忘记一个作业并删除其日志。与 Stop 一样，重载作业的原始 pid 在发信号前被重新识别。不匹配时不杀任何东西：作业被退役，错误要求调用方在陈旧状态确定后再次移除。与 Stop 一样，被观察作业的终态属于其进程内观察者：在这里以 -1 退役它会发布过早的终态快照并隐藏观察者即将记录的真实退出码。
// 发信号前被重新识别。不匹配时不杀任何东西：作业被退役，
// 错误要求调用方在陈旧状态确定后再次移除。
func (r *Registry) Remove(id string) error {
	r.mu.Lock()
	job, ok := r.jobs[id]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("jobs: no job named %q", id)
	}
	running := job.Status == StatusRunning
	pid := job.PID
	startedAt := job.StartedAt
	live := job.live
	logPath := job.LogPath
	if running && live == nil && !sameProcess(pid, startedAt) {
		// 与 Stop 一样，被观察作业的终态属于其进程内
		// 观察者：在这里以 -1 退役它会发布过早的终态
		// 快照并隐藏观察者即将记录的真实退出码。
		if job.watched {
			r.mu.Unlock()
			return fmt.Errorf("%w (job %s, pid %d); nothing was killed, job kept", errProcessIdentity, id, pid)
		}
		markStale(job)
		snap, version := r.changedLocked()
		r.mu.Unlock()
		_ = r.persist(snap, version)
		return fmt.Errorf("%w (job %s, pid %d); nothing was killed, job kept", errProcessIdentity, id, pid)
	}
	delete(r.jobs, id)
	r.forgetAccessLocked(id)
	snap, version := r.changedLocked()
	r.mu.Unlock()
	_ = r.persist(snap, version)

	if running {
		if live != nil {
			live.Kill()
		} else {
			killTree(pid)
		}
	}
	if logPath != "" {
		if err := os.Remove(logPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("jobs: remove %s: %w", logPath, err)
		}
	}
	return nil
}

// Prune 忘记启动时间超过 age 的已完成作业。它们的日志留在磁盘上：日志是作业所做之事的唯一痕迹。age 为零时忘记所有已完成的作业。
//
// 它们的日志留在磁盘上：日志是作业所做之事的唯一痕迹。age 为零
// 时忘记所有已完成的作业。
func (r *Registry) Prune(age time.Duration) (int, error) {
	cutoff := time.Now().Add(-age)
	r.mu.Lock()
	removed := 0
	for id, job := range r.jobs {
		if job.Status == StatusRunning {
			continue
		}
		if age > 0 && job.StartedAt.After(cutoff) {
			continue
		}
		delete(r.jobs, id)
		r.forgetAccessLocked(id)
		removed++
	}
	var snap index
	var version uint64
	if removed > 0 {
		snap, version = r.changedLocked()
	}
	r.mu.Unlock()
	if removed > 0 {
		if err := r.persist(snap, version); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

// refreshLocked 把进程消失的作业翻转为已退出。它报告
// 是否有任何变化。我们无法恢复的退出码保持 -1。
//
// 刷新与任何其他状态变化一样：它增加版本号并把 registry 标脏。读路径从不自己写入索引 —— 下一次
// registry 标脏。读路径从不自己写入索引 —— 下一次
// 写入路径或 reaper 持久化完整快照。
func (r *Registry) refreshLocked() bool {
	changed := false
	for _, job := range r.jobs {
		if job.Status != StatusRunning {
			continue
		}
		// refreshLocked 把进程消失的作业翻转为已退出。它报告是否有任何变化。我们无法恢复的退出码保持 -1。刷新与任何其他状态变化一样：它增加版本号并把 registry 标脏。读路径从不自己写入索引 —— 下一次写入路径或 reaper 持久化完整快照。
		// 带进程内观察者的作业（Start reaper、提升 watcher）拥有自己的退出码。仅死的 pid
		// 不得在该观察者记录真实代码之前发布终态 Exited/-1 快照。
		if job.live != nil || job.watched {
			continue
		}
		// 存活的 pid 不是证据：registry 重载后它可能已被 OS 回收。
		// 还要求创建时间匹配。
		if sameProcess(job.PID, job.StartedAt) {
			continue
		}
		markStale(job)
		changed = true
	}
	if changed {
		r.noteChangeLocked()
	}
	return changed
}

// allocateID 返回一个在 registry 内唯一的 id。
//
// allocateID 返回一个在 registry 内唯一的 id。空会话保留旧版全局 "job%d" 序列，因此 Start 和 PromoteFromTimeout 的行为完全不变。具名会话使用单独的计数器和 "@session" 后缀；数字部分永不与旧序列重叠，这使即使在会话作业存在之后，普通 Start 的 id 也保持稳定。碰撞循环仍防止复用较旧索引遗留的 id。
// PromoteFromTimeout 的行为完全不变。具名会话使用单独的
// 计数器和 "@session" 后缀；数字部分永不与旧序列重叠，
// 这使即使在会话作业存在之后，普通 Start 的 id 也保持稳定。
// 碰撞循环仍防止复用较旧索引遗留的 id。
// id。
func (r *Registry) allocateID(session string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		var id string
		if session == "" {
			r.next++
			id = fmt.Sprintf("job%d", r.next)
		} else {
			r.sessionNext++
			id = fmt.Sprintf("job%d@%s", r.sessionNext, idToken(session))
		}
		if _, taken := r.jobs[id]; !taken {
			return id
		}
	}
}

// idToken 把会话名缩减为文件名安全的字符，
// 因为 id 也是作业日志的基础文件名。数字前缀
// 保证唯一性，所以映射到相同 token 的两个会话不会
// 产生相同 id。
func idToken(session string) string {
	token := strings.Map(func(ch rune) rune {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9',
			ch == '-', ch == '_', ch == '.':
			return ch
		default:
			return '_'
		}
	}, session)
	if token == "" {
		return "_"
	}
	return token
}

// index 是磁盘形态。SessionNext 是附加的：会话支持之前写出的索引没有该键且解码为零，这没问题因为那时还不存在会话作用域的 id。
// 会话支持之前写出的索引没有该键且解码为零，这没问题因为
// 那时还不存在会话作用域的 id。
type index struct {
	Next        int   `json:"next"`
	SessionNext int   `json:"sessionNext,omitempty"`
	Jobs        []Job `json:"jobs"`
}

func (r *Registry) indexPath() string { return filepath.Join(r.dir, storeFile) }

func (r *Registry) load() error {
	data, err := os.ReadFile(r.indexPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("jobs: read %s: %w", r.indexPath(), err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	var stored index
	if err := json.Unmarshal(data, &stored); err != nil {
		return fmt.Errorf("jobs: parse %s: %w", r.indexPath(), err)
	}
	for i := range stored.Jobs {
		job := stored.Jobs[i]
		// 从磁盘读回的作业没有活观察者也没有 pid 能力。
		// 把任何 pid 已消失或现在属于不同进程的"运行中"条目退役，
		// 从磁盘读回的作业没有活观察者也没有 pid 能力。把任何 pid 已消失或现在属于不同进程的"运行中"条目退役，使读者绝不会把被回收的 pid 误认为原始子进程。这里不发生磁盘写入；修正后的状态由下一次写入路径拾取。
		// 原始子进程。这里不发生磁盘写入；修正后的状态
		// 由下一次写入路径拾取。
		if job.Status == StatusRunning && !sameProcess(job.PID, job.StartedAt) {
			markStale(&job)
		}
		r.jobs[job.ID] = &job
	}
	r.next = stored.Next
	r.sessionNext = stored.SessionNext
	return nil
}

// markStale 退役其进程已消失或无法再可靠识别的作业。
// markStale 退役其进程已消失或无法再可靠识别的作业。其真实退出码无法恢复，因此保持 -1。
func markStale(job *Job) {
	job.Status = StatusExited
	job.ExitCode = -1
}

// noteChangeLocked 记录一次内存修改。调用方必须持有 mu。
func (r *Registry) noteChangeLocked() {
	r.version++
	r.dirty = true
}

// changedLocked 记录一次修改并返回要持久化的快照及其所代表的
// 版本。调用方必须持有 mu 且必须在解锁后调用 persist；
// changedLocked 记录一次修改并返回要持久化的快照及其所代表的 URL 版本。调用方必须持有 mu 且必须在解锁后调用 persist；持有 mu 时不发生磁盘 I/O。它标记一次写入进行中，使读者能等待它到达磁盘。
// 使读者能等待它到达磁盘。
func (r *Registry) changedLocked() (index, uint64) {
	r.noteChangeLocked()
	r.inflight++
	return r.snapshotLocked(), r.version
}

// finishWrite 平衡一次 changedLocked 调用。它由 persist 延迟调用，使进行中计数在所有返回路径（包括错误）上都被清除。
// 进行中计数在所有返回路径（包括错误）上都被清除。
func (r *Registry) finishWrite() {
	r.mu.Lock()
	r.inflight--
	if r.inflight == 0 {
		r.idleCond.Broadcast()
	}
	r.mu.Unlock()
}

// snapshotLocked 复制 registry 状态以供持久化。调用方必须持有 mu。
func (r *Registry) snapshotLocked() index {
	stored := index{Next: r.next, SessionNext: r.sessionNext, Jobs: make([]Job, 0, len(r.jobs))}
	for _, job := range r.jobs {
		stored.Jobs = append(stored.Jobs, *job)
	}
	sort.Slice(stored.Jobs, func(i, j int) bool { return stored.Jobs[i].ID < stored.Jobs[j].ID })
	return stored
}

// persist 把带版本的快照原子写入磁盘。编码和文件
// persist 把带版本的快照原子写入磁盘。编码和文件 I/O 在 ioMu 下进行但释放 mu，因此它们从不阻塞读者或其他内存状态的写入者。被更新版本超越的快照被跳过：较新的快照由其自己的调用方写入。
// I/O 在 ioMu 下进行但释放 mu，因此它们从不阻塞读者或其他内存状态的写入者。被更新版本超越的快照被
// 跳过：较新的快照由其自己的调用方写入。
func (r *Registry) persist(stored index, version uint64) error {
	defer r.finishWrite()
	r.ioMu.Lock()
	defer r.ioMu.Unlock()

	r.mu.Lock()
	stale := r.persisted >= version
	r.mu.Unlock()
	if stale {
		return nil
	}

	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return fmt.Errorf("jobs: encode index: %w", err)
	}
	data = append(data, '\n')

	// 唯一的临时文件名避免破坏另一 registry 实例可能正在同一目录中写入的文件。
	// 实例可能正在同一目录中写入的文件。
	tmp, err := os.CreateTemp(r.dir, ".jobs-*.tmp")
	if err != nil {
		return fmt.Errorf("jobs: create temp index: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("jobs: write %s: %w", tmpName, err)
	}
	// 在 rename 前把数据刷到磁盘，避免掉电后目标文件为空或被截断。
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("jobs: sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("jobs: close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, r.indexPath()); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("jobs: replace %s: %w", r.indexPath(), err)
	}

	r.mu.Lock()
	if version > r.persisted {
		r.persisted = version
	}
	if r.version == version {
		r.dirty = false
	}
	r.mu.Unlock()
	return nil
}

// tailFile 读取日志的最后 n 行而不加载全部内容。窗口从一行中间开始；丢弃该片段。
func tailFile(path string, n int) (string, error) {
	if n <= 0 {
		n = DefaultTailLines
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("jobs: read %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	var data []byte
	if info, statErr := file.Stat(); statErr == nil && info.Size() > tailWindow {
		if _, err := file.Seek(-tailWindow, io.SeekEnd); err != nil {
			return "", fmt.Errorf("jobs: seek %s: %w", path, err)
		}
		data, err = io.ReadAll(io.LimitReader(file, tailWindow))
		if err != nil {
			return "", fmt.Errorf("jobs: read %s: %w", path, err)
		}
		// 窗口从一行中间开始；丢弃该片段。
		if cut := bytes.IndexByte(data, '\n'); cut >= 0 {
			data = data[cut+1:]
		}
	} else {
		data, err = io.ReadAll(file)
		if err != nil {
			return "", fmt.Errorf("jobs: read %s: %w", path, err)
		}
	}

	text := strings.TrimRight(string(data), "\r\n")
	if text == "" {
		return "", nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}
