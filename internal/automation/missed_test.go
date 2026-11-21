package automation

import (
	"context"
	"testing"
	"time"
)

// TestScheduleNextTrigger 表驱动验证错过补偿策略下的触发时隙与下次到期时间。
// 覆盖正常触发、三种策略、未知策略回退以及边界时间（时隙恰好等于 now、now 恰好落在时隙上、
// 时隙晚于 now）。默认 run_once 的路径必须与 recordRun 原有计算逐字节一致。
func TestScheduleNextTrigger(t *testing.T) {
	everyHour := Schedule{Kind: KindCron, Expr: "0 * * * *"}
	cases := []struct {
		name      string
		schedule  Schedule
		slot      string
		now       string
		policy    string
		wantTrigg string // 空表示本次不运行（trigger 为零时间）
		wantNext  string
	}{
		{
			// 正常触发：认领的时隙就是最近的时隙，各策略都从它继续。
			name:      "normal on time run_once",
			schedule:  everyHour,
			slot:      "2026-01-01T11:00:00Z",
			now:       "2026-01-01T11:00:30Z",
			policy:    MissedRunOnce,
			wantTrigg: "2026-01-01T11:00:00Z",
			wantNext:  "2026-01-01T12:00:00Z",
		},
		{
			name:      "normal on time skip",
			schedule:  everyHour,
			slot:      "2026-01-01T11:00:00Z",
			now:       "2026-01-01T11:00:30Z",
			policy:    MissedSkip,
			wantTrigg: "",
			wantNext:  "2026-01-01T12:00:00Z",
		},
		{
			name:      "normal on time run_latest",
			schedule:  everyHour,
			slot:      "2026-01-01T11:00:00Z",
			now:       "2026-01-01T11:00:30Z",
			policy:    MissedRunLatest,
			wantTrigg: "2026-01-01T11:00:00Z",
			wantNext:  "2026-01-01T12:00:00Z",
		},
		{
			// 默认策略：错过多个时隙只补一次，next 回退到 now 之后，避免每个 tick 重复触发。
			name:      "run_once with a long outage catches up once",
			schedule:  everyHour,
			slot:      "2026-01-01T08:00:00Z",
			now:       "2026-01-01T13:07:00Z",
			policy:    MissedRunOnce,
			wantTrigg: "2026-01-01T08:00:00Z",
			wantNext:  "2026-01-01T14:00:00Z",
		},
		{
			// skip：丢弃所有错过的时隙，本次不运行，直接等到 now 之后。
			name:      "skip with a long outage",
			schedule:  everyHour,
			slot:      "2026-01-01T08:00:00Z",
			now:       "2026-01-01T13:07:00Z",
			policy:    MissedSkip,
			wantTrigg: "",
			wantNext:  "2026-01-01T14:00:00Z",
		},
		{
			// run_latest：补偿到不晚于 now 的最近一个时隙（13:00），next 为 14:00。
			name:      "run_latest anchors to the most recent missed slot",
			schedule:  everyHour,
			slot:      "2026-01-01T08:00:00Z",
			now:       "2026-01-01T13:07:00Z",
			policy:    MissedRunLatest,
			wantTrigg: "2026-01-01T13:00:00Z",
			wantNext:  "2026-01-01T14:00:00Z",
		},
		{
			// 边界：now 恰好落在时隙上，该时隙视为已到期（与 IsDue 的 <= 一致）。
			name:      "now exactly on a slot run_latest",
			schedule:  everyHour,
			slot:      "2026-01-01T08:00:00Z",
			now:       "2026-01-01T13:00:00Z",
			policy:    MissedRunLatest,
			wantTrigg: "2026-01-01T13:00:00Z",
			wantNext:  "2026-01-01T14:00:00Z",
		},
		{
			// 边界：now 恰好落在时隙上，默认策略仍不晚于 now，回退到 now 之后。
			name:      "now exactly on a slot run_once",
			schedule:  everyHour,
			slot:      "2026-01-01T08:00:00Z",
			now:       "2026-01-01T13:00:00Z",
			policy:    MissedRunOnce,
			wantTrigg: "2026-01-01T08:00:00Z",
			wantNext:  "2026-01-01T14:00:00Z",
		},
		{
			// 边界：认领时隙恰好等于 now（无迟到），三种策略一致。
			name:      "slot exactly at now",
			schedule:  everyHour,
			slot:      "2026-01-01T13:00:00Z",
			now:       "2026-01-01T13:00:00Z",
			policy:    MissedRunLatest,
			wantTrigg: "2026-01-01T13:00:00Z",
			wantNext:  "2026-01-01T14:00:00Z",
		},
		{
			// 边界：slot 晚于 now（异常/竞态），run_latest 不运行并跳到 now 之后。
			name:      "slot after now run_latest",
			schedule:  everyHour,
			slot:      "2026-01-01T14:00:00Z",
			now:       "2026-01-01T13:07:00Z",
			policy:    MissedRunLatest,
			wantTrigg: "",
			wantNext:  "2026-01-01T14:00:00Z",
		},
		{
			// 未知策略与空策略一样回退到默认 run_once，保证拼写错误不改变行为。
			name:      "unknown policy falls back to run_once",
			schedule:  everyHour,
			slot:      "2026-01-01T08:00:00Z",
			now:       "2026-01-01T13:07:00Z",
			policy:    "typo",
			wantTrigg: "2026-01-01T08:00:00Z",
			wantNext:  "2026-01-01T14:00:00Z",
		},
		{
			// 工作日 cron：周末没有时隙，run_latest 从周五跳到周一这个最近的时隙。
			name:      "weekday cron run_latest skips the weekend",
			schedule:  Schedule{Kind: KindCron, Expr: "0 9 * * 1-5"},
			slot:      "2026-01-02T09:00:00Z", // 周五
			now:       "2026-01-05T10:00:00Z", // 周一
			policy:    MissedRunLatest,
			wantTrigg: "2026-01-05T09:00:00Z",
			wantNext:  "2026-01-06T09:00:00Z",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			slot := mustTime(t, tc.slot)
			now := mustTime(t, tc.now)
			trigger, next := tc.schedule.NextTrigger(slot, now, tc.policy)

			if tc.wantTrigg == "" {
				if !trigger.IsZero() {
					t.Fatalf("trigger = %s, want zero (no run)", trigger.Format(time.RFC3339))
				}
			} else if want := mustTime(t, tc.wantTrigg); !trigger.Equal(want) {
				t.Fatalf("trigger = %s, want %s", trigger.Format(time.RFC3339), want.Format(time.RFC3339))
			}

			wantNext := mustTime(t, tc.wantNext)
			if !next.Equal(wantNext) {
				t.Fatalf("next = %s, want %s", next.Format(time.RFC3339), wantNext.Format(time.RFC3339))
			}

			// 不变量：非零 trigger 必须是一个真实匹配的时隙，且 next 严格晚于 trigger。
			if !trigger.IsZero() {
				if c, err := ParseCron(tc.schedule.Expr); err == nil && !c.Matches(trigger) {
					t.Fatalf("trigger %s does not match %q", trigger.Format(time.RFC3339), tc.schedule.Expr)
				}
				if !next.After(trigger) {
					t.Fatalf("next %s is not strictly after trigger %s", next.Format(time.RFC3339), trigger.Format(time.RFC3339))
				}
			}
		})
	}
}

// TestScheduleLatestSlotAtOrBefore 表驱动验证"最近一个已过期时隙"的求解。
func TestScheduleLatestSlotAtOrBefore(t *testing.T) {
	cases := []struct {
		name      string
		slot      string
		now       string
		wantEmpty bool
		want      string
	}{
		{"no missed slot", "2026-01-01T11:00:00Z", "2026-01-01T11:30:00Z", false, "2026-01-01T11:00:00Z"},
		{"several missed slots", "2026-01-01T08:00:00Z", "2026-01-01T11:30:00Z", false, "2026-01-01T11:00:00Z"},
		{"now on a slot", "2026-01-01T08:00:00Z", "2026-01-01T11:00:00Z", false, "2026-01-01T11:00:00Z"},
		{"slot after now", "2026-01-01T12:00:00Z", "2026-01-01T11:30:00Z", true, ""},
	}
	schedule := Schedule{Kind: KindCron, Expr: "0 * * * *"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := schedule.latestSlotAtOrBefore(mustTime(t, tc.slot), mustTime(t, tc.now))
			if tc.wantEmpty {
				if !got.IsZero() {
					t.Fatalf("got %s, want zero", got.Format(time.RFC3339))
				}
				return
			}
			if want := mustTime(t, tc.want); !got.Equal(want) {
				t.Fatalf("got %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
			}
		})
	}
}

// TestMissedRunLatestOptIn 验证新策略是 opt-in：规范化、校验与默认值均不受影响。
func TestMissedRunLatestOptIn(t *testing.T) {
	spec := Spec{Missed: " RUN_LATEST "}
	if got := spec.MissedPolicy(); got != MissedRunLatest {
		t.Fatalf("normalized missed = %q, want %q", got, MissedRunLatest)
	}
	spec.ID = "task"
	spec.Schedule = Schedule{Kind: KindCron, Expr: "0 * * * *"}
	spec.Name = "task"
	spec.Prompt = "do task"
	if err := spec.Validate(); err != nil {
		t.Fatalf("run_latest should validate: %v", err)
	}

	// 默认（空 missed）仍为 run_once，未知值仍被校验拒绝。
	if got := (Spec{}).MissedPolicy(); got != MissedRunOnce {
		t.Fatalf("default missed = %q, want %q", got, MissedRunOnce)
	}
	spec.Missed = "typo"
	if err := spec.Validate(); err == nil {
		t.Fatal("unknown missed policy should fail validation")
	}
}

// TestDaemonMissedRunLatest 验证新策略在 daemon 中端到端生效：
// 多个时隙被错过时只补跑一次，并把下次到期推进到 now 之后的时隙。
func TestDaemonMissedRunLatest(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	now := mustTime(t, "2026-01-01T13:07:00Z")
	spec := dueSpec("nightly", mustTime(t, "2026-01-01T08:00:00Z"))
	spec.Missed = MissedRunLatest
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
	// 相同时刻的第二次扫描不应再次触发。
	d.scan(context.Background())
	d.wait()

	if exec.count() != 1 {
		t.Fatalf("ran %d times, want exactly one catch-up run", exec.count())
	}
	got, _ := store.Get("nightly")
	if got.LastStatus != StatusOK {
		t.Fatalf("last status = %q, want ok", got.LastStatus)
	}
	if want := mustTime(t, "2026-01-01T14:00:00Z"); !got.NextDue.Equal(want) {
		t.Fatalf("next due = %s, want %s", got.NextDue, want)
	}
}
