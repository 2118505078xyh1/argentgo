package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 记录在 store 和每个运行日志中的运行状态。
const (
	StatusOK      = "ok"
	StatusFailed  = "failed"
	StatusTimeout = "timeout"
	StatusSkipped = "skipped"
)

// DefaultTick 是 daemon 查找到期任务的频率。
const DefaultTick = 30 * time.Second

// Executor 运行一个任务。daemon 提供任务并遵守其超时；
// Executor 运行一个任务。daemon 提供任务并遵守其超时；executor 只需把 spec 变成结果（在 CLI 中意味着
// 驱动 agent 循环，这里是一个注入的 seam）。
type Executor interface {
	Run(ctx context.Context, spec Spec) (RunResult, error)
}

// RunResult 是一次运行的结果。
type RunResult struct {
	ID       string
	Status   string
	Output   string
	Err      error
	Started  time.Time
	Finished time.Time
}

// Success 报告运行是否干净完成。
func (r RunResult) Success() bool {
	return r.Status == "" || r.Status == StatusOK
}

// Options 配置 daemon。
type Options struct {
	// Store 是任务列表（必需）。
	Store *Store
	// Executor 运行到期任务（必需）。
	Executor Executor
	// LogDir 接收 <id>/<epoch>-<seq>.log 及匹配的 .json。默认为 <store dir>/runs。
	// 默认为 <store dir>/runs。
	LogDir string
	// Clock 覆盖 time.Now；测试用它驱动调度。
	Clock func() time.Time
	// Tick 是轮询间隔；默认为 DefaultTick。
	Tick time.Duration
}

// daemon 是正在运行的调度器。
type daemon struct {
	store  *Store
	exec   Executor
	logDir string
	now    func() time.Time
	tick   time.Duration
	// daemon 是正在运行的调度器。
	// startTime 是该 daemon 启动的时刻。在此之前就已到期的时隙在本进程中没有机会运行，因此 missed=skip 可能
	// 丢弃它；在我们运行期间到期的时隙是准时的（tick 很粗）且
	// 丢弃它；在我们运行期间到期的时隙是准时的（tick 很粗）且必须执行。
	startTime time.Time

	wg sync.WaitGroup
}

func newDaemon(opts Options) (*daemon, error) {
	if opts.Store == nil {
		return nil, errors.New("automation: daemon needs a store")
	}
	if opts.Executor == nil {
		return nil, errors.New("automation: daemon needs an executor")
	}
	d := &daemon{
		store:  opts.Store,
		exec:   opts.Executor,
		logDir: opts.LogDir,
		now:    opts.Clock,
		tick:   opts.Tick,
	}
	if d.now == nil {
		d.now = time.Now
	}
	d.startTime = d.now()
	if d.tick <= 0 {
		d.tick = DefaultTick
	}
	if d.logDir == "" {
		d.logDir = filepath.Join(opts.Store.Dir(), "runs")
	}
	return d, nil
}

// Run 轮询 store 直到 ctx 被取消并在关闭时返回 nil。
//
// 立即执行一次扫描，因此在某时隙被错过之后启动的 daemon 会立即应用该任务的 missed 策略。每个任务在其自身
// 立即应用该任务的 missed 策略。每个任务在其自身
// goroutine 中运行且每个 id 至多一个运行在进行中；下一个 tick 到来时仍在运行的任务就保持不动。
// goroutine 中运行且每个 id 至多一个运行在进行中；下一个 tick 到来时仍在
func Run(ctx context.Context, opts Options) error {
	d, err := newDaemon(opts)
	if err != nil {
		return err
	}
	ticker := time.NewTicker(d.tick)
	defer ticker.Stop()
	for {
		d.scan(ctx)
		select {
		case <-ctx.Done():
			d.wg.Wait()
			return nil
		case <-ticker.C:
		}
	}
}

// scan 启动所有到期且未运行的任务。
func (d *daemon) scan(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	// 任务列表是共享 JSON 文件。自我们上次扫描以来，另一个 `automation run` 进程可能处理了某个时隙并推进了 NextDue。先
	// 自我们上次扫描以来可能处理了某个时隙并推进了 NextDue。先
	// 刷新，使陈旧的内存视图不会再次触发该时隙。若文件无法读取，等待
	// 刷新，使陈旧的内存视图不会再次触发该时隙。若文件无法读取，等待下一个 tick，而不是根据可能重复运行的陈旧
	// 数据行动。
	if err := d.store.Reload(); err != nil {
		return
	}
	now := d.now()
	for _, spec := range d.store.List() {
		if !spec.IsDue(now) {
			continue
		}
		if spec.MissedPolicy() == MissedSkip && spec.NextDue.Before(d.startTime) {
			// 数据行动。
			// 该时隙在本进程启动前就已过去，因此没有运行它的机会：丢弃该时隙并等待下一个。
			if _, err := d.store.claim(spec.ID); err == nil {
				_ = d.store.recordRun(spec.ID, StatusSkipped, "", runTime(spec.NextDue, now), spec.NextDue)
				d.store.release(spec.ID)
			}
			continue
		}
		seq, err := d.store.claim(spec.ID)
		if err != nil {
			// 此处或另一个进程中已在运行（例如 RunNow）。
			continue
		}
		d.wg.Add(1)
		go d.execute(ctx, spec, seq)
	}
}

// runTime 是为某次运行记录的时间戳：当前时间，绝不会早于
// runTime 是为某次运行记录的时间戳：当前时间，绝不会早于触发它的时隙。这防止 LastRun 在执行或跳过陈旧时隙时
// 向后移动。
func runTime(slot, current time.Time) time.Time {
	if slot.After(current) {
		return slot
	}
	return current
}

// execute 运行一个任务、写入其日志并记录结果。
func (d *daemon) execute(ctx context.Context, spec Spec, seq uint64) {
	defer d.wg.Done()
	defer d.store.release(spec.ID)

	// 捕获扫描认领的时隙。下一次触发由此刻的时隙派生，
	// 捕获扫描认领的时隙。下一次触发由此刻的时隙派生，而非完成时间，使慢任务不能把整个
	// 调度表向前推（12:00 + 10m 仍是 12:10，即使运行在 12:02 结束）。
	slot := spec.NextDue
	started := d.now()
	runCtx := ctx
	if timeout := spec.Timeout.Std(); timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	res, err := d.exec.Run(runCtx, spec)
	finished := d.now()

	res.ID = spec.ID
	res.Started = started
	res.Finished = finished
	res.Err = err
	if res.Status == "" {
		switch {
		case err == nil:
			res.Status = StatusOK
		case errors.Is(err, context.DeadlineExceeded):
			res.Status = StatusTimeout
		default:
			res.Status = StatusFailed
		}
	}
	logPath, _ := d.writeLogs(spec, res, seq)
	// 运行中被删除的 spec 无可记录；这不是错误。
	_ = d.store.recordRun(spec.ID, res.Status, logPath, runTime(slot, finished), slot)
}

// wait 阻塞直到进行中的运行完成。
func (d *daemon) wait() { d.wg.Wait() }

// runRecord 是运行日志的 JSON 侧。
type runRecord struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Schedule   Schedule  `json:"schedule"`
	Status     string    `json:"status"`
	Started    time.Time `json:"started"`
	Finished   time.Time `json:"finished"`
	DurationMS int64     `json:"durationMs"`
	Log        string    `json:"log"`
	Error      string    `json:"error,omitempty"`
}

// maxRunLogs 是每个任务保留的运行对（.log + .json）数量。较旧的运行在每次写入后被裁剪，使 runs/<id>/ 不会无界增长。
// 较旧的运行在每次写入后被裁剪，使 runs/<id>/ 不会无界增长。
const maxRunLogs = 20

// writeLogs 写入 <logDir>/<id>/<epoch>-<seq>.log 及匹配的 .json
// 记录并返回日志路径。seq 来自 store 的认领，因此同一秒开始的两个运行仍获得不同的有序名称。
// 同一秒开始的两个运行仍获得不同的有序名称。
func (d *daemon) writeLogs(spec Spec, res RunResult, seq uint64) (string, error) {
	dir := filepath.Join(d.logDir, spec.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	epoch := res.Started.Unix()
	if res.Started.IsZero() {
		epoch = d.now().Unix()
	}
	base := filepath.Join(dir, fmt.Sprintf("%d-%012d", epoch, seq))
	logPath := base + ".log"

	var body []byte
	body = append(body, fmt.Sprintf("# automation %s\n# schedule %s\n# started %s\n# status %s\n\n",
		spec.ID, spec.Schedule.String(), res.Started.Format(time.RFC3339), res.Status)...)
	body = append(body, res.Output...)
	if res.Output != "" && res.Output[len(res.Output)-1] != '\n' {
		body = append(body, '\n')
	}
	if res.Err != nil {
		body = append(body, fmt.Sprintf("\n--- error ---\n%s\n", res.Err.Error())...)
	}
	if err := writeAtomic(logPath, body); err != nil {
		return "", err
	}

	record := runRecord{
		ID:         spec.ID,
		Name:       spec.Name,
		Schedule:   spec.Schedule,
		Status:     res.Status,
		Started:    res.Started,
		Finished:   res.Finished,
		DurationMS: res.Finished.Sub(res.Started).Milliseconds(),
		Log:        logPath,
	}
	if res.Err != nil {
		record.Error = res.Err.Error()
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return logPath, err
	}
	if err := writeAtomic(base+".json", append(data, '\n')); err != nil {
		return logPath, err
	}
	rotateLogs(dir, maxRunLogs)
	return logPath, nil
}

// rotateLogs 只保留 dir 中最新的 keep 个运行对。名称格式为 "<epoch>-<补零的 seq>"，因此字典序即时间序。
// 名称格式，因此字典序即时间序。
func rotateLogs(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	byBase := make(map[string][]string)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		var base string
		switch {
		case strings.HasSuffix(name, ".log"):
			base = strings.TrimSuffix(name, ".log")
		case strings.HasSuffix(name, ".json"):
			base = strings.TrimSuffix(name, ".json")
		default:
			continue
		}
		byBase[base] = append(byBase[base], filepath.Join(dir, name))
	}
	if len(byBase) <= keep {
		return
	}
	bases := make([]string, 0, len(byBase))
	for base := range byBase {
		bases = append(bases, base)
	}
	sort.Strings(bases)
	for _, base := range bases[:len(bases)-keep] {
		for _, path := range byBase[base] {
			_ = os.Remove(path)
		}
	}
}

// RunNow 在 daemon 循环之外立即运行一个存储的任务并记录
// RunNow 在 daemon 循环之外立即运行一个存储的任务并记录结果。CLI 用它实现 `automation run <id>`。
func RunNow(ctx context.Context, store *Store, ex Executor, logDir, id string, at time.Time) (RunResult, error) {
	if store == nil {
		return RunResult{}, errors.New("automation: RunNow needs a store")
	}
	if ex == nil {
		return RunResult{}, errors.New("automation: RunNow needs an executor")
	}
	spec, ok := store.Get(id)
	if !ok {
		return RunResult{}, fmt.Errorf("automation: no task %q", id)
	}
	// 通过与 daemon 相同的 store 级机制认领，使
	// 通过与 daemon 相同的 store 级机制认领，使手动运行对同一 id 绝不可能与 daemon 运行（或另一个 RunNow）重叠，
	// 即使它们位于不同进程中。
	seq, err := store.claim(id)
	if err != nil {
		if errors.Is(err, errBusy) {
			return RunResult{}, fmt.Errorf("automation: task %q is already running", id)
		}
		return RunResult{}, fmt.Errorf("automation: claim task %q: %w", id, err)
	}
	defer store.release(id)
	d := &daemon{store: store, exec: ex, logDir: logDir, now: time.Now}
	if d.logDir == "" {
		d.logDir = filepath.Join(store.Dir(), "runs")
	}
	if at.IsZero() {
		at = time.Now()
	}
	runCtx := ctx
	if timeout := spec.Timeout.Std(); timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	res, err := ex.Run(runCtx, spec)
	res.ID = spec.ID
	res.Started = at
	res.Finished = time.Now()
	res.Err = err
	if res.Status == "" {
		switch {
		case err == nil:
			res.Status = StatusOK
		case errors.Is(err, context.DeadlineExceeded):
			res.Status = StatusTimeout
		default:
			res.Status = StatusFailed
		}
	}
	logPath, _ := d.writeLogs(spec, res, seq)
	if recordErr := store.RecordRun(spec.ID, res.Status, logPath, res.Finished); recordErr != nil {
		return res, recordErr
	}
	return res, nil
}
