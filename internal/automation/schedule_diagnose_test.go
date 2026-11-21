package automation

import (
	"reflect"
	"testing"
	"time"
)

// TestDiagnoseWindowReasons 表驱动验证各 Reason 的稳定取值与 InWindow 一致性。
// 2026-01-05 是星期一，因此星期过滤用例可据此推定。
func TestDiagnoseWindowReasons(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	// 上海本地时间：东八区，本地 10 点约为 UTC 2 点，用于验证按时刻自身时区判断。
	inShanghai := func(hour int) time.Time {
		return time.Date(2026, 1, 5, hour, 30, 0, 0, shanghai)
	}

	cases := []struct {
		name       string
		w          TimeWindow
		at         time.Time
		wantIn     bool
		wantReason string
	}{
		{"normal inside lower bound", TimeWindow{9, 17, nil}, windowTime(t, "2026-01-05T09:00:00Z"), true, reasonWindowAllowed},
		{"normal inside upper bound excluded", TimeWindow{9, 17, nil}, windowTime(t, "2026-01-05T16:59:00Z"), true, reasonWindowAllowed},
		{"normal hour at upper bound", TimeWindow{9, 17, nil}, windowTime(t, "2026-01-05T17:00:00Z"), false, reasonHourOutOfRange},
		{"normal hour before lower bound", TimeWindow{9, 17, nil}, windowTime(t, "2026-01-05T08:59:00Z"), false, reasonHourOutOfRange},
		{"cross midnight late half", TimeWindow{22, 6, nil}, windowTime(t, "2026-01-05T23:30:00Z"), true, reasonWindowAllowed},
		{"cross midnight across zero", TimeWindow{22, 6, nil}, windowTime(t, "2026-01-06T00:00:00Z"), true, reasonWindowAllowed},
		{"cross midnight upper bound excluded", TimeWindow{22, 6, nil}, windowTime(t, "2026-01-06T06:00:00Z"), false, reasonHourOutOfRange},
		{"cross midnight before lower bound", TimeWindow{22, 6, nil}, windowTime(t, "2026-01-05T21:59:00Z"), false, reasonHourOutOfRange},
		{"allowed weekday", TimeWindow{9, 17, []time.Weekday{time.Monday, time.Wednesday}}, windowTime(t, "2026-01-05T10:00:00Z"), true, reasonWindowAllowed},
		{"weekday not allowed", TimeWindow{9, 17, []time.Weekday{time.Monday, time.Wednesday}}, windowTime(t, "2026-01-06T10:00:00Z"), false, reasonWeekdayNotAllowed},
		{"hour wins when both filters fail", TimeWindow{9, 17, []time.Weekday{time.Monday}}, windowTime(t, "2026-01-06T18:00:00Z"), false, reasonHourOutOfRange},
		{"zero window has no constraint", TimeWindow{}, windowTime(t, "2026-01-05T03:00:00Z"), true, reasonWindowAllowed},
		{"equal endpoints mean whole day", TimeWindow{9, 9, nil}, windowTime(t, "2026-01-05T03:00:00Z"), true, reasonWindowAllowed},
		{"impossible weekday", TimeWindow{Days: []time.Weekday{time.Weekday(7)}}, windowTime(t, "2026-01-05T10:00:00Z"), false, reasonWeekdayNotAllowed},
		{"zero time", TimeWindow{}, time.Time{}, false, reasonZeroTime},
		{"CJK zone inside window", TimeWindow{9, 17, nil}, inShanghai(10), true, reasonWindowAllowed},
		{"CJK zone outside window", TimeWindow{9, 17, nil}, inShanghai(18), false, reasonHourOutOfRange},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DiagnoseWindow(tc.w, []time.Time{tc.at})
			if len(got) != 1 {
				t.Fatalf("len = %d, want 1", len(got))
			}
			check := got[0]
			if !check.At.Equal(tc.at) {
				t.Fatalf("At = %s, want %s", check.At, tc.at)
			}
			if check.At.Location() != tc.at.Location() {
				t.Fatalf("At location = %v, want %v", check.At.Location(), tc.at.Location())
			}
			if check.InWindow != tc.wantIn {
				t.Fatalf("InWindow = %v, want %v", check.InWindow, tc.wantIn)
			}
			if check.Reason != tc.wantReason {
				t.Fatalf("Reason = %q, want %q", check.Reason, tc.wantReason)
			}
			// 不变量：InWindow 必须与 Contains 的判定完全一致。
			if check.InWindow != tc.w.Contains(tc.at) {
				t.Fatalf("InWindow = %v but Contains = %v for %+v at %s", check.InWindow, tc.w.Contains(tc.at), tc.w, tc.at)
			}
		})
	}
}

// TestDiagnoseWindowOrderAndPurity 验证按输入顺序输出、不排序、不改入参，
// 且对同一输入重复调用得到完全相同的结果。
func TestDiagnoseWindowOrderAndPurity(t *testing.T) {
	w := TimeWindow{9, 17, []time.Weekday{time.Monday, time.Wednesday}}
	times := []time.Time{
		windowTime(t, "2026-01-05T18:00:00Z"), // 小时越界
		windowTime(t, "2026-01-06T10:00:00Z"), // 星期不允许
		windowTime(t, "2026-01-05T10:00:00Z"), // 在允许时段
		{},                                    // 零时刻
		windowTime(t, "2026-01-07T10:00:00Z"), // 星期三，允许
	}
	original := append([]time.Time(nil), times...)

	got := DiagnoseWindow(w, times)
	again := DiagnoseWindow(w, times)

	if !reflect.DeepEqual(got, again) {
		t.Fatalf("repeated calls differ:\nfirst  = %+v\nsecond = %+v", got, again)
	}
	if !reflect.DeepEqual(times, original) {
		t.Fatalf("input slice was modified:\ngot  = %+v\nwant = %+v", times, original)
	}
	wantReasons := []string{
		reasonHourOutOfRange,
		reasonWeekdayNotAllowed,
		reasonWindowAllowed,
		reasonZeroTime,
		reasonWindowAllowed,
	}
	if len(got) != len(wantReasons) {
		t.Fatalf("len = %d, want %d", len(got), len(wantReasons))
	}
	for i, check := range got {
		if !check.At.Equal(times[i]) || check.At.Location() != times[i].Location() {
			t.Fatalf("result[%d].At = %s (%v), want %s (%v)", i, check.At, check.At.Location(), times[i], times[i].Location())
		}
		if check.Reason != wantReasons[i] {
			t.Fatalf("result[%d].Reason = %q, want %q", i, check.Reason, wantReasons[i])
		}
	}
}

// TestDiagnoseWindowEmpty 验证空输入返回非 nil 的空切片。
func TestDiagnoseWindowEmpty(t *testing.T) {
	got := DiagnoseWindow(TimeWindow{}, nil)
	if got == nil {
		t.Fatal("nil input should return a non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}

// TestNextAllowedInWindow 表驱动验证按 expr 推进到窗口内的下一次触发。
func TestNextAllowedInWindow(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}

	cases := []struct {
		name   string
		w      TimeWindow
		from   time.Time
		expr   string
		want   time.Time
		wantOK bool
	}{
		{
			name:   "cron advances into window",
			w:      TimeWindow{9, 17, nil},
			from:   windowTime(t, "2026-01-05T08:00:00Z"),
			expr:   "0 * * * *",
			want:   windowTime(t, "2026-01-05T09:00:00Z"),
			wantOK: true,
		},
		{
			name:   "already inside still advances",
			w:      TimeWindow{9, 17, nil},
			from:   windowTime(t, "2026-01-05T10:00:00Z"),
			expr:   "0 * * * *",
			want:   windowTime(t, "2026-01-05T11:00:00Z"),
			wantOK: true,
		},
		{
			name:   "cross midnight window",
			w:      TimeWindow{22, 6, nil},
			from:   windowTime(t, "2026-01-05T20:00:00Z"),
			expr:   "0 * * * *",
			want:   windowTime(t, "2026-01-05T22:00:00Z"),
			wantOK: true,
		},
		{
			name:   "skip until allowed weekday",
			w:      TimeWindow{9, 17, []time.Weekday{time.Monday}},
			from:   windowTime(t, "2026-01-02T08:00:00Z"), // 星期五
			expr:   "0 9 * * 1-5",
			want:   windowTime(t, "2026-01-05T09:00:00Z"), // 星期一
			wantOK: true,
		},
		{
			name:   "zero window still advances once",
			w:      TimeWindow{},
			from:   windowTime(t, "2026-01-05T08:55:00Z"),
			expr:   "*/15 * * * *",
			want:   windowTime(t, "2026-01-05T09:00:00Z"),
			wantOK: true,
		},
		{
			name:   "CJK zone is honored",
			w:      TimeWindow{9, 17, nil},
			from:   time.Date(2026, 1, 5, 8, 55, 0, 0, shanghai),
			expr:   "0 9 * * *",
			want:   time.Date(2026, 1, 5, 9, 0, 0, 0, shanghai),
			wantOK: true,
		},
		{
			name:   "invalid expr fails",
			w:      TimeWindow{},
			from:   windowTime(t, "2026-01-05T08:00:00Z"),
			expr:   "bad-expression",
			wantOK: false,
		},
		{
			name:   "CJK expr is handled without panic",
			w:      TimeWindow{},
			from:   windowTime(t, "2026-01-05T08:00:00Z"),
			expr:   "中文任务表达式",
			wantOK: false,
		},
		{
			name:   "zero from fails",
			w:      TimeWindow{},
			from:   time.Time{},
			expr:   "0 * * * *",
			wantOK: false,
		},
		{
			name:   "unreachable window fails",
			w:      TimeWindow{Days: []time.Weekday{time.Weekday(7)}},
			from:   windowTime(t, "2026-01-05T00:00:00Z"),
			expr:   "0 * * * *",
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from := tc.from
			got, ok := NextAllowedInWindow(tc.w, from, tc.expr)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if tc.wantOK {
				if !got.Equal(tc.want) {
					t.Fatalf("NextAllowedInWindow = %s, want %s", got, tc.want)
				}
				if !got.After(from) {
					t.Fatalf("result %s is not strictly after from %s", got, from)
				}
				if !tc.w.Contains(got) {
					t.Fatalf("result %s is outside %+v", got, tc.w)
				}
			} else if !got.IsZero() {
				t.Fatalf("failure should return zero time, got %s", got)
			}
			// 入参不得被修改。
			if !from.Equal(tc.from) || from.Location() != tc.from.Location() {
				t.Fatalf("from was modified: got %s (%v), want %s (%v)", from, from.Location(), tc.from, tc.from.Location())
			}
		})
	}
}

// TestNextAllowedInWindowDeterministic 验证重复调用得到完全相同的结果。
func TestNextAllowedInWindowDeterministic(t *testing.T) {
	w := TimeWindow{9, 17, []time.Weekday{time.Monday}}
	from := windowTime(t, "2026-01-02T08:00:00Z")
	expr := "0 9 * * 1-5"

	first, firstOK := NextAllowedInWindow(w, from, expr)
	second, secondOK := NextAllowedInWindow(w, from, expr)
	if firstOK != secondOK || !first.Equal(second) {
		t.Fatalf("results differ: (%s, %v) vs (%s, %v)", first, firstOK, second, secondOK)
	}
	want := windowTime(t, "2026-01-05T09:00:00Z")
	if !firstOK || !first.Equal(want) {
		t.Fatalf("result = (%s, %v), want (%s, true)", first, firstOK, want)
	}
}
