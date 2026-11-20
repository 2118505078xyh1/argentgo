package jobs

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// evictionJob 构造一个只带淘汰决策所需字段的作业视图。
func evictionJob(id string, status Status, started time.Time) JobInfo {
	return JobInfo{ID: id, Status: status, StartedAt: started}
}

func TestSelectEvictionCandidatesUnderLimitReturnsNone(t *testing.T) {
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	jobs := []JobInfo{
		evictionJob("a", StatusRunning, base),
		evictionJob("b", StatusExited, base.Add(time.Hour)),
		evictionJob("c", StatusKilled, base.Add(2*time.Hour)),
	}
	if got := selectEvictionCandidates(jobs, 5); len(got) != 0 {
		t.Fatalf("got %v, want no candidates", got)
	}
	if got := selectEvictionCandidates(jobs, 3); len(got) != 0 {
		t.Fatalf("got %v, want no candidates at exact limit", got)
	}
}

func TestSelectEvictionCandidatesOverLimitSelectsOldest(t *testing.T) {
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	jobs := []JobInfo{
		evictionJob("j1", StatusRunning, base),
		evictionJob("j2", StatusRunning, base.Add(time.Hour)),
		evictionJob("j3", StatusRunning, base.Add(2*time.Hour)),
		evictionJob("j4", StatusRunning, base.Add(3*time.Hour)),
	}
	got := selectEvictionCandidates(jobs, 2)
	want := []string{"j1", "j2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSelectEvictionCandidatesPrefersTerminalJobs(t *testing.T) {
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	jobs := []JobInfo{
		evictionJob("old-running", StatusRunning, base),
		evictionJob("new-exited", StatusExited, base.Add(3*time.Hour)),
		evictionJob("mid-killed", StatusKilled, base.Add(2*time.Hour)),
		evictionJob("new-running", StatusRunning, base.Add(4*time.Hour)),
	}

	// 超限一个时先淘汰已结束的作业，即使有更老的运行中作业。
	if got, want := selectEvictionCandidates(jobs, 3), []string{"mid-killed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("over=1: got %v, want %v", got, want)
	}
	// 超限两个时两个已结束的作业都排在前面，且按启动时间排序。
	if got, want := selectEvictionCandidates(jobs, 2), []string{"mid-killed", "new-exited"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("over=2: got %v, want %v", got, want)
	}
}

func TestSelectEvictionCandidatesLRUOrderWithinClass(t *testing.T) {
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	jobs := []JobInfo{
		evictionJob("newest", StatusExited, base.Add(4*time.Hour)),
		evictionJob("oldest", StatusExited, base),
		evictionJob("middle", StatusExited, base.Add(2*time.Hour)),
	}
	got := selectEvictionCandidates(jobs, 1)
	want := []string{"oldest", "middle"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSelectEvictionCandidatesEmpty(t *testing.T) {
	if got := selectEvictionCandidates(nil, 3); len(got) != 0 {
		t.Fatalf("nil jobs: got %v, want none", got)
	}
	if got := selectEvictionCandidates([]JobInfo{}, 0); len(got) != 0 {
		t.Fatalf("empty jobs: got %v, want none", got)
	}
}

func TestSelectEvictionCandidatesCJKNames(t *testing.T) {
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	jobs := []JobInfo{
		evictionJob("作业-北京", StatusRunning, base.Add(2*time.Hour)),
		evictionJob("作业-上海", StatusExited, base),
		evictionJob("作业-广州", StatusExited, base.Add(time.Hour)),
	}

	if got, want := selectEvictionCandidates(jobs, 2), []string{"作业-上海"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("over=1: got %v, want %v", got, want)
	}
	if got, want := selectEvictionCandidates(jobs, 1), []string{"作业-上海", "作业-广州"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("over=2: got %v, want %v", got, want)
	}
	if got, want := selectEvictionCandidates(jobs, 0), []string{"作业-上海", "作业-广州", "作业-北京"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("max=0: got %v, want %v", got, want)
	}
}

func TestSelectEvictionCandidatesNegativeMaxClampsToAll(t *testing.T) {
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	jobs := []JobInfo{
		evictionJob("a", StatusRunning, base),
		evictionJob("b", StatusExited, base.Add(time.Hour)),
	}
	got := selectEvictionCandidates(jobs, -5)
	if len(got) != len(jobs) {
		t.Fatalf("got %d candidates, want %d", len(got), len(jobs))
	}
}

func TestSelectEvictionCandidatesStableWithEqualTimes(t *testing.T) {
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	jobs := []JobInfo{
		evictionJob("b", StatusExited, base),
		evictionJob("a", StatusExited, base),
	}
	got := selectEvictionCandidates(jobs, 1)
	want := []string{"a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSelectEvictionCandidatesDoesNotMutateInput(t *testing.T) {
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	jobs := []JobInfo{
		evictionJob("c", StatusRunning, base.Add(2*time.Hour)),
		evictionJob("a", StatusExited, base),
		evictionJob("b", StatusKilled, base.Add(time.Hour)),
	}
	original := append([]JobInfo(nil), jobs...)
	_ = selectEvictionCandidates(jobs, 1)
	if !reflect.DeepEqual(jobs, original) {
		t.Fatalf("input was mutated: got %v, want %v", jobs, original)
	}
}

func TestEnforceMaxJobsDisabledByDefault(t *testing.T) {
	registry := newRegistry(t)
	base := time.Now().Add(-time.Hour)
	registry.jobs["a"] = &Job{ID: "a", Status: StatusExited, StartedAt: base}
	registry.jobs["b"] = &Job{ID: "b", Status: StatusExited, StartedAt: base.Add(time.Minute)}
	registry.jobs["c"] = &Job{ID: "c", Status: StatusExited, StartedAt: base.Add(2 * time.Minute)}

	if got := registry.EnforceMaxJobs(); got != nil {
		t.Fatalf("got %v, want no eviction when disabled", got)
	}
	if len(registry.jobs) != 3 {
		t.Fatalf("job count = %d, want 3", len(registry.jobs))
	}
}

func TestEnforceMaxJobsEvictsOldestTerminalFirst(t *testing.T) {
	registry := newRegistry(t)
	base := time.Now().Add(-time.Hour)
	registry.jobs["old-exited"] = &Job{ID: "old-exited", Status: StatusExited, StartedAt: base}
	registry.jobs["new-exited"] = &Job{ID: "new-exited", Status: StatusExited, StartedAt: base.Add(time.Hour)}
	registry.jobs["running"] = &Job{ID: "running", Status: StatusRunning, StartedAt: base.Add(2 * time.Hour)}

	registry.SetMaxJobs(2)
	got := registry.EnforceMaxJobs()
	want := []string{"old-exited"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("evicted %v, want %v", got, want)
	}
	if len(registry.jobs) != 2 {
		t.Fatalf("job count = %d, want 2", len(registry.jobs))
	}
	if _, ok := registry.jobs["old-exited"]; ok {
		t.Fatal("old-exited should have been evicted")
	}
}

func TestStartEnforcesMaxJobsWhenEnabled(t *testing.T) {
	registry := newRegistry(t)
	registry.SetMaxJobs(1)
	t.Cleanup(func() { _ = registry.StopAll(context.Background()) })

	var started []Job
	for i := 0; i < 3; i++ {
		job, err := registry.Start(longCommand())
		if err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		started = append(started, job)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		list, err := registry.List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(list) == 1 {
			if list[0].ID != started[2].ID {
				t.Fatalf("remaining job = %q, want %q", list[0].ID, started[2].ID)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job count = %d, want 1", len(list))
		}
		time.Sleep(20 * time.Millisecond)
	}
}
