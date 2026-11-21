package automation

import (
	"testing"
	"time"
)

// windowTime 解析测试用的 UTC 时刻；2026-01-05 是星期一。
func windowTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return parsed
}

func TestTimeWindowContains(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	inShanghai := time.Date(2026, 1, 5, 10, 0, 0, 0, shanghai) // 本地 10 点，UTC 2 点

	cases := []struct {
		name string
		w    TimeWindow
		at   time.Time
		want bool
	}{
		{"normal window at lower bound", TimeWindow{StartHour: 9, EndHour: 17}, windowTime(t, "2026-01-05T09:00:00Z"), true},
		{"normal window inside", TimeWindow{StartHour: 9, EndHour: 17}, windowTime(t, "2026-01-05T16:59:00Z"), true},
		{"normal window upper bound excluded", TimeWindow{StartHour: 9, EndHour: 17}, windowTime(t, "2026-01-05T17:00:00Z"), false},
		{"normal window before lower bound", TimeWindow{StartHour: 9, EndHour: 17}, windowTime(t, "2026-01-05T08:59:00Z"), false},
		{"cross midnight late half", TimeWindow{StartHour: 22, EndHour: 6}, windowTime(t, "2026-01-05T23:30:00Z"), true},
		{"cross midnight across zero", TimeWindow{StartHour: 22, EndHour: 6}, windowTime(t, "2026-01-06T00:00:00Z"), true},
		{"cross midnight before upper bound", TimeWindow{StartHour: 22, EndHour: 6}, windowTime(t, "2026-01-06T05:59:00Z"), true},
		{"cross midnight upper bound excluded", TimeWindow{StartHour: 22, EndHour: 6}, windowTime(t, "2026-01-06T06:00:00Z"), false},
		{"cross midnight before lower bound", TimeWindow{StartHour: 22, EndHour: 6}, windowTime(t, "2026-01-05T21:59:00Z"), false},
		{"zero window has no constraint", TimeWindow{}, windowTime(t, "2026-01-05T03:00:00Z"), true},
		{"equal endpoints mean whole day", TimeWindow{StartHour: 9, EndHour: 9}, windowTime(t, "2026-01-05T03:00:00Z"), true},
		{"allowed weekday", TimeWindow{9, 17, []time.Weekday{time.Monday, time.Wednesday}}, windowTime(t, "2026-01-05T10:00:00Z"), true},
		{"filtered weekday", TimeWindow{9, 17, []time.Weekday{time.Monday, time.Wednesday}}, windowTime(t, "2026-01-06T10:00:00Z"), false},
		{"allowed weekday outside hours", TimeWindow{9, 17, []time.Weekday{time.Monday}}, windowTime(t, "2026-01-05T18:00:00Z"), false},
		{"impossible weekday", TimeWindow{Days: []time.Weekday{time.Weekday(7)}}, windowTime(t, "2026-01-05T10:00:00Z"), false},
		{"evaluated in the time's own zone", TimeWindow{StartHour: 9, EndHour: 17}, inShanghai, true},
		{"zero time", TimeWindow{}, time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.w.Contains(tc.at); got != tc.want {
				t.Fatalf("%+v Contains(%s) = %v, want %v", tc.w, tc.at, got, tc.want)
			}
		})
	}
}

func TestNextInWindowTable(t *testing.T) {
	hourly := func(v time.Time) time.Time { return v.Add(time.Hour) }
	stalled := func(v time.Time) time.Time { return v }
	regressing := func(v time.Time) time.Time { return v.Add(-time.Hour) }

	cases := []struct {
		name   string
		expr   string
		w      TimeWindow
		now    string
		next   func(time.Time) time.Time
		want   string
		wantOK bool
	}{
		{"advance hourly into window", "ignored", TimeWindow{9, 17, nil}, "2026-01-05T08:00:00Z", hourly, "2026-01-05T09:00:00Z", true},
		{"already inside still advances", "ignored", TimeWindow{9, 17, nil}, "2026-01-05T10:00:00Z", hourly, "2026-01-05T11:00:00Z", true},
		{"skip until allowed weekday", "ignored", TimeWindow{9, 17, []time.Weekday{time.Monday}}, "2026-01-01T08:00:00Z", hourly, "2026-01-05T09:00:00Z", true},
		{"no window still advances once", "ignored", TimeWindow{}, "2026-01-05T08:00:00Z", hourly, "2026-01-05T09:00:00Z", true},
		{"CJK expr is irrelevant with injected next", "中文任务表达式", TimeWindow{9, 17, nil}, "2026-01-05T08:00:00Z", hourly, "2026-01-05T09:00:00Z", true},
		{"injected next overrides invalid expr", "bad-expression", TimeWindow{}, "2026-01-05T08:00:00Z", hourly, "2026-01-05T09:00:00Z", true},
		{"nil next uses cron expr", "*/15 * * * *", TimeWindow{9, 17, nil}, "2026-01-05T08:55:00Z", nil, "2026-01-05T09:00:00Z", true},
		{"invalid expr with nil next fails", "bad-expression", TimeWindow{}, "2026-01-05T08:00:00Z", nil, "", false},
		{"zero now fails", "ignored", TimeWindow{}, "0001-01-01T00:00:00Z", hourly, "", false},
		{"stalled next fails", "ignored", TimeWindow{}, "2026-01-05T08:00:00Z", stalled, "", false},
		{"regressing next fails", "ignored", TimeWindow{}, "2026-01-05T08:00:00Z", regressing, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := NextInWindow(tc.expr, tc.w, windowTime(t, tc.now), tc.next)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if tc.wantOK {
				want := windowTime(t, tc.want)
				if !got.Equal(want) {
					t.Fatalf("NextInWindow = %s, want %s", got, want)
				}
				if !tc.w.Contains(got) {
					t.Fatalf("NextInWindow = %s, which is outside %+v", got, tc.w)
				}
			} else if !got.IsZero() {
				t.Fatalf("failure should return zero time, got %s", got)
			}
		})
	}
}

// TestNextInWindowDeterministic 验证注入的 next 决定推进过程：调用次数精确可数，
// 重复调用得到完全相同的结果。
func TestNextInWindowDeterministic(t *testing.T) {
	run := func() (time.Time, int) {
		calls := 0
		next := func(v time.Time) time.Time {
			calls++
			return v.Add(time.Hour)
		}
		got, ok := NextInWindow("ignored", TimeWindow{12, 13, nil}, windowTime(t, "2026-01-05T08:00:00Z"), next)
		if !ok {
			t.Fatalf("NextInWindow should find 12:00")
		}
		return got, calls
	}
	first, firstCalls := run()
	second, secondCalls := run()
	want := windowTime(t, "2026-01-05T12:00:00Z")
	if !first.Equal(want) || !second.Equal(want) {
		t.Fatalf("results = %s, %s; want %s", first, second, want)
	}
	if firstCalls != 4 || secondCalls != 4 {
		t.Fatalf("advance calls = %d, %d; want 4 each", firstCalls, secondCalls)
	}
}

// TestNextInWindowBounded 验证 next 一直前进但窗口永不匹配时，推进仍受步数上限约束。
func TestNextInWindowBounded(t *testing.T) {
	calls := 0
	next := func(v time.Time) time.Time {
		calls++
		return v.Add(time.Minute)
	}
	// 星期 7 不是合法的 Weekday，因此 Contains 永不成立。
	w := TimeWindow{Days: []time.Weekday{time.Weekday(7)}}
	got, ok := NextInWindow("ignored", w, windowTime(t, "2026-01-05T00:00:00Z"), next)
	if ok || !got.IsZero() {
		t.Fatalf("unreachable window = (%s, %v), want (zero, false)", got, ok)
	}
	if calls != nextInWindowMaxSteps {
		t.Fatalf("advance calls = %d, want %d", calls, nextInWindowMaxSteps)
	}
}
