package automation

import (
	"reflect"
	"testing"
)

// TestAssessScheduleVerdicts 表驱动验证三种 Verdict 的判定与优先级边界。
// 基准 LastRunMS=1000，阈值 1000，故 nowMS<=2000 不视为过期。
func TestAssessScheduleVerdicts(t *testing.T) {
	cases := []struct {
		name        string
		h           ScheduleHealth
		nowMS       int64
		staleAfter  int64
		wantVerdict string
	}{
		{
			name:        "fresh with no issues is healthy",
			h:           ScheduleHealth{LastRunMS: 1000},
			nowMS:       1500,
			staleAfter:  1000,
			wantVerdict: VerdictHealthy,
		},
		{
			name:        "age exactly at threshold is not stale",
			h:           ScheduleHealth{LastRunMS: 1000},
			nowMS:       2000,
			staleAfter:  1000,
			wantVerdict: VerdictHealthy,
		},
		{
			name:        "age one past threshold is late",
			h:           ScheduleHealth{LastRunMS: 1000},
			nowMS:       2001,
			staleAfter:  1000,
			wantVerdict: VerdictLate,
		},
		{
			name:        "missed alone on a fresh schedule is late",
			h:           ScheduleHealth{LastRunMS: 1000, Missed: 1},
			nowMS:       1500,
			staleAfter:  1000,
			wantVerdict: VerdictLate,
		},
		{
			name:        "many missed but fresh stays late not stuck",
			h:           ScheduleHealth{LastRunMS: 1000, Missed: 99},
			nowMS:       1500,
			staleAfter:  1000,
			wantVerdict: VerdictLate,
		},
		{
			name:        "single failure is late",
			h:           ScheduleHealth{LastRunMS: 1000, ConsecutiveFail: 1},
			nowMS:       1500,
			staleAfter:  1000,
			wantVerdict: VerdictLate,
		},
		{
			name:        "failure just below stuck threshold is late",
			h:           ScheduleHealth{LastRunMS: 1000, ConsecutiveFail: stuckFailThreshold - 1},
			nowMS:       1500,
			staleAfter:  1000,
			wantVerdict: VerdictLate,
		},
		{
			name:        "failure at stuck threshold is stuck",
			h:           ScheduleHealth{LastRunMS: 1000, ConsecutiveFail: stuckFailThreshold},
			nowMS:       1500,
			staleAfter:  1000,
			wantVerdict: VerdictStuck,
		},
		{
			name:        "failure above stuck threshold is stuck",
			h:           ScheduleHealth{LastRunMS: 1000, ConsecutiveFail: stuckFailThreshold + 2},
			nowMS:       1500,
			staleAfter:  1000,
			wantVerdict: VerdictStuck,
		},
		{
			name:        "stale with one missed is stuck",
			h:           ScheduleHealth{LastRunMS: 1000, Missed: 1},
			nowMS:       2001,
			staleAfter:  1000,
			wantVerdict: VerdictStuck,
		},
		{
			name:        "stale with failure but no missed is late",
			h:           ScheduleHealth{LastRunMS: 1000, ConsecutiveFail: 1},
			nowMS:       2001,
			staleAfter:  1000,
			wantVerdict: VerdictLate,
		},
		{
			name:        "clock skew is not stale",
			h:           ScheduleHealth{LastRunMS: 1000},
			nowMS:       500,
			staleAfter:  1000,
			wantVerdict: VerdictHealthy,
		},
		{
			name:        "clock skew with missed is late",
			h:           ScheduleHealth{LastRunMS: 1000, Missed: 1},
			nowMS:       500,
			staleAfter:  1000,
			wantVerdict: VerdictLate,
		},
		{
			name:        "stuck dominates missed on a fresh schedule",
			h:           ScheduleHealth{LastRunMS: 1000, Missed: 2, ConsecutiveFail: stuckFailThreshold},
			nowMS:       1500,
			staleAfter:  1000,
			wantVerdict: VerdictStuck,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AssessSchedule(tc.h, tc.nowMS, tc.staleAfter)
			if got.Verdict != tc.wantVerdict {
				t.Fatalf("Verdict = %q, want %q", got.Verdict, tc.wantVerdict)
			}
		})
	}
}

// TestAssessScheduleStaleThreshold 验证 staleAfterMS<=0 采用默认阈值，
// 以及显式阈值的严格大于边界。用 Verdict 区分是否过期（仅过期时变 late）。
func TestAssessScheduleStaleThreshold(t *testing.T) {
	const base int64 = 5_000_000

	cases := []struct {
		name       string
		staleAfter int64
		age        int64
		wantStale  bool
	}{
		{"explicit below threshold", 1000, 999, false},
		{"explicit at threshold", 1000, 1000, false},
		{"explicit one past threshold", 1000, 1001, true},
		{"zero uses default at threshold", 0, defaultStaleAfterMS, false},
		{"zero uses default one past", 0, defaultStaleAfterMS + 1, true},
		{"negative uses default at threshold", -1, defaultStaleAfterMS, false},
		{"negative uses default one past", -1, defaultStaleAfterMS + 1, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := ScheduleHealth{LastRunMS: base}
			got := AssessSchedule(h, base+tc.age, tc.staleAfter)
			want := VerdictHealthy
			if tc.wantStale {
				want = VerdictLate
			}
			if got.Verdict != want {
				t.Fatalf("age=%d staleAfter=%d: Verdict = %q, want %q", tc.age, tc.staleAfter, got.Verdict, want)
			}
		})
	}
}

// TestAssessScheduleDoesNotMutateInput 验证纯函数契约：入参按值传入不被修改，
// 除 Verdict 外的字段原样保留，CJK 表达式不受影响，重复调用结果一致。
func TestAssessScheduleDoesNotMutateInput(t *testing.T) {
	original := ScheduleHealth{
		ID:              "任务-42",
		Expression:      "*/5 * * * * 中文表达式",
		LastRunMS:       1000,
		Missed:          2,
		ConsecutiveFail: 1,
		Verdict:         "OLD",
	}
	input := original

	first := AssessSchedule(input, 9999, 1000)
	second := AssessSchedule(input, 9999, 1000)

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated calls differ:\nfirst  = %+v\nsecond = %+v", first, second)
	}
	if !reflect.DeepEqual(input, original) {
		t.Fatalf("input was modified:\ngot  = %+v\nwant = %+v", input, original)
	}

	// 除 Verdict 外所有字段必须与入参一致。
	first.Verdict = input.Verdict
	if !reflect.DeepEqual(first, input) {
		t.Fatalf("non-Verdict fields changed:\ngot  = %+v\nwant = %+v", first, input)
	}
	if first.Expression != original.Expression {
		t.Fatalf("CJK Expression changed: got %q, want %q", first.Expression, original.Expression)
	}
}

// TestAssessScheduleOverflow 验证超大间隔的溢出不会绕过过期判定。
func TestAssessScheduleOverflow(t *testing.T) {
	const maxInt64 = int64(^uint64(0) >> 1)
	got := AssessSchedule(ScheduleHealth{LastRunMS: -1}, maxInt64, 1000)
	if got.Verdict != VerdictLate {
		t.Fatalf("Verdict = %q, want %q (overflow delta must count as stale)", got.Verdict, VerdictLate)
	}
}

// TestAssessScheduleEmpty 验证零值入参得到确定结果且不会 panic。
func TestAssessScheduleEmpty(t *testing.T) {
	var empty ScheduleHealth
	got := AssessSchedule(empty, 0, 0)
	if got.Verdict != VerdictHealthy {
		t.Fatalf("Verdict = %q, want %q", got.Verdict, VerdictHealthy)
	}
	if !reflect.DeepEqual(got, ScheduleHealth{Verdict: VerdictHealthy}) {
		t.Fatalf("unexpected result: %+v", got)
	}
}

// TestScheduleHealthSummary 验证中文摘要的确切文本、稳定性、CJK 含汉字与占位符。
func TestScheduleHealthSummary(t *testing.T) {
	h := ScheduleHealth{
		ID:              "job-1",
		Expression:      "*/5 * * * *",
		LastRunMS:       123,
		Missed:          2,
		ConsecutiveFail: 1,
		Verdict:         VerdictLate,
	}
	want := "任务 job-1（*/5 * * * *）：判定 滞后；最近运行 123 毫秒；错过 2 次；连续失败 1 次"
	if got := h.Summary(); got != want {
		t.Fatalf("Summary = %q, want %q", got, want)
	}
	if h.Summary() != h.Summary() {
		t.Fatal("Summary is not deterministic")
	}
	if !containsCJK(h.Summary()) {
		t.Fatalf("Summary %q is not Chinese", h.Summary())
	}

	// 空字段以固定占位符呈现，仍稳定。
	empty := ScheduleHealth{Verdict: VerdictHealthy}
	wantEmpty := "任务 未命名任务（无表达式）：判定 健康；最近运行 0 毫秒；错过 0 次；连续失败 0 次"
	if got := empty.Summary(); got != wantEmpty {
		t.Fatalf("empty Summary = %q, want %q", got, wantEmpty)
	}

	// 未知 Verdict 原样保留。
	unknown := ScheduleHealth{Verdict: "unexpected"}
	if got := unknown.Summary(); got != "任务 未命名任务（无表达式）：判定 unexpected；最近运行 0 毫秒；错过 0 次；连续失败 0 次" {
		t.Fatalf("unknown verdict Summary = %q", got)
	}
}

// TestVerdictLabel 验证三种 Verdict 的中文标签映射。
func TestVerdictLabel(t *testing.T) {
	cases := map[string]string{
		VerdictHealthy: "健康",
		VerdictLate:    "滞后",
		VerdictStuck:   "卡死",
	}
	for verdict, want := range cases {
		if got := verdictLabel(verdict); got != want {
			t.Fatalf("verdictLabel(%q) = %q, want %q", verdict, got, want)
		}
	}
}
