package automation

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// stepNext 返回按固定步长推进的注入 next，供表驱动用例复用。
func stepNext(step time.Duration) func(time.Time) time.Time {
	return func(t time.Time) time.Time { return t.Add(step) }
}

func TestPreviewScheduleNextRuns(t *testing.T) {
	cases := []struct {
		name    string
		expr    string
		now     string
		n       int
		step    time.Duration
		want    []string
		wantHum string
	}{
		{
			name: "daily cron three runs", expr: "0 9 * * *",
			now: "2026-01-01T08:00:00Z", n: 3, step: 24 * time.Hour,
			want: []string{
				"2026-01-02T08:00:00Z",
				"2026-01-03T08:00:00Z",
				"2026-01-04T08:00:00Z",
			},
			wantHum: "每天 09:00 起，接下来 3 次",
		},
		{
			name: "single run", expr: "*/15 * * * *",
			now: "2026-01-01T00:07:00Z", n: 1, step: 15 * time.Minute,
			want:    []string{"2026-01-01T00:22:00Z"},
			wantHum: "调度 */15 * * * *，接下来 1 次",
		},
		{
			name: "five runs", expr: "30m",
			now: "2026-01-01T00:00:00Z", n: 5, step: 30 * time.Minute,
			want: []string{
				"2026-01-01T00:30:00Z",
				"2026-01-01T01:00:00Z",
				"2026-01-01T01:30:00Z",
				"2026-01-01T02:00:00Z",
				"2026-01-01T02:30:00Z",
			},
			wantHum: "调度 30m，接下来 5 次",
		},
		{
			name: "large n is honored", expr: "1h",
			now: "2026-01-01T00:00:00Z", n: 10, step: time.Hour,
			want: []string{
				"2026-01-01T01:00:00Z", "2026-01-01T02:00:00Z", "2026-01-01T03:00:00Z",
				"2026-01-01T04:00:00Z", "2026-01-01T05:00:00Z", "2026-01-01T06:00:00Z",
				"2026-01-01T07:00:00Z", "2026-01-01T08:00:00Z", "2026-01-01T09:00:00Z",
				"2026-01-01T10:00:00Z",
			},
			wantHum: "调度 1h，接下来 10 次",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PreviewSchedule(tc.expr, mustTime(t, tc.now), stepNext(tc.step), tc.n)
			if got.Expression != tc.expr {
				t.Fatalf("Expression = %q, want %q", got.Expression, tc.expr)
			}
			if !reflect.DeepEqual(got.NextRuns, tc.want) {
				t.Fatalf("NextRuns = %v, want %v", got.NextRuns, tc.want)
			}
			if got.Human != tc.wantHum {
				t.Fatalf("Human = %q, want %q", got.Human, tc.wantHum)
			}
		})
	}
}

func TestPreviewScheduleDefaultCount(t *testing.T) {
	for _, n := range []int{0, -1, -100} {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			got := PreviewSchedule("0 9 * * *", mustTime(t, "2026-01-01T08:00:00Z"), stepNext(time.Hour), n)
			if len(got.NextRuns) != previewDefaultCount {
				t.Fatalf("n=%d: len(NextRuns) = %d, want %d", n, len(got.NextRuns), previewDefaultCount)
			}
			wantHum := "每天 09:00 起，接下来 3 次"
			if got.Human != wantHum {
				t.Fatalf("Human = %q, want %q", got.Human, wantHum)
			}
		})
	}
}

func TestPreviewScheduleStableUTC(t *testing.T) {
	// 注入的时刻带 +08:00 偏移；输出必须归一到 UTC，从而不依赖宿主时区。
	now := mustTime(t, "2026-01-01T08:00:00+08:00")
	next := func(t time.Time) time.Time { return t.Add(90 * time.Minute) }
	got := PreviewSchedule("1h30m", now, next, 2)
	want := []string{"2026-01-01T01:30:00Z", "2026-01-01T03:00:00Z"}
	if !reflect.DeepEqual(got.NextRuns, want) {
		t.Fatalf("NextRuns = %v, want %v", got.NextRuns, want)
	}
	for _, v := range got.NextRuns {
		if !strings.HasSuffix(v, "Z") {
			t.Fatalf("time %q is not normalized to UTC", v)
		}
	}
}

func TestPreviewScheduleCJK(t *testing.T) {
	expr := "每天 09:00 定时任务"
	now := mustTime(t, "2026-01-01T08:00:00Z")
	got := PreviewSchedule(expr, now, stepNext(time.Hour), 2)
	if got.Expression != expr {
		t.Fatalf("Expression = %q, want %q", got.Expression, expr)
	}
	if !strings.Contains(got.Human, "定时任务") || !strings.Contains(got.Human, "接下来 2 次") {
		t.Fatalf("Human = %q, want Chinese expression and stable count", got.Human)
	}
	if want := []string{"2026-01-01T09:00:00Z", "2026-01-01T10:00:00Z"}; !reflect.DeepEqual(got.NextRuns, want) {
		t.Fatalf("NextRuns = %v, want %v", got.NextRuns, want)
	}
}

func TestPreviewScheduleDoesNotModifyInputs(t *testing.T) {
	now := mustTime(t, "2026-01-01T08:00:00Z")
	original := now
	expr := "0 9 * * *"
	calls := 0
	next := func(at time.Time) time.Time {
		calls++
		if !at.Equal(now.Add(time.Duration(calls-1) * time.Hour)) {
			t.Fatalf("next call %d got unexpected time %s", calls, at.Format(time.RFC3339))
		}
		return at.Add(time.Hour)
	}
	got := PreviewSchedule(expr, now, next, 3)
	if !now.Equal(original) {
		t.Fatalf("now was modified: %s, want %s", now.Format(time.RFC3339), original.Format(time.RFC3339))
	}
	if expr != "0 9 * * *" {
		t.Fatalf("expr was modified: %q", expr)
	}
	if calls != 3 {
		t.Fatalf("next called %d times, want 3", calls)
	}
	if len(got.NextRuns) != 3 {
		t.Fatalf("len(NextRuns) = %d, want 3", len(got.NextRuns))
	}
}

func TestPreviewScheduleEmpty(t *testing.T) {
	t.Run("nil next", func(t *testing.T) {
		got := PreviewSchedule("0 9 * * *", mustTime(t, "2026-01-01T08:00:00Z"), nil, 3)
		if len(got.NextRuns) != 0 {
			t.Fatalf("NextRuns = %v, want empty", got.NextRuns)
		}
		if want := "每天 09:00 起，接下来 3 次"; got.Human != want {
			t.Fatalf("Human = %q, want %q", got.Human, want)
		}
	})
	t.Run("next returns zero", func(t *testing.T) {
		got := PreviewSchedule("never", mustTime(t, "2026-01-01T08:00:00Z"),
			func(time.Time) time.Time { return time.Time{} }, 3)
		if len(got.NextRuns) != 0 {
			t.Fatalf("NextRuns = %v, want empty", got.NextRuns)
		}
		if want := "调度 never，接下来 3 次"; got.Human != want {
			t.Fatalf("Human = %q, want %q", got.Human, want)
		}
	})
	t.Run("next stops early", func(t *testing.T) {
		calls := 0
		got := PreviewSchedule("x", mustTime(t, "2026-01-01T08:00:00Z"), func(t time.Time) time.Time {
			calls++
			if calls == 2 {
				return time.Time{}
			}
			return t.Add(time.Hour)
		}, 5)
		if calls != 2 {
			t.Fatalf("next called %d times, want 2", calls)
		}
		if want := []string{"2026-01-01T09:00:00Z"}; !reflect.DeepEqual(got.NextRuns, want) {
			t.Fatalf("NextRuns = %v, want %v", got.NextRuns, want)
		}
	})
}

func TestPreviewScheduleDeterministic(t *testing.T) {
	now := mustTime(t, "2026-06-01T12:34:00Z")
	a := PreviewSchedule("0 9 * * 1-5", now, stepNext(24*time.Hour), 3)
	b := PreviewSchedule("0 9 * * 1-5", now, stepNext(24*time.Hour), 3)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("PreviewSchedule is not deterministic: %+v vs %+v", a, b)
	}
}

func TestSchedulePreviewSummary(t *testing.T) {
	cases := []struct {
		name  string
		input SchedulePreview
		want  string
	}{
		{
			name: "with runs",
			input: SchedulePreview{
				Expression: "0 9 * * *",
				NextRuns:   []string{"2026-01-02T08:00:00Z", "2026-01-03T08:00:00Z"},
				Human:      "每天 09:00 起，接下来 3 次",
			},
			want: "0 9 * * * | 每天 09:00 起，接下来 3 次 | 下一次: 2026-01-02T08:00:00Z",
		},
		{
			name: "no runs",
			input: SchedulePreview{
				Expression: "never",
				Human:      "调度 never，接下来 3 次",
			},
			want: "never | 调度 never，接下来 3 次 | 下一次: 无",
		},
		{
			name:  "zero value",
			input: SchedulePreview{},
			want:  " |  | 下一次: 无",
		},
		{
			name: "whitespace is folded to a single line",
			input: SchedulePreview{
				Expression: "0 9\n* *\t*",
				NextRuns:   []string{"2026-01-02T08:00:00Z"},
				Human:      "每天 09:00 起，\n接下来 3 次",
			},
			want: "0 9 * * * | 每天 09:00 起， 接下来 3 次 | 下一次: 2026-01-02T08:00:00Z",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.input.Summary()
			if got != tc.want {
				t.Fatalf("Summary() = %q, want %q", got, tc.want)
			}
			if strings.ContainsAny(got, "\n\r") {
				t.Fatalf("Summary() is not a single line: %q", got)
			}
		})
	}
}
