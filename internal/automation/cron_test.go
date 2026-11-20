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
	// An hour is an elapsed hour, so the local clock jumps from 01:30 EST
	// to 03:30 EDT. Only day intervals are calendar-based.
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
	// An empty kind is inferred from the expression shape.
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
