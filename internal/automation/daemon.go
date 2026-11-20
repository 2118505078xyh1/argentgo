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

// Run statuses recorded in the store and in each run log.
const (
	StatusOK      = "ok"
	StatusFailed  = "failed"
	StatusTimeout = "timeout"
	StatusSkipped = "skipped"
)

// DefaultTick is how often the daemon looks for due tasks.
const DefaultTick = 30 * time.Second

// Executor runs one task. The daemon supplies the task and honours its timeout;
// the executor only has to turn a spec into a result (in the CLI that means
// driving the agent loop, here it is an injected seam).
type Executor interface {
	Run(ctx context.Context, spec Spec) (RunResult, error)
}

// RunResult is the outcome of one run.
type RunResult struct {
	ID       string
	Status   string
	Output   string
	Err      error
	Started  time.Time
	Finished time.Time
}

// Success reports whether the run finished cleanly.
func (r RunResult) Success() bool {
	return r.Status == "" || r.Status == StatusOK
}

// Options configures the daemon.
type Options struct {
	// Store is the task list (required).
	Store *Store
	// Executor runs due tasks (required).
	Executor Executor
	// LogDir receives <id>/<epoch>-<seq>.log and the matching .json. It
	// defaults to <store dir>/runs.
	LogDir string
	// Clock overrides time.Now; tests drive the schedule with it.
	Clock func() time.Time
	// Tick is the polling interval; it defaults to DefaultTick.
	Tick time.Duration
}

// daemon is the running scheduler.
type daemon struct {
	store  *Store
	exec   Executor
	logDir string
	now    func() time.Time
	tick   time.Duration
	// startTime is when this daemon came up. A slot that was already due
	// before then had no chance to run in this process, so missed=skip may
	// drop it; a slot that came due while we were running is on time (the
	// tick is coarse) and must be executed.
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

// Run polls the store until ctx is cancelled and returns nil on shutdown.
//
// One scan happens immediately, so a daemon that starts after a slot was missed
// applies the task's missed policy right away. Each task runs in its own
// goroutine and at most one run per id is in flight; a run that is still
// executing when the next tick arrives is simply left alone.
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

// scan starts every task that is due and not already running.
func (d *daemon) scan(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	// The task list is a shared JSON file. A separate `automation run` process
	// may have handled a slot and advanced NextDue since our last scan. Refresh
	// first so a stale in-memory view cannot fire that slot a second time. If
	// the file cannot be read, wait for the next tick rather than act on stale
	// data that might duplicate a run.
	if err := d.store.Reload(); err != nil {
		return
	}
	now := d.now()
	for _, spec := range d.store.List() {
		if !spec.IsDue(now) {
			continue
		}
		if spec.MissedPolicy() == MissedSkip && spec.NextDue.Before(d.startTime) {
			// The slot passed before this process started, so there was no
			// opportunity to run it: drop the slot and wait for the next one.
			if _, err := d.store.claim(spec.ID); err == nil {
				_ = d.store.recordRun(spec.ID, StatusSkipped, "", runTime(spec.NextDue, now), spec.NextDue)
				d.store.release(spec.ID)
			}
			continue
		}
		seq, err := d.store.claim(spec.ID)
		if err != nil {
			// Already in flight here or in another process (e.g. a RunNow).
			continue
		}
		d.wg.Add(1)
		go d.execute(ctx, spec, seq)
	}
}

// runTime is the timestamp recorded for a run: the current time, never earlier
// than the slot that triggered it. It keeps LastRun from moving backwards when
// a stale slot is executed or skipped.
func runTime(slot, current time.Time) time.Time {
	if slot.After(current) {
		return slot
	}
	return current
}

// execute runs one task, writes its logs and records the outcome.
func (d *daemon) execute(ctx context.Context, spec Spec, seq uint64) {
	defer d.wg.Done()
	defer d.store.release(spec.ID)

	// Capture the slot the scan claimed. The next firing is derived from this
	// slot, not from the completion time, so a slow task cannot push the whole
	// schedule forward (12:00 + 10m stays 12:10 even if the run ends at 12:02).
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
	// A spec deleted while running has nothing to record; that is not an error.
	_ = d.store.recordRun(spec.ID, res.Status, logPath, runTime(slot, finished), slot)
}

// wait blocks until in-flight runs finish.
func (d *daemon) wait() { d.wg.Wait() }

// runRecord is the JSON side of a run log.
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

// maxRunLogs is how many run pairs (.log + .json) are kept per task. Older
// runs are pruned after every write so runs/<id>/ does not grow without bound.
const maxRunLogs = 20

// writeLogs writes <logDir>/<id>/<epoch>-<seq>.log plus the matching .json
// record and returns the log path. seq comes from the store claim, so two runs
// that start in the same second still get distinct, ordered names.
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

// rotateLogs keeps only the newest keep run pairs in dir. Names are
// "<epoch>-<zero-padded seq>", so lexicographic order is chronological order.
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

// RunNow runs one stored task immediately, outside the daemon loop, and records
// the outcome. The CLI uses it for `automation run <id>`.
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
	// Claim through the same store-level mechanism the daemon uses, so a
	// manual run can never overlap a daemon run (or another RunNow) for the
	// same id, even when they live in different processes.
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
