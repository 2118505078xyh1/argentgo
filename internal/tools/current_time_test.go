package tools

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"
)

func fixedClock(t *testing.T) {
	t.Helper()
	previous := currentTimeNow
	currentTimeNow = func() time.Time {
		return time.Date(2026, 11, 19, 12, 34, 56, 0, time.UTC)
	}
	t.Cleanup(func() { currentTimeNow = previous })
}

func TestCurrentTimeDefaultsToUTCAndLocalRFC3339(t *testing.T) {
	fixedClock(t)
	registry := New(t.TempDir(), true)

	out, err := registry.Run(context.Background(), "current_time", `{}`)
	if err != nil {
		t.Fatalf("current_time: %v", err)
	}
	for _, want := range []string{"utc: 2026-11-19T12:34:56Z", "Local: 2026-11-19T"} {
		if !strings.Contains(out, want) {
			t.Fatalf("current_time = %q, want %q", out, want)
		}
	}
}

func TestCurrentTimeSupportsIANAZone(t *testing.T) {
	if _, err := time.LoadLocation("Asia/Shanghai"); err != nil {
		t.Skipf("no IANA tzdata on this host: %v", err)
	}
	fixedClock(t)
	registry := New(t.TempDir(), true)

	out, err := registry.Run(context.Background(), "current_time", `{"timezone":"Asia/Shanghai"}`)
	if err != nil {
		t.Fatalf("current_time: %v", err)
	}
	if !strings.Contains(out, "Asia/Shanghai: 2026-11-19T20:34:56+08:00") {
		t.Fatalf("current_time = %q", out)
	}
}

func TestCurrentTimeFormats(t *testing.T) {
	fixedClock(t)
	registry := New(t.TempDir(), true)
	fixed := time.Date(2026, 11, 19, 12, 34, 56, 0, time.UTC)

	cases := map[string]string{
		"date":    "2026-11-19",
		"time":    "12:34:56",
		"unix":    strconv.FormatInt(fixed.Unix(), 10),
		"rfc3339": "2026-11-19T12:34:56Z",
	}
	for format, want := range cases {
		out, err := registry.Run(context.Background(), "current_time", `{"timezone":"UTC","format":"`+format+`"}`)
		if err != nil {
			t.Fatalf("current_time(%s): %v", format, err)
		}
		if !strings.Contains(out, "utc: "+want) {
			t.Fatalf("current_time(%s) = %q, want %q", format, out, want)
		}
	}
}

func TestCurrentTimeRejectsInvalidArgs(t *testing.T) {
	registry := New(t.TempDir(), true)
	for _, args := range []string{`{"format":"iso"}`, `{"timezone":"Mars/Phobos"}`, `{`} {
		if _, err := registry.Run(context.Background(), "current_time", args); err == nil {
			t.Fatalf("current_time accepted invalid args %s", args)
		}
	}
	if _, err := registry.Run(context.Background(), "current_time", `{"format":"ISO"}`); err == nil || !strings.Contains(err.Error(), "invalid format") {
		t.Fatalf("err = %v, want an invalid-format error", err)
	}
	if _, err := registry.Run(context.Background(), "current_time", `{"timezone":"Mars/Phobos"}`); err == nil || !strings.Contains(err.Error(), "unknown timezone") {
		t.Fatalf("err = %v, want an unknown-timezone error", err)
	}
}
