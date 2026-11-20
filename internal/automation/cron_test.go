package automation

import (
	"testing"
	"time"
	_ "time/tzdata" // keep DST tests independent of the host's zone database
)

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return parsed
}

func TestCronNextRunTable(t *testing.T) {
	cases := []struct {
		name  string
		expr  string
		after string
		want  string
	}{
		{"quarter hourly", "*/15 * * * *", "2026-01-01T00:07:00Z", "2026-01-01T00:15:00Z"},
		{"strictly after the slot", "*/15 * * * *", "2026-01-01T00:15:00Z", "2026-01-01T00:30:00Z"},
		{"every seven minutes", "*/7 * * * *", "2026-01-01T00:00:00Z", "2026-01-01T00:07:00Z"},
		{"every seven minutes, late", "*/7 * * * *", "2026-01-01T00:50:00Z", "2026-01-01T00:56:00Z"},
		{"every seven minutes, wrap", "*/7 * * * *", "2026-01-01T00:57:00Z", "2026-01-01T01:00:00Z"},
		{"hourly", "0 * * * *", "2026-01-01T00:00:00Z", "2026-01-01T01:00:00Z"},
		{"minute list", "0,30 * * * *", "2026-01-01T00:15:00Z", "2026-01-01T00:30:00Z"},
		{"stepped range", "0-30/10 * * * *", "2026-01-01T00:05:00Z", "2026-01-01T00:10:00Z"},
		{"stepped range exhausted", "0-30/10 * * * *", "2026-01-01T00:31:00Z", "2026-01-01T01:00:00Z"},
		{"hour range", "30 9-17 * * *", "2026-03-01T10:00:00Z", "2026-03-01T10:30:00Z"},
		{"hour range rolls over", "30 9-17 * * *", "2026-03-01T17:45:00Z", "2026-03-02T09:30:00Z"},
		{"month rollover", "0 0 1 * *", "2026-01-15T00:00:00Z", "2026-02-01T00:00:00Z"},
		{"month list", "0 0 1 6,12 *", "2026-01-01T00:00:00Z", "2026-06-01T00:00:00Z"},
		{"year rollover", "0 0 1 1 *", "2026-01-01T00:00:00Z", "2027-01-01T00:00:00Z"},
		{"day of month only", "0 0 13 * *", "2026-01-01T00:00:00Z", "2026-01-13T00:00:00Z"},
		{"day of week only", "0 0 * * 1", "2026-01-01T00:00:00Z", "2026-01-05T00:00:00Z"},
		{"sunday as seven", "0 0 * * 7", "2026-01-01T00:00:00Z", "2026-01-04T00:00:00Z"},
		{"dom or dow picks the near match", "0 0 13 * 5", "2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z"},
		{"dom or dow picks dom when nearer", "0 0 13 * 5", "2026-01-10T00:00:00Z", "2026-01-13T00:00:00Z"},
		{"month without a 31st is skipped", "0 0 31 * *", "2026-01-31T00:00:00Z", "2026-03-31T00:00:00Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cron, err := ParseCron(tc.expr)
			if err != nil {
				t.Fatalf("ParseCron(%q): %v", tc.expr, err)
			}
			got := cron.Next(mustTime(t, tc.after))
			want := mustTime(t, tc.want)
			if !got.Equal(want) {
				t.Fatalf("%q after %s = %s, want %s", tc.expr, tc.after, got.Format(time.RFC3339), want.Format(time.RFC3339))
			}
		})
	}
}

func TestCronThatNeverFires(t *testing.T) {
	cron, err := ParseCron("0 0 31 2 *")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	if got := cron.Next(mustTime(t, "2026-01-01T00:00:00Z")); !got.IsZero() {
		t.Fatalf("31 February should never fire, got %s", got)
	}
}

func TestCronMatches(t *testing.T) {
	cron, err := ParseCron("30 9 * * 1-5")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	if !cron.Matches(mustTime(t, "2026-01-05T09:30:15Z")) {
		t.Fatal("Monday 09:30 should match")
	}
	if cron.Matches(mustTime(t, "2026-01-04T09:30:00Z")) {
		t.Fatal("Sunday should not match a weekday expression")
	}
	if cron.Matches(mustTime(t, "2026-01-05T09:31:00Z")) {
		t.Fatal("09:31 should not match minute 30")
	}
}

// TestCronNextAcrossDSTTransitions 守护 Next 中的挂钟小时跳转。
// 标记消失或无法检查：不要卡住任务。
// TestCronNextAcrossDSTTransitions 守护 Next 中的挂钟小时跳转。DST 回拨使某个本地小时出现两次；用 time.Date 在该小时重建时间会解析为第一次出现，因此忽略这一点的
// 忽略这一点可能永远循环。每个用例都在超时下运行，使
// 回归大声失败而非挂住套件。
func TestCronNextAcrossDSTTransitions(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	lh, err := time.LoadLocation("Australia/Lord_Howe")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	in := func(loc *time.Location, value string) time.Time {
		t.Helper()
		parsed, err := time.ParseInLocation("2006-01-02T15:04:05", value, loc)
		if err != nil {
			t.Fatalf("parse %q: %v", value, err)
		}
		return parsed
	}
	cases := []struct {
		name string
		expr string
		loc  *time.Location
		from time.Time
		want time.Time
	}{
		{
			// 小时前进可能永远循环。每个用例都在超时下运行，使回归大声失败而非挂住套件。
			// 回归：小时 1 被排除，因此 Next 必须越过回拨日 01:00 的两个出现。在
			// 01:00 重建时间会把循环永远钉在第一次出现上。
			name: "fall back with the repeated hour excluded",
			expr: "0 0 * * *",
			loc:  ny,
			from: in(ny, "2026-11-01T00:01:00"),
			want: in(ny, "2026-11-02T00:00:00"),
		},
		{
			// 重复的小时被允许：第一次出现是下一个。
			name: "fall back with the repeated hour allowed",
			expr: "30 1 * * *",
			loc:  ny,
			from: in(ny, "2026-11-01T00:00:00"),
			want: time.Date(2026, 11, 1, 1, 30, 0, 0, ny), // 01:30 EDT
		},
		{
			// 02:30 在春季前拨日不存在；下一个有效时段是第二天早上。
			// 时段是第二天早上。
			name: "spring forward skips a nonexistent wall time",
			expr: "30 2 * * *",
			loc:  ny,
			from: in(ny, "2026-03-08T00:00:00"),
			want: in(ny, "2026-03-09T02:30:00"),
		},
		{
			// Lord Howe 偏移 30 分钟，因此基于时长的跳转仍必须落在真实的挂钟分钟上。
			// 仍必须落在真实的挂钟分钟上。
			name: "half hour fall back with the repeated hour excluded",
			expr: "0 0 * * *",
			loc:  lh,
			from: in(lh, "2026-04-05T01:00:00"),
			want: in(lh, "2026-04-06T00:00:00"),
		},
		{
			// 跳到下一小时必须把分钟规范化为零。
			name: "hour jump starts at the top of the hour",
			expr: "0 9 * * *",
			loc:  ny,
			from: in(ny, "2026-01-01T08:37:00"),
			want: in(ny, "2026-01-01T09:00:00"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := ParseCron(tc.expr)
			if err != nil {
				t.Fatalf("ParseCron(%q): %v", tc.expr, err)
			}
			done := make(chan time.Time, 1)
			go func() { done <- c.Next(tc.from) }()
			select {
			case got := <-done:
				if !got.Equal(tc.want) {
					t.Fatalf("%q after %s = %s, want %s", tc.expr, tc.from, got, tc.want)
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("%q after %s did not return (hour jump looped)", tc.expr, tc.from)
			}
		})
	}
}

// 跳到下一小时必须把分钟规范化为零。
// TestCronNextAcrossMidnightDST 守护 Next 中的日/月跳转。在 00:00 切换 DST 的时区有一个不存在的午夜，time.Date 会把它向后规范化到前一天。从该值重建日边界
// 前一天。从该值重建日边界
// 会让 Next 永远在同一个被排除的日期上打转（并且在 daemon 中，
// 会让 Next 永远在同一个被排除的日期上打转（并且在 daemon 中，因为 recordRun 持有 mutex 时调用 Next，会卡住 store mutex）。每个
// 用例都在超时下运行，使回归大声失败而非挂住。
func TestCronNextAcrossMidnightDST(t *testing.T) {
	havana, err := time.LoadLocation("America/Havana")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	beirut, err := time.LoadLocation("Asia/Beirut")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	in := func(loc *time.Location, value string) time.Time {
		t.Helper()
		parsed, err := time.ParseInLocation("2006-01-02T15:04:05", value, loc)
		if err != nil {
			t.Fatalf("parse %q: %v", value, err)
		}
		return parsed
	}
	cases := []struct {
		name string
		expr string
		from time.Time
		want time.Time
	}{
		{
			// 春季前拨日（2026-03-08）在 Havana 没有 00:00；"day 1" 任务必须从四月恢复，而不是在三月打转。
			// "day 1" 任务必须从四月恢复，而不是在三月打转。
			name: "monthly day skipped over a midnight spring forward",
			expr: "0 0 1 * *",
			from: in(havana, "2026-03-08T23:31:00"),
			want: in(havana, "2026-04-01T00:00:00"),
		},
		{
			// 过渡期从星期日开始的星期任务必须
			// 过渡期从星期日开始的星期任务必须到达星期一 09:30，而不是在被重复的 23:00 挂钟小时上循环。
			name: "weekday task after a midnight spring forward",
			expr: "30 9 * * 1-5",
			from: in(havana, "2026-03-08T23:31:00"),
			want: in(havana, "2026-03-09T09:30:00"),
		},
		{
			// 过渡日的 01:00 是被跳过的午夜之后的第一个有效时刻；回退步骤必须落在那里，而不是
			// 被跳过的午夜；回退步骤必须落在那里，而不是
			// 完全跳过那一天。
			name: "first valid instant of the transition day",
			expr: "0 1 8 * *",
			from: in(havana, "2026-03-07T23:31:00"),
			want: in(havana, "2026-03-08T01:00:00"),
		},
		{
			// Beirut 也在午夜前拨；月中的某一天必须落在下个月过渡后的偏移量上。
			// 落在下个月过渡后的偏移量上。
			name: "mid month day across a midnight spring forward",
			expr: "0 0 15 * *",
			from: in(beirut, "2026-03-28T23:31:00"),
			want: in(beirut, "2026-04-15T00:00:00"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := ParseCron(tc.expr)
			if err != nil {
				t.Fatalf("ParseCron(%q): %v", tc.expr, err)
			}
			done := make(chan time.Time, 1)
			go func() { done <- c.Next(tc.from) }()
			var got time.Time
			select {
			case got = <-done:
			case <-time.After(3 * time.Second):
				t.Fatalf("%q after %s did not return (day jump looped)", tc.expr, tc.from)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("%q after %s = %s, want %s", tc.expr, tc.from, got, tc.want)
			}
			if !got.After(tc.from) {
				t.Fatalf("%q after %s = %s, which is not strictly after the input", tc.expr, tc.from, got)
			}
			if !c.Matches(got) {
				t.Fatalf("%q returned %s, which does not match the expression", tc.expr, got)
			}
		})
	}
}

func TestCronParseErrors(t *testing.T) {
	for _, expr := range []string{
		"",
		"* * * *",
		"* * * * * *",
		"60 * * * *",
		"* 24 * * *",
		"* * 0 * *",
		"* * * 13 *",
		"* * * * 8",
		"*/0 * * * *",
		"5-1 * * * *",
		"a * * * *",
		"1,,2 * * * *",
	} {
		if _, err := ParseCron(expr); err == nil {
			t.Fatalf("ParseCron(%q) should fail", expr)
		}
	}
}

func TestParseInterval(t *testing.T) {
	cases := map[string]time.Duration{
		"30m":   30 * time.Minute,
		"2h":    2 * time.Hour,
		"90s":   90 * time.Second,
		"1d":    24 * time.Hour,
		"2.5d":  60 * time.Hour,
		"1 D":   24 * time.Hour,
		"1h30m": 90 * time.Minute,
	}
	for expr, want := range cases {
		got, err := ParseInterval(expr)
		if err != nil {
			t.Fatalf("ParseInterval(%q): %v", expr, err)
		}
		if got != want {
			t.Fatalf("ParseInterval(%q) = %s, want %s", expr, got, want)
		}
	}
	for _, expr := range []string{"", "0s", "-5m", "abc", "1x", "d"} {
		if _, err := ParseInterval(expr); err == nil {
			t.Fatalf("ParseInterval(%q) should fail", expr)
		}
	}
}

func TestCalendarDayIntervalAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	cases := []struct {
		name  string
		expr  string
		after time.Time
		want  time.Time
	}{
		{
			name:  "spring forward keeps noon",
			expr:  "1d",
			after: time.Date(2026, 3, 7, 12, 0, 0, 0, loc), // EST
			want:  time.Date(2026, 3, 8, 12, 0, 0, 0, loc), // EDT, only 23h later
		},
		{
			name:  "fall back keeps noon",
			expr:  "1d",
			after: time.Date(2026, 10, 31, 12, 0, 0, 0, loc), // EDT
			want:  time.Date(2026, 11, 1, 12, 0, 0, 0, loc),  // EST, 25h later
		},
		{
			name:  "two days still keep noon",
			expr:  "2d",
			after: time.Date(2026, 3, 6, 12, 0, 0, 0, loc),
			want:  time.Date(2026, 3, 8, 12, 0, 0, 0, loc),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schedule := Schedule{Kind: KindInterval, Expr: tc.expr}
			got := schedule.Next(tc.after)
			if !got.Equal(tc.want) {
				t.Fatalf("%q after %s = %s, want %s", tc.expr, tc.after, got, tc.want)
			}
			if got.Hour() != tc.after.Hour() || got.Minute() != tc.after.Minute() {
				t.Fatalf("%q moved off the wall-clock time: %s -> %s", tc.expr, tc.after, got)
			}
		})
	}
}

func TestNonDayIntervalStaysAbsoluteAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	from := time.Date(2026, 3, 8, 1, 30, 0, 0, loc) // EST, one hour before spring forward
	got := (Schedule{Kind: KindInterval, Expr: "1h"}).Next(from)
	// 一小时是经过的一小时，因此本地时钟从 01:30 EST 跳到
	// 03:30 EDT。只有日间隔是基于日历的。
	want := time.Date(2026, 3, 8, 3, 30, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("1h after %s = %s, want %s", from, got, want)
	}
}

func TestScheduleNext(t *testing.T) {
	now := mustTime(t, "2026-01-01T00:07:00Z")
	interval := Schedule{Kind: KindInterval, Expr: "30m"}
	if got, want := interval.Next(now), mustTime(t, "2026-01-01T00:37:00Z"); !got.Equal(want) {
		t.Fatalf("interval next = %s, want %s", got, want)
	}
	cron := Schedule{Kind: KindCron, Expr: "*/15 * * * *"}
	if got, want := cron.Next(now), mustTime(t, "2026-01-01T00:15:00Z"); !got.Equal(want) {
		t.Fatalf("cron next = %s, want %s", got, want)
	}
	// 空的 kind 从表达式形态推断。
	inferred := Schedule{Expr: "*/15 * * * *"}
	if got, want := inferred.Next(now), mustTime(t, "2026-01-01T00:15:00Z"); !got.Equal(want) {
		t.Fatalf("inferred cron next = %s, want %s", got, want)
	}
	inferredInterval := Schedule{Expr: "30m"}
	if got := inferredInterval.Next(now); !got.Equal(mustTime(t, "2026-01-01T00:37:00Z")) {
		t.Fatalf("inferred interval next = %s", got)
	}
	if !(Schedule{Kind: KindCron, Expr: "nope"}).Next(now).IsZero() {
		t.Fatal("an invalid cron expression should not produce a next time")
	}
	if err := (Schedule{Kind: "hourly", Expr: "1h"}).Validate(); err == nil {
		t.Fatal("an unknown kind should fail validation")
	}
}
