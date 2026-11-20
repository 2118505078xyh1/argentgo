package automation

import (
	"reflect"
	"testing"
	"time"
)

func TestDescribeCronSummary(t *testing.T) {
	cases := []struct {
		expr string
		want string
	}{
		{"* * * * *", "every minute"},
		{"*/15 * * * *", "every 15 minutes"},
		{"0 * * * *", "hourly at :00"},
		{"0 3 * * *", "every day at 03:00"},
		{"0 9 * * 1-5", "every Monday-Friday at 09:00"},
		{"0 0 * * 7", "every Sunday at 00:00"},
		{"30 9-17 * * *", "every day at :30 during hours 9-17"},
		{"0-30/10 * * * *", "every day at minutes 0,10,20,30 of every hour"},
		{"0 0 1 1,7 *", "on day 1 of the month in January,July at 00:00"},
		{"* * * * 1", "every Monday, every minute"},
		{"0 0 31 2 *", "on day 31 of the month in February at 00:00"},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := DescribeCron(tc.expr, time.Time{}, 0)
			if err != nil {
				t.Fatalf("DescribeCron(%q): %v", tc.expr, err)
			}
			if got.Summary != tc.want {
				t.Fatalf("DescribeCron(%q).Summary = %q, want %q", tc.expr, got.Summary, tc.want)
			}
			if got.String() != tc.want {
				t.Fatalf("String() = %q, want %q", got.String(), tc.want)
			}
			if got.Expression != tc.expr {
				t.Fatalf("Expression = %q, want %q", got.Expression, tc.expr)
			}
			if len(got.Next) != 0 {
				t.Fatalf("n=0 should not compute next times, got %v", got.Next)
			}
		})
	}
}

func TestDescribeCronNextTimes(t *testing.T) {
	cases := []struct {
		name  string
		expr  string
		after string
		n     int
		want  []string
	}{
		{
			name: "next three quarter-hours", expr: "*/15 * * * *",
			after: "2026-01-01T00:07:00Z", n: 3,
			want: []string{"2026-01-01T00:15:00Z", "2026-01-01T00:30:00Z", "2026-01-01T00:45:00Z"},
		},
		{
			name: "strictly after the slot", expr: "*/15 * * * *",
			after: "2026-01-01T00:15:00Z", n: 2,
			want: []string{"2026-01-01T00:30:00Z", "2026-01-01T00:45:00Z"},
		},
		{
			name: "daily across midnight", expr: "0 3 * * *",
			after: "2026-01-01T00:00:00Z", n: 2,
			want: []string{"2026-01-01T03:00:00Z", "2026-01-02T03:00:00Z"},
		},
		{
			name: "never firing has no next times", expr: "0 0 31 2 *",
			after: "2026-01-01T00:00:00Z", n: 3, want: nil,
		},
		{
			name: "zero count skips computation", expr: "*/15 * * * *",
			after: "2026-01-01T00:07:00Z", n: 0, want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DescribeCron(tc.expr, mustTime(t, tc.after), tc.n)
			if err != nil {
				t.Fatalf("DescribeCron: %v", err)
			}
			want := make([]time.Time, 0, len(tc.want))
			for _, v := range tc.want {
				want = append(want, mustTime(t, v))
			}
			if len(got.Next) != len(want) {
				t.Fatalf("Next = %v, want %d times", got.Next, len(want))
			}
			for i := range want {
				if !got.Next[i].Equal(want[i]) {
					t.Fatalf("Next[%d] = %s, want %s", i, got.Next[i].Format(time.RFC3339), want[i].Format(time.RFC3339))
				}
			}
		})
	}
}

func TestDescribeCronZeroAfterSkipsTimes(t *testing.T) {
	got, err := DescribeCron("*/15 * * * *", time.Time{}, 3)
	if err != nil {
		t.Fatalf("DescribeCron: %v", err)
	}
	if len(got.Next) != 0 {
		t.Fatalf("zero after should skip next times, got %v", got.Next)
	}
	if got.Summary != "every 15 minutes" {
		t.Fatalf("summary = %q, want the human-readable sentence even without a clock", got.Summary)
	}
}

func TestDescribeCronRejectsInvalidExpression(t *testing.T) {
	if _, err := DescribeCron("not a cron", time.Now(), 3); err == nil {
		t.Fatal("DescribeCron must propagate the parse error for an invalid expression")
	}
}

func TestDescribeCronIsDeterministic(t *testing.T) {
	after := mustTime(t, "2026-06-01T12:34:00Z")
	first, err := DescribeCron("0 0 1 6,12 *", after, 3)
	if err != nil {
		t.Fatalf("DescribeCron: %v", err)
	}
	second, err := DescribeCron("0 0 1 6,12 *", after, 3)
	if err != nil {
		t.Fatalf("DescribeCron: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("DescribeCron is not deterministic: %+v vs %+v", first, second)
	}
}

func TestScheduleDescribe(t *testing.T) {
	after := mustTime(t, "2026-01-01T00:00:00Z")
	t.Run("cron delegates to DescribeCron", func(t *testing.T) {
		s := Schedule{Kind: KindCron, Expr: "0 3 * * *"}
		got, err := s.Describe(after, 1)
		if err != nil {
			t.Fatalf("Describe: %v", err)
		}
		if got.Summary != "every day at 03:00" {
			t.Fatalf("summary = %q", got.Summary)
		}
		if len(got.Next) != 1 || !got.Next[0].Equal(mustTime(t, "2026-01-01T03:00:00Z")) {
			t.Fatalf("next = %v", got.Next)
		}
	})
	t.Run("interval", func(t *testing.T) {
		s := Schedule{Kind: KindInterval, Expr: "30m"}
		got, err := s.Describe(after, 2)
		if err != nil {
			t.Fatalf("Describe: %v", err)
		}
		if got.Summary != "every 30m" {
			t.Fatalf("summary = %q, want %q", got.Summary, "every 30m")
		}
		want := []time.Time{after.Add(30 * time.Minute), after.Add(time.Hour)}
		if !reflect.DeepEqual(got.Next, want) {
			t.Fatalf("next = %v, want %v", got.Next, want)
		}
	})
	t.Run("day interval advances the calendar", func(t *testing.T) {
		s := Schedule{Kind: KindInterval, Expr: "1d"}
		got, err := s.Describe(after, 1)
		if err != nil {
			t.Fatalf("Describe: %v", err)
		}
		if len(got.Next) != 1 || !got.Next[0].Equal(after.AddDate(0, 0, 1)) {
			t.Fatalf("next = %v", got.Next)
		}
	})
	t.Run("invalid interval fails", func(t *testing.T) {
		if _, err := (Schedule{Kind: KindInterval, Expr: "nope"}).Describe(after, 2); err == nil {
			t.Fatal("Describe must fail for an invalid interval")
		}
	})
	t.Run("unknown kind fails", func(t *testing.T) {
		if _, err := (Schedule{Kind: "hourly", Expr: "30m"}).Describe(after, 2); err == nil {
			t.Fatal("Describe must fail for an unknown schedule kind")
		}
	})
}

func TestScheduleNextTimes(t *testing.T) {
	after := mustTime(t, "2026-01-01T00:00:00Z")
	cases := []struct {
		name string
		s    Schedule
		n    int
		want []time.Time
	}{
		{"zero count", Schedule{Kind: KindCron, Expr: "* * * * *"}, 0, nil},
		{"cron", Schedule{Kind: KindCron, Expr: "*/15 * * * *"}, 2,
			[]time.Time{after.Add(15 * time.Minute), after.Add(30 * time.Minute)}},
		{"interval", Schedule{Kind: KindInterval, Expr: "1h"}, 2,
			[]time.Time{after.Add(time.Hour), after.Add(2 * time.Hour)}},
		{"never fires", Schedule{Kind: KindCron, Expr: "0 0 31 2 *"}, 3, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.s.NextTimes(after, tc.n)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("NextTimes = %v, want %v", got, tc.want)
			}
		})
	}
	if got := (Schedule{Kind: KindCron, Expr: "* * * * *"}).NextTimes(time.Time{}, 3); got != nil {
		t.Fatalf("zero after should return nil, got %v", got)
	}
}

func TestScheduleStringIncludesCronSummary(t *testing.T) {
	cases := []struct {
		s    Schedule
		want string
	}{
		{Schedule{Kind: KindCron, Expr: "0 3 * * *"}, "cron 0 3 * * * (every day at 03:00)"},
		{Schedule{Kind: KindCron, Expr: "*/15 * * * *"}, "cron */15 * * * * (every 15 minutes)"},
		// kind 从五字段表达式推断。
		{Schedule{Expr: "0 9 * * 1-5"}, "cron 0 9 * * 1-5 (every Monday-Friday at 09:00)"},
		// 无效的 cron 保留规范形式，使日志保持可读。
		{Schedule{Kind: KindCron, Expr: "bogus"}, "cron bogus"},
		// 间隔调度保持不变。
		{Schedule{Kind: KindInterval, Expr: "30m"}, "interval 30m"},
	}
	for _, tc := range cases {
		if got := tc.s.String(); got != tc.want {
			t.Fatalf("String() = %q, want %q", got, tc.want)
		}
	}
}
