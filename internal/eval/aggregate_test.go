package eval

import (
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// scored 是一个辅助函数，构建具有选定时长的结构有效 Result。
// 无效结果的情况从它开始，然后破坏一个字段。
func scored(index int, passed bool, reason string, d time.Duration) Result {
	return Result{Index: index, Passed: passed, Reason: reason, Turns: 1, Duration: d}
}

func TestAggregateScoresEmptyInput(t *testing.T) {
	want := Summary{FailureCounts: map[string]int{}}
	for _, tc := range []struct {
		name    string
		results []Result
	}{
		{"nil slice", nil},
		{"empty slice", []Result{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := AggregateScores(tc.results)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("AggregateScores(%v) = %+v, want %+v", tc.results, got, want)
			}
			if math.IsNaN(got.PassRate) || math.IsInf(got.PassRate, 0) {
				t.Fatalf("empty input produced a non-finite PassRate: %v", got.PassRate)
			}
		})
	}
}

func TestAggregateScoresPassRateCountsAndDuration(t *testing.T) {
	tests := []struct {
		name         string
		results      []Result
		wantPassed   int
		wantFailed   int
		wantRate     float64
		wantDuration time.Duration
	}{
		{
			name:         "single pass",
			results:      []Result{scored(1, true, "", 10*time.Millisecond)},
			wantPassed:   1,
			wantRate:     100,
			wantDuration: 10 * time.Millisecond,
		},
		{
			name:         "single failure",
			results:      []Result{scored(1, false, "bad tool", 20*time.Millisecond)},
			wantFailed:   1,
			wantRate:     0,
			wantDuration: 20 * time.Millisecond,
		},
		{
			name: "mixed",
			results: []Result{
				scored(1, true, "", 10*time.Millisecond),
				scored(2, false, "bad text", 20*time.Millisecond),
				scored(3, true, "", 30*time.Millisecond),
			},
			wantPassed:   2,
			wantFailed:   1,
			wantRate:     float64(2) * 100 / 3,
			wantDuration: 20 * time.Millisecond,
		},
		{
			name: "zero durations are included in the mean",
			results: []Result{
				scored(1, true, "", 0),
				scored(2, false, "bad", 0),
			},
			wantPassed:   1,
			wantFailed:   1,
			wantRate:     50,
			wantDuration: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AggregateScores(tc.results)
			if got.Total != len(tc.results) || got.Valid != len(tc.results) || got.Skipped != 0 {
				t.Fatalf("total/valid/skipped = %d/%d/%d, want %d/%d/0",
					got.Total, got.Valid, got.Skipped, len(tc.results), len(tc.results))
			}
			if got.Passed != tc.wantPassed || got.Failed != tc.wantFailed {
				t.Fatalf("passed/failed = %d/%d, want %d/%d",
					got.Passed, got.Failed, tc.wantPassed, tc.wantFailed)
			}
			if math.Abs(got.PassRate-tc.wantRate) > 1e-9 {
				t.Fatalf("PassRate = %v, want %v", got.PassRate, tc.wantRate)
			}
			if got.AverageDuration != tc.wantDuration {
				t.Fatalf("AverageDuration = %v, want %v", got.AverageDuration, tc.wantDuration)
			}
		})
	}
}

func TestAggregateScoresIgnoresInvalidResults(t *testing.T) {
	zeroIndex := scored(0, true, "", time.Hour) // ungraded record
	badTurns := scored(2, true, "", time.Hour)
	badTurns.Turns = -1
	badDuration := scored(3, false, "invalid duration", -time.Second)
	results := []Result{
		scored(1, true, "", 10*time.Millisecond), // valid pass
		zeroIndex,
		badTurns,
		badDuration,
		scored(4, false, "answer missing", 30*time.Millisecond), // valid failure
	}

	got := AggregateScores(results)
	if got.Total != 5 || got.Valid != 2 || got.Skipped != 3 {
		t.Fatalf("total/valid/skipped = %d/%d/%d, want 5/2/3", got.Total, got.Valid, got.Skipped)
	}
	if got.Passed != 1 || got.Failed != 1 || got.PassRate != 50 {
		t.Fatalf("passed/failed/rate = %d/%d/%v, want 1/1/50", got.Passed, got.Failed, got.PassRate)
	}
	if got.AverageDuration != 20*time.Millisecond {
		t.Fatalf("AverageDuration = %v, want 20ms (invalid durations must not leak in)", got.AverageDuration)
	}
	wantCounts := map[string]int{"answermissing": 1} // whitespace stripped from the key
	if !reflect.DeepEqual(got.FailureCounts, wantCounts) {
		t.Fatalf("FailureCounts = %v, want %v", got.FailureCounts, wantCounts)
	}
}

func TestAggregateScoresCJKFailureReasons(t *testing.T) {
	long := strings.Repeat("答案不完整", 30) // 150 runes, must be clipped
	results := []Result{
		scored(1, false, "答案不包含“发布说明”", time.Millisecond),
		scored(2, false, "答案不包含   “发布说明”", time.Millisecond), // same after whitespace collapse
		scored(3, false, "工具调用失败：无法读取文件", time.Millisecond),
		scored(4, false, long, time.Millisecond),
	}
	got := AggregateScores(results)

	if got.Failed != 4 || len(got.FailureCounts) != 3 {
		t.Fatalf("failed = %d, distinct reasons = %d, want 4/3 (%v)",
			got.Failed, len(got.FailureCounts), got.FailureCounts)
	}
	if n := got.FailureCounts["答案不包含“发布说明”"]; n != 2 {
		t.Fatalf("collapsed CJK reason count = %d, want 2", n)
	}
	if n := got.FailureCounts["工具调用失败：无法读取文件"]; n != 1 {
		t.Fatalf("CJK reason count = %d, want 1", n)
	}
	for key := range got.FailureCounts {
		if !utf8.ValidString(key) {
			t.Fatalf("FailureCounts key is not valid UTF-8: %q", key)
		}
	}
	clipKey := failureReasonKey(long)
	if !utf8.ValidString(clipKey) || utf8.RuneCountInString(clipKey) != failureReasonKeyRunes {
		t.Fatalf("clipped key = %q (runes=%d, valid=%t), want %d runes of valid UTF-8",
			clipKey, utf8.RuneCountInString(clipKey), utf8.ValidString(clipKey), failureReasonKeyRunes)
	}
	if got.FailureCounts[clipKey] != 1 {
		t.Fatalf("long CJK reason was not bucketed under its clipped key")
	}
}

func TestAggregateScoresOrderIndependent(t *testing.T) {
	base := []Result{
		scored(1, true, "", 5*time.Millisecond),
		scored(2, false, "工具调用失败", 15*time.Millisecond),
		scored(3, true, "", 25*time.Millisecond),
		scored(4, false, "expected 2 turns, the model was called 3 times", 35*time.Millisecond),
		scored(5, false, "工具调用失败", 45*time.Millisecond),
	}
	want := AggregateScores(base)

	reversed := make([]Result, len(base))
	for i, r := range base {
		reversed[len(base)-1-i] = r
	}
	if got := AggregateScores(reversed); !reflect.DeepEqual(got, want) {
		t.Fatalf("reversed order changed the summary:\n got %+v\nwant %+v", got, want)
	}

	// 从并发 worker 收集的结果以不确定的
	// 顺序到达；聚合结果仍必须完全相同。
	ch := make(chan Result, len(base))
	var wg sync.WaitGroup
	for _, r := range base {
		wg.Add(1)
		go func(r Result) {
			defer wg.Done()
			ch <- r
		}(r)
	}
	wg.Wait()
	close(ch)
	var collected []Result
	for r := range ch {
		collected = append(collected, r)
	}
	if got := AggregateScores(collected); !reflect.DeepEqual(got, want) {
		t.Fatalf("concurrent collection changed the summary:\n got %+v\nwant %+v", got, want)
	}
}

func TestAggregateScoresDoesNotMutateInput(t *testing.T) {
	results := []Result{
		scored(1, false, "失败 原因", time.Millisecond),
		scored(2, true, "", time.Millisecond),
	}
	before := append([]Result(nil), results...)
	AggregateScores(results)
	if !reflect.DeepEqual(results, before) {
		t.Fatalf("AggregateScores mutated its input:\n got %+v\nwant %+v", results, before)
	}
}
