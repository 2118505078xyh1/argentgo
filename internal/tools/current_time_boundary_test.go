package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

func fixedClockAt(t *testing.T, instant time.Time) {
	t.Helper()
	previous := currentTimeNow
	currentTimeNow = func() time.Time { return instant }
	t.Cleanup(func() { currentTimeNow = previous })
}

func TestCurrentTimeHandlesDSTTransitionAtIANABoundary(t *testing.T) {
	zone := "America/New_York"
	if _, err := time.LoadLocation(zone); err != nil {
		t.Skipf("no IANA tzdata on this host: %v", err)
	}
	registry := New(t.TempDir(), true)
	cases := []struct {
		instant time.Time
		want    string
	}{
		{time.Date(2026, 3, 8, 6, 30, 0, 0, time.UTC), zone + ": 2026-03-08T01:30:00-05:00"},
		{time.Date(2026, 3, 8, 7, 30, 0, 0, time.UTC), zone + ": 2026-03-08T03:30:00-04:00"},
		{time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC), zone + ": 2026-11-01T01:30:00-04:00"},
		{time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC), zone + ": 2026-11-01T01:30:00-05:00"},
	}
	for _, tc := range cases {
		fixedClockAt(t, tc.instant)
		out, err := registry.Run(context.Background(), "current_time", `{"timezone":"`+zone+`"}`)
		if err != nil {
			t.Fatalf("current_time(%s): %v", tc.instant, err)
		}
		if !strings.Contains(out, tc.want) {
			t.Fatalf("current_time(%s) = %q, want %q", tc.instant, out, tc.want)
		}
	}
}

func TestCurrentTimeHandlesHalfHourIANAOffsetAndDateRollover(t *testing.T) {
	if _, err := time.LoadLocation("Asia/Kolkata"); err != nil {
		t.Skipf("no IANA tzdata on this host: %v", err)
	}
	fixedClockAt(t, time.Date(2026, 11, 19, 20, 30, 0, 0, time.UTC))
	registry := New(t.TempDir(), true)
	out, err := registry.Run(context.Background(), "current_time", `{"timezone":"Asia/Kolkata","format":"date"}`)
	if err != nil {
		t.Fatalf("current_time: %v", err)
	}
	if !strings.Contains(out, "utc: 2026-11-19") || !strings.Contains(out, "Asia/Kolkata: 2026-11-20") {
		t.Fatalf("date rollover = %q", out)
	}
}
