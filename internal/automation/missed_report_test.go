package automation

import (
	"reflect"
	"testing"
	"time"
)

// TestReportMissedRunsByPolicy 表驱动验证三种策略、窗口内外、跨午夜与星期过滤下的
// WillRun 与稳定中文 Reason。期望按输入顺序逐条给出，便于核对选中时隙的唯一性。
func TestReportMissedRunsByPolicy(t *testing.T) {
	// 2026-01-05 是星期一。now 取在多个时隙之后，便于模拟长时间停机后的补偿。
	fullDay := TimeWindow{} // 零值窗口不施加约束

	cases := []struct {
		name    string
		slots   []string
		now     string
		policy  string
		w       TimeWindow
		wantRun []bool
		wantWhy []string
	}{
		{
			name:    "skip discards every missed slot regardless of window",
			slots:   []string{"08:00", "10:00", "13:00"},
			now:     "13:07",
			policy:  MissedSkip,
			w:       fullDay,
			wantRun: []bool{false, false, false},
			wantWhy: []string{reasonMissedSkipped, reasonMissedSkipped, reasonMissedSkipped},
		},
		{
			name:    "skip ignores an out of window slot",
			slots:   []string{"08:00", "10:00"},
			now:     "13:07",
			policy:  MissedSkip,
			w:       TimeWindow{9, 17, nil},
			wantRun: []bool{false, false},
			wantWhy: []string{reasonMissedSkipped, reasonMissedSkipped},
		},
		{
			name:    "run_once runs only the earliest missed slot",
			slots:   []string{"08:00", "09:00", "10:00", "11:00", "12:00", "13:00"},
			now:     "13:07",
			policy:  MissedRunOnce,
			w:       fullDay,
			wantRun: []bool{true, false, false, false, false, false},
			wantWhy: []string{
				reasonMissedRunOnce,
				reasonMissedRunOnceIdle, reasonMissedRunOnceIdle,
				reasonMissedRunOnceIdle, reasonMissedRunOnceIdle, reasonMissedRunOnceIdle,
			},
		},
		{
			name:    "run_latest runs only the most recent missed slot",
			slots:   []string{"08:00", "09:00", "10:00", "11:00", "12:00", "13:00"},
			now:     "13:07",
			policy:  MissedRunLatest,
			w:       fullDay,
			wantRun: []bool{false, false, false, false, false, true},
			wantWhy: []string{
				reasonMissedRunLatestIdle, reasonMissedRunLatestIdle,
				reasonMissedRunLatestIdle, reasonMissedRunLatestIdle,
				reasonMissedRunLatestIdle, reasonMissedRunLatest,
			},
		},
		{
			name:    "run_once picks the earliest slot inside the window",
			slots:   []string{"08:00", "10:00", "12:00"},
			now:     "13:07",
			policy:  MissedRunOnce,
			w:       TimeWindow{9, 17, nil},
			wantRun: []bool{false, true, false},
			wantWhy: []string{reasonMissedOutOfWindow, reasonMissedRunOnce, reasonMissedRunOnceIdle},
		},
		{
			name:    "run_latest picks the most recent slot inside the window",
			slots:   []string{"08:00", "10:00", "16:00", "18:00"},
			now:     "19:00",
			policy:  MissedRunLatest,
			w:       TimeWindow{9, 17, nil},
			wantRun: []bool{false, false, true, false},
			wantWhy: []string{reasonMissedOutOfWindow, reasonMissedRunLatestIdle, reasonMissedRunLatest, reasonMissedOutOfWindow},
		},
		{
			name:    "no slot inside window leaves nothing to run",
			slots:   []string{"08:00", "18:00"},
			now:     "19:00",
			policy:  MissedRunLatest,
			w:       TimeWindow{9, 17, nil},
			wantRun: []bool{false, false},
			wantWhy: []string{reasonMissedOutOfWindow, reasonMissedOutOfWindow},
		},
		{
			name:   "cross midnight window keeps the late and early halves",
			slots:  []string{"2026-01-05T21:00:00Z", "2026-01-05T23:30:00Z", "2026-01-06T00:30:00Z", "2026-01-06T06:00:00Z", "2026-01-06T07:00:00Z"},
			now:    "2026-01-06T07:30:00Z",
			policy: MissedRunLatest,
			w:      TimeWindow{22, 6, nil},
			// 23:30 与 00:30 在窗口内，最近的一个 00:30 补跑；21:00、06:00（上界开区间）、07:00 均越界。
			wantRun: []bool{false, false, true, false, false},
			wantWhy: []string{
				reasonMissedOutOfWindow, reasonMissedRunLatestIdle, reasonMissedRunLatest,
				reasonMissedOutOfWindow, reasonMissedOutOfWindow,
			},
		},
		{
			name:   "weekday filter excludes non allowed days",
			slots:  []string{"2026-01-05T10:00:00Z", "2026-01-06T10:00:00Z", "2026-01-07T09:30:00Z"},
			now:    "2026-01-07T10:00:00Z",
			policy: MissedRunOnce,
			// 仅星期一允许：星期一在窗口内，星期二与星期三被星期过滤排除。
			w:       TimeWindow{9, 17, []time.Weekday{time.Monday}},
			wantRun: []bool{true, false, false},
			wantWhy: []string{reasonMissedRunOnce, reasonMissedOutOfWindow, reasonMissedOutOfWindow},
		},
		{
			name:    "policy is normalized before deciding",
			slots:   []string{"08:00", "13:00"},
			now:     "13:07",
			policy:  " RUN_LATEST ",
			w:       fullDay,
			wantRun: []bool{false, true},
			wantWhy: []string{reasonMissedRunLatestIdle, reasonMissedRunLatest},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			slots := make([]time.Time, 0, len(tc.slots))
			for _, slot := range tc.slots {
				slots = append(slots, parseReportTime(t, slot))
			}
			now := parseReportTime(t, tc.now)

			got := ReportMissedRuns(slots, now, tc.policy, tc.w)
			if len(got) != len(tc.wantRun) {
				t.Fatalf("len = %d, want %d", len(got), len(tc.wantRun))
			}
			effective := normalizeMissed(tc.policy)
			for i, report := range got {
				if !report.Slot.Equal(slots[i]) || report.Slot.Location() != slots[i].Location() {
					t.Fatalf("result[%d].Slot = %s (%v), want %s (%v)", i, report.Slot, report.Slot.Location(), slots[i], slots[i].Location())
				}
				if report.Policy != effective {
					t.Fatalf("result[%d].Policy = %q, want %q", i, report.Policy, effective)
				}
				if report.WillRun != tc.wantRun[i] {
					t.Fatalf("result[%d].WillRun = %v, want %v", i, report.WillRun, tc.wantRun[i])
				}
				if report.Reason != tc.wantWhy[i] {
					t.Fatalf("result[%d].Reason = %q, want %q", i, report.Reason, tc.wantWhy[i])
				}
				// 不变量：Reason 必须是中文字符串。
				if !containsCJK(report.Reason) {
					t.Fatalf("result[%d].Reason = %q is not Chinese", i, report.Reason)
				}
			}
		})
	}
}

// TestReportMissedRunsOrderAndPurity 验证只报告早于 now 的时隙、保持输入相对顺序、
// 不修改入参，且重复调用得到完全相同的结果。
func TestReportMissedRunsOrderAndPurity(t *testing.T) {
	slots := []time.Time{
		windowTime(t, "2026-01-05T12:00:00Z"), // 窗口内，较晚
		windowTime(t, "2026-01-05T14:00:00Z"), // 不早于 now，应被排除
		windowTime(t, "2026-01-05T09:00:00Z"), // 窗口内，较早
		windowTime(t, "2026-01-05T08:00:00Z"), // 窗口外
	}
	original := append([]time.Time(nil), slots...)
	now := windowTime(t, "2026-01-05T13:07:00Z")
	w := TimeWindow{9, 17, nil}

	got := ReportMissedRuns(slots, now, MissedRunOnce, w)
	again := ReportMissedRuns(slots, now, MissedRunOnce, w)

	if !reflect.DeepEqual(got, again) {
		t.Fatalf("repeated calls differ:\nfirst  = %+v\nsecond = %+v", got, again)
	}
	if !reflect.DeepEqual(slots, original) {
		t.Fatalf("input slice was modified:\ngot  = %+v\nwant = %+v", slots, original)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (the 14:00 slot is not missed)", len(got))
	}
	// 顺序与输入一致：12:00（最早的窗口内时隙，补跑）、09:00、08:00。
	wantRun := []bool{true, false, false}
	wantWhy := []string{reasonMissedRunOnce, reasonMissedRunOnceIdle, reasonMissedOutOfWindow}
	for i, report := range got {
		if report.WillRun != wantRun[i] || report.Reason != wantWhy[i] {
			t.Fatalf("result[%d] = (%v, %q), want (%v, %q)", i, report.WillRun, report.Reason, wantRun[i], wantWhy[i])
		}
	}
	// 显式核对每条 Slot 对应的输入时隙，确认未排序。
	wantOrder := []string{"2026-01-05T12:00:00Z", "2026-01-05T09:00:00Z", "2026-01-05T08:00:00Z"}
	for i, report := range got {
		if want := windowTime(t, wantOrder[i]); !report.Slot.Equal(want) {
			t.Fatalf("result[%d].Slot = %s, want %s (output was reordered)", i, report.Slot, want)
		}
	}
}

// TestReportMissedRunsBoundary 验证时隙恰好等于 now、零时刻 now 与零时刻时隙的处理。
func TestReportMissedRunsBoundary(t *testing.T) {
	onSlot := windowTime(t, "2026-01-05T10:00:00Z")
	before := windowTime(t, "2026-01-05T09:00:00Z")

	// slot == now（不算错过）与零时刻时隙（无效占位）都不进入报告。
	if got := ReportMissedRuns([]time.Time{onSlot, time.Time{}}, onSlot, MissedRunLatest, TimeWindow{}); len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}

	// 只有真正早于 now 的时隙参与判定并补跑。
	got := ReportMissedRuns([]time.Time{before, onSlot, time.Time{}}, onSlot, MissedRunLatest, TimeWindow{})
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if !got[0].Slot.Equal(before) {
		t.Fatalf("slot = %s, want %s", got[0].Slot, before)
	}
	if !got[0].WillRun || got[0].Reason != reasonMissedRunLatest {
		t.Fatalf("result = (%v, %q), want (true, %q)", got[0].WillRun, got[0].Reason, reasonMissedRunLatest)
	}

	// 零时刻 now 早于任何真实时隙，不会产生错过报告。
	if got := ReportMissedRuns([]time.Time{before}, time.Time{}, MissedRunOnce, TimeWindow{}); len(got) != 0 {
		t.Fatalf("zero now should report nothing, got %+v", got)
	}
}

// TestReportMissedRunsEmpty 验证空输入返回非 nil 的空切片。
func TestReportMissedRunsEmpty(t *testing.T) {
	for _, slots := range [][]time.Time{nil, {}} {
		got := ReportMissedRuns(slots, windowTime(t, "2026-01-05T10:00:00Z"), MissedRunOnce, TimeWindow{})
		if got == nil {
			t.Fatal("empty input should return a non-nil empty slice")
		}
		if len(got) != 0 {
			t.Fatalf("len = %d, want 0", len(got))
		}
	}
}

// TestReportMissedRunsCJKZone 验证窗口按时隙自身时区判断：本地 20 点在 UTC 是 12 点，
// 若误用 UTC 会被错判为窗口内；正确的实现应把它排除。
func TestReportMissedRunsCJKZone(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	at := func(hour int) time.Time {
		return time.Date(2026, 1, 5, hour, 0, 0, 0, shanghai)
	}
	slots := []time.Time{at(10), at(16), at(20)} // 本地 10/16/20 点
	now := at(21)                                // 本地 21 点

	got := ReportMissedRuns(slots, now, MissedRunLatest, TimeWindow{9, 17, nil})
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].WillRun || got[0].Reason != reasonMissedRunLatestIdle {
		t.Fatalf("local 10:00 = (%v, %q)", got[0].WillRun, got[0].Reason)
	}
	if !got[1].WillRun || got[1].Reason != reasonMissedRunLatest {
		t.Fatalf("local 16:00 = (%v, %q), want the latest in-window run", got[1].WillRun, got[1].Reason)
	}
	// 本地 20:00（UTC 12:00）必须判为窗口外，证明按本地挂钟小时而非 UTC 判断。
	if got[2].WillRun || got[2].Reason != reasonMissedOutOfWindow {
		t.Fatalf("local 20:00 = (%v, %q), want out of window", got[2].WillRun, got[2].Reason)
	}
}

// parseReportTime 解析 "15:04" 或完整 RFC3339 字符串；仅有时间时补齐基准日期 2026-01-05。
func parseReportTime(t *testing.T, value string) time.Time {
	t.Helper()
	if len(value) == len("15:04") {
		value = "2026-01-05T" + value + ":00Z"
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return parsed
}

// containsCJK 报告字符串是否包含 CJK 汉字，用于校验 Reason 的稳定中文取值。
func containsCJK(s string) bool {
	for _, r := range s {
		if r >= 0x4e00 && r <= 0x9fff {
			return true
		}
	}
	return false
}
