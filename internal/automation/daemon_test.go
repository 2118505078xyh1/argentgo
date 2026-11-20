package automation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manually advanced clock.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(at time.Time) *fakeClock { return &fakeClock{now: at} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeExecutor records every call and can block or fail on demand.
type fakeExecutor struct {
	mu    sync.Mutex
	calls int
	specs []Spec
	ch    chan Spec
	run   func(ctx context.Context, spec Spec) (RunResult, error)
}

func (f *fakeExecutor) Run(ctx context.Context, spec Spec) (RunResult, error) {
	f.mu.Lock()
	f.calls++
	f.specs = append(f.specs, spec)
	f.mu.Unlock()
	if f.ch != nil {
		f.ch <- spec
	}
	if f.run != nil {
		return f.run(ctx, spec)
	}
	return RunResult{Status: StatusOK, Output: "done " + spec.ID}, nil
}

func (f *fakeExecutor) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func dueSpec(id string, nextDue time.Time) Spec {
	spec := testSpec(id)
	spec.NextDue = nextDue
	return spec
}

func TestDaemonRunsDueTask(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	now := mustTime(t, "2026-01-01T12:00:00Z")
	slot := now.Add(-time.Minute)
	if err := store.Put(dueSpec("nightly", slot)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	clock := newFakeClock(now)
	exec := &fakeExecutor{ch: make(chan Spec, 8)}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			Store:    store,
			Executor: exec,
			LogDir:   filepath.Join(dir, "logs"),
			Clock:    clock.Now,
			Tick:     2 * time.Millisecond,
		})
	}()

	select {
	case spec := <-exec.ch:
		if spec.ID != "nightly" {
			t.Fatalf("ran %q", spec.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon never ran the due task")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}

	spec, ok := store.Get("nightly")
	if !ok {
		t.Fatal("task vanished")
	}
	if spec.LastStatus != StatusOK {
		t.Fatalf("last status = %q", spec.LastStatus)
	}
	if spec.LastRun == nil || !spec.LastRun.Equal(now) {
		t.Fatalf("last run = %v", spec.LastRun)
	}
	// The slot was 11:59, so the next one is 12:59 — advancing from the
	// completion time (12:00) would have produced 13:00 and caused drift.
	if want := slot.Add(time.Hour); !spec.NextDue.Equal(want) {
		t.Fatalf("next due = %s, want %s", spec.NextDue, want)
	}
	base := filepath.Join(dir, "logs", "nightly", strconv.FormatInt(now.Unix(), 10))
	logData, err := os.ReadFile(base + ".log")
	if err != nil {
		t.Fatalf("read run log: %v", err)
	}
	if !contains(string(logData), "done nightly") {
		t.Fatalf("run log missing output:\n%s", logData)
	}
	record, err := os.ReadFile(base + ".json")
	if err != nil {
		t.Fatalf("read run record: %v", err)
	}
	var parsed runRecord
	if err := json.Unmarshal(record, &parsed); err != nil {
		t.Fatalf("run record is not JSON: %v\n%s", err, record)
	}
	if parsed.ID != "nightly" || parsed.Status != StatusOK {
		t.Fatalf("unexpected run record: %+v", parsed)
	}
}

func TestDaemonMissedSkipDropsTheSlot(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	now := mustTime(t, "2026-01-01T12:00:00Z")
	spec := dueSpec("nightly", now.Add(-2*time.Hour))
	spec.Missed = MissedSkip
	if err := store.Put(spec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	exec := &fakeExecutor{}
	d, err := newDaemon(Options{Store: store, Executor: exec, Clock: newFakeClock(now).Now})
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}
	d.scan(context.Background())
	d.wait()

	if exec.count() != 0 {
		t.Fatalf("missed=skip ran the task %d times", exec.count())
	}
	got, _ := store.Get("nightly")
	if got.LastStatus != StatusSkipped {
		t.Fatalf("last status = %q, want skipped", got.LastStatus)
	}
	if want := now.Add(time.Hour); !got.NextDue.Equal(want) {
		t.Fatalf("next due = %s, want %s (the skipped slot must not repeat)", got.NextDue, want)
	}
}

func TestDaemonSkipPolicyOnlyDropsSlotsMissedBeforeStart(t *testing.T) {
	start := mustTime(t, "2026-01-01T12:00:00Z")
	cases := []struct {
		name       string
		slot       time.Time
		advance    time.Duration
		wantCalls  int
		wantStatus string
		wantDue    time.Time
	}{
		{
			name:       "slot missed before start",
			slot:       start.Add(-2 * time.Hour),
			wantStatus: StatusSkipped,
			wantDue:    start.Add(time.Hour),
		},
		{
			name:       "slot due exactly at start",
			slot:       start,
			wantCalls:  1,
			wantStatus: StatusOK,
			wantDue:    start.Add(time.Hour),
		},
		{
			// With a 30s tick the daemon almost always sees a slot a few
			// seconds late. That is not "missed" and must still run.
			name:       "slot became due after start",
			slot:       start.Add(15 * time.Second),
			advance:    30 * time.Second,
			wantCalls:  1,
			wantStatus: StatusOK,
			wantDue:    start.Add(time.Hour).Add(15 * time.Second),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := OpenStore(t.TempDir())
			if err != nil {
				t.Fatalf("OpenStore: %v", err)
			}
			spec := dueSpec("nightly", tc.slot)
			spec.Missed = MissedSkip
			if err := store.Put(spec); err != nil {
				t.Fatalf("Put: %v", err)
			}
			clock := newFakeClock(start)
			exec := &fakeExecutor{}
			d, err := newDaemon(Options{Store: store, Executor: exec, Clock: clock.Now})
			if err != nil {
				t.Fatalf("newDaemon: %v", err)
			}
			clock.Advance(tc.advance)
			d.scan(context.Background())
			d.wait()

			if exec.count() != tc.wantCalls {
				t.Fatalf("executor called %d times, want %d", exec.count(), tc.wantCalls)
			}
			got, _ := store.Get("nightly")
			if got.LastStatus != tc.wantStatus {
				t.Fatalf("last status = %q, want %q", got.LastStatus, tc.wantStatus)
			}
			if !got.NextDue.Equal(tc.wantDue) {
				t.Fatalf("next due = %s, want %s", got.NextDue, tc.wantDue)
			}
		})
	}
}

func TestDaemonMissedRunOnceRunsOnce(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	now := mustTime(t, "2026-01-01T12:00:00Z")
	if err := store.Put(dueSpec("nightly", now.Add(-5*time.Hour))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	clock := newFakeClock(now)
	exec := &fakeExecutor{}
	d, err := newDaemon(Options{Store: store, Executor: exec, Clock: clock.Now})
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}
	d.scan(context.Background())
	d.wait()
	// A second scan with the same clock must find nothing left to do.
	d.scan(context.Background())
	d.wait()

	if exec.count() != 1 {
		t.Fatalf("ran %d times, want exactly one catch-up run", exec.count())
	}
	got, _ := store.Get("nightly")
	if got.LastStatus != StatusOK {
		t.Fatalf("last status = %q", got.LastStatus)
	}
	if want := now.Add(time.Hour); !got.NextDue.Equal(want) {
		t.Fatalf("next due = %s, want %s", got.NextDue, want)
	}
}

func TestDaemonSkipsDisabledTasks(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	now := mustTime(t, "2026-01-01T12:00:00Z")
	spec := dueSpec("nightly", now.Add(-time.Hour))
	spec.Enabled = false
	if err := store.Put(spec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	exec := &fakeExecutor{}
	d, err := newDaemon(Options{Store: store, Executor: exec, Clock: newFakeClock(now).Now})
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}
	d.scan(context.Background())
	d.wait()
	if exec.count() != 0 {
		t.Fatalf("a disabled task ran %d times", exec.count())
	}
	if got, _ := store.Get("nightly"); got.LastStatus != "" {
		t.Fatalf("a disabled task recorded status %q", got.LastStatus)
	}
}

func TestDaemonNeverRunsTheSameIdConcurrently(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	now := mustTime(t, "2026-01-01T12:00:00Z")
	if err := store.Put(dueSpec("nightly", now.Add(-time.Hour))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	exec := &fakeExecutor{run: func(ctx context.Context, spec Spec) (RunResult, error) {
		started <- struct{}{}
		<-release
		return RunResult{Status: StatusOK, Output: "slow"}, nil
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
	// The slot is still in the past, so a second scan would fire again if the
	// daemon did not track in-flight runs.
	d.scan(context.Background())
	close(release)
	d.wait()

	if exec.count() != 1 {
		t.Fatalf("the task ran %d times concurrently", exec.count())
	}
}

func TestDaemonRecordsTimeouts(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	now := mustTime(t, "2026-01-01T12:00:00Z")
	spec := dueSpec("slow", now.Add(-time.Hour))
	spec.Timeout = Duration(20 * time.Millisecond)
	if err := store.Put(spec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	exec := &fakeExecutor{run: func(ctx context.Context, spec Spec) (RunResult, error) {
		<-ctx.Done()
		return RunResult{}, ctx.Err()
	}}
	d, err := newDaemon(Options{Store: store, Executor: exec, Clock: newFakeClock(now).Now})
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}
	d.scan(context.Background())
	d.wait()

	if got, _ := store.Get("slow"); got.LastStatus != StatusTimeout {
		t.Fatalf("last status = %q, want timeout", got.LastStatus)
	}
}

func TestDaemonAdvancesCronSlots(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	now := mustTime(t, "2026-01-01T12:20:00Z")
	spec := dueSpec("quarter", now.Add(-time.Hour))
	spec.Schedule = Schedule{Kind: KindCron, Expr: "*/15 * * * *"}
	if err := store.Put(spec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	exec := &fakeExecutor{}
	d, err := newDaemon(Options{Store: store, Executor: exec, Clock: newFakeClock(now).Now})
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}
	d.scan(context.Background())
	d.wait()
	if exec.count() != 1 {
		t.Fatalf("ran %d times", exec.count())
	}
	got, _ := store.Get("quarter")
	if want := mustTime(t, "2026-01-01T12:30:00Z"); !got.NextDue.Equal(want) {
		t.Fatalf("next due = %s, want %s", got.NextDue, want)
	}
}

func TestDaemonAdvancesIntervalFromSlotNotFinish(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	now := mustTime(t, "2026-01-01T12:00:00Z")
	clock := newFakeClock(now)
	spec := dueSpec("quick", now)
	spec.Schedule = Schedule{Kind: KindInterval, Expr: "10m"}
	if err := store.Put(spec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	exec := &fakeExecutor{run: func(ctx context.Context, spec Spec) (RunResult, error) {
		clock.Advance(2 * time.Minute) // the task takes two minutes
		return RunResult{Status: StatusOK}, nil
	}}
	d, err := newDaemon(Options{Store: store, Executor: exec, Clock: clock.Now})
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}

	d.scan(context.Background())
	d.wait()
	got, _ := store.Get("quick")
	if want := now.Add(10 * time.Minute); !got.NextDue.Equal(want) {
		t.Fatalf("next due = %s, want %s (the slot, not the finish time)", got.NextDue, want)
	}
	if got.LastRun == nil || !got.LastRun.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("last run = %v, want the finish time", got.LastRun)
	}

	// A second slot must not inherit the first run's delay: 12:10 -> 12:20,
	// not 12:22. This is the long-term drift the probe reproduced.
	d.scan(context.Background()) // 12:02, nothing due yet
	d.wait()
	clock.Advance(8 * time.Minute) // 12:10
	d.scan(context.Background())
	d.wait()
	if exec.count() != 2 {
		t.Fatalf("executor called %d times, want 2", exec.count())
	}
	got, _ = store.Get("quick")
	if want := now.Add(20 * time.Minute); !got.NextDue.Equal(want) {
		t.Fatalf("next due = %s, want %s", got.NextDue, want)
	}
}

func TestDaemonLateRunResumesFromNow(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	now := mustTime(t, "2026-01-01T12:00:00Z")
	spec := dueSpec("catchup", now.Add(-2*time.Hour))
	spec.Schedule = Schedule{Kind: KindInterval, Expr: "10m"}
	if err := store.Put(spec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	exec := &fakeExecutor{}
	d, err := newDaemon(Options{Store: store, Executor: exec, Clock: newFakeClock(now).Now})
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}
	d.scan(context.Background())
	d.wait()

	// Advancing from the stale 10:00 slot alone would yield 10:10 and fire
	// again immediately; the catch-up must resume from "now".
	got, _ := store.Get("catchup")
	if want := now.Add(10 * time.Minute); !got.NextDue.Equal(want) {
		t.Fatalf("next due = %s, want %s", got.NextDue, want)
	}
	d.scan(context.Background())
	d.wait()
	if exec.count() != 1 {
		t.Fatalf("executor called %d times, want exactly one catch-up run", exec.count())
	}
}

func TestRunNowRunsImmediately(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	now := mustTime(t, "2026-01-01T12:00:00Z")
	spec := testSpec("nightly")
	spec.NextDue = now.Add(24 * time.Hour)
	if err := store.Put(spec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	exec := &fakeExecutor{}
	res, err := RunNow(context.Background(), store, exec, filepath.Join(dir, "logs"), "nightly", now)
	if err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	if res.Status != StatusOK || res.Output != "done nightly" {
		t.Fatalf("unexpected result: %+v", res)
	}
	got, _ := store.Get("nightly")
	if got.LastStatus != StatusOK {
		t.Fatalf("last status = %q", got.LastStatus)
	}
	if _, err := os.Stat(got.LastLog); err != nil {
		t.Fatalf("run log: %v", err)
	}
	if _, err := RunNow(context.Background(), store, exec, "", "missing", now); err == nil {
		t.Fatal("RunNow on a missing task should fail")
	}
}

func TestRunRequiresExecutorAndStore(t *testing.T) {
	if err := Run(context.Background(), Options{}); err == nil {
		t.Fatal("Run without a store should fail")
	}
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if err := Run(context.Background(), Options{Store: store}); err == nil {
		t.Fatal("Run without an executor should fail")
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
