package automation

import (
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// auditRun 用 RFC3339 时隙字符串构造一条 MissedRun，便于表驱动用例书写。
func auditRun(t *testing.T, value, policy string, willRun bool, reason string) MissedRun {
	t.Helper()
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return MissedRun{Slot: at, Policy: policy, WillRun: willRun, Reason: reason}
}

// rec 是表驱动用例里 MissedRun 的简写形态。
type rec struct {
	slot    string
	policy  string
	willRun bool
	reason  string
}

func toRuns(t *testing.T, recs []rec) []MissedRun {
	t.Helper()
	out := make([]MissedRun, 0, len(recs))
	for _, r := range recs {
		out = append(out, auditRun(t, r.slot, r.policy, r.willRun, r.reason))
	}
	return out
}

// TestMissedAuditRecordTimeline 表驱动验证 Record 的多批次按调用顺序展开为 Timeline，
// 且空批次不改变内容、零值可用。
func TestMissedAuditRecordTimeline(t *testing.T) {
	cases := []struct {
		name    string
		batches [][]rec
		want    []rec
	}{
		{
			name:    "zero value has empty timeline",
			batches: nil,
			want:    []rec{},
		},
		{
			name:    "empty record contributes nothing",
			batches: [][]rec{nil, {}, nil},
			want:    []rec{},
		},
		{
			name: "single batch preserves order",
			batches: [][]rec{{
				{"2026-01-05T08:00:00Z", MissedRunOnce, true, reasonMissedRunOnce},
				{"2026-01-05T09:00:00Z", MissedRunOnce, false, reasonMissedRunOnceIdle},
			}},
			want: []rec{
				{"2026-01-05T08:00:00Z", MissedRunOnce, true, reasonMissedRunOnce},
				{"2026-01-05T09:00:00Z", MissedRunOnce, false, reasonMissedRunOnceIdle},
			},
		},
		{
			name: "multiple batches flatten in record order",
			batches: [][]rec{
				{{"2026-01-05T08:00:00Z", MissedRunOnce, true, reasonMissedRunOnce}},
				{
					{"2026-01-05T09:00:00Z", MissedRunOnce, false, reasonMissedRunOnceIdle},
					{"2026-01-05T10:00:00Z", MissedSkip, false, reasonMissedSkipped},
				},
				{{"2026-01-05T11:00:00Z", MissedRunLatest, true, reasonMissedRunLatest}},
			},
			want: []rec{
				{"2026-01-05T08:00:00Z", MissedRunOnce, true, reasonMissedRunOnce},
				{"2026-01-05T09:00:00Z", MissedRunOnce, false, reasonMissedRunOnceIdle},
				{"2026-01-05T10:00:00Z", MissedSkip, false, reasonMissedSkipped},
				{"2026-01-05T11:00:00Z", MissedRunLatest, true, reasonMissedRunLatest},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var a MissedAudit
			for _, batch := range tc.batches {
				a.Record(toRuns(t, batch))
			}
			got := a.Timeline()
			want := toRuns(t, tc.want)
			if got == nil {
				t.Fatal("Timeline() returned nil, want non-nil slice")
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Timeline() = %+v, want %+v", got, want)
			}
		})
	}
}

// TestMissedAuditCounts 表驱动验证按 (WillRun, Reason) 分类计数，覆盖 CJK Reason
// 与空审计。期望键由 missedCountKey 构造，与实现意图一致且可稳定比较。
func TestMissedAuditCounts(t *testing.T) {
	cases := []struct {
		name string
		runs []rec
		want map[string]int
	}{
		{
			name: "empty audit returns empty map",
			runs: nil,
			want: map[string]int{},
		},
		{
			name: "classifies by willRun and reason",
			runs: []rec{
				{"2026-01-05T08:00:00Z", MissedRunOnce, true, reasonMissedRunOnce},
				{"2026-01-05T09:00:00Z", MissedRunOnce, false, reasonMissedRunOnceIdle},
				{"2026-01-05T10:00:00Z", MissedSkip, false, reasonMissedSkipped},
				{"2026-01-05T11:00:00Z", MissedRunOnce, true, reasonMissedRunOnce},
				{"2026-01-05T12:00:00Z", MissedRunLatest, false, reasonMissedOutOfWindow},
			},
			want: map[string]int{
				missedCountKey(true, reasonMissedRunOnce):      2,
				missedCountKey(false, reasonMissedRunOnceIdle): 1,
				missedCountKey(false, reasonMissedSkipped):     1,
				missedCountKey(false, reasonMissedOutOfWindow): 1,
			},
		},
		{
			name: "arbitrary CJK reason is counted verbatim",
			runs: []rec{
				{"2026-01-05T08:00:00Z", MissedRunOnce, true, "自定义中文原因"},
				{"2026-01-05T09:00:00Z", MissedRunOnce, true, "自定义中文原因"},
				{"2026-01-05T10:00:00Z", MissedRunOnce, false, "另一个原因"},
			},
			want: map[string]int{
				missedCountKey(true, "自定义中文原因"): 2,
				missedCountKey(false, "另一个原因"):  1,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var a MissedAudit
			a.Record(toRuns(t, tc.runs))
			got := a.Counts()
			if got == nil {
				t.Fatal("Counts() returned nil, want non-nil map")
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Counts() = %v, want %v", got, tc.want)
			}
			// 不变量：Reason 相关的键都含 CJK，且前缀区分 WillRun。
			for key := range got {
				if !containsCJK(key) {
					t.Fatalf("count key %q is not Chinese", key)
				}
				if !strings.HasPrefix(key, "运行|") && !strings.HasPrefix(key, "未运行|") {
					t.Fatalf("count key %q lacks a WillRun prefix", key)
				}
			}
		})
	}
}

// TestMissedAuditSummary 表驱动验证稳定中文汇总的格式与排序，并确认重复调用结果一致。
func TestMissedAuditSummary(t *testing.T) {
	// summaryWant 按与实现相同的文档格式独立构造期望值，避免手写大量 CJK 排序细节。
	summaryWant := func(records int, counts map[string]int) string {
		total := 0
		for _, n := range counts {
			total += n
		}
		keys := make([]string, 0, len(counts))
		for k := range counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+itoa(counts[k]))
		}
		return "累计 " + itoa(records) + " 次审计，" + itoa(total) + " 条错过记录；分类：" + strings.Join(parts, "，")
	}

	cases := []struct {
		name    string
		batches [][]rec
		counts  map[string]int
	}{
		{
			name:   "zero value summary",
			counts: map[string]int{},
		},
		{
			name: "single classification",
			batches: [][]rec{{
				{"2026-01-05T08:00:00Z", MissedRunOnce, true, reasonMissedRunOnce},
			}},
			counts: map[string]int{
				missedCountKey(true, reasonMissedRunOnce): 1,
			},
		},
		{
			name: "multiple classifications sorted by key",
			batches: [][]rec{
				{
					{"2026-01-05T08:00:00Z", MissedRunOnce, true, reasonMissedRunOnce},
					{"2026-01-05T09:00:00Z", MissedSkip, false, reasonMissedSkipped},
					{"2026-01-05T10:00:00Z", MissedRunLatest, true, reasonMissedRunLatest},
				},
				{
					{"2026-01-05T11:00:00Z", MissedRunOnce, false, reasonMissedOutOfWindow},
				},
			},
			counts: map[string]int{
				missedCountKey(true, reasonMissedRunOnce):      1,
				missedCountKey(false, reasonMissedSkipped):     1,
				missedCountKey(true, reasonMissedRunLatest):    1,
				missedCountKey(false, reasonMissedOutOfWindow): 1,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var a MissedAudit
			records := 0
			for _, batch := range tc.batches {
				a.Record(toRuns(t, batch))
				records++
			}
			want := summaryWant(records, tc.counts)
			if len(tc.batches) == 0 {
				// 零值时分类段固定为"无"，与空分类的格式化结果区分。
				want = "累计 0 次审计，0 条错过记录；分类：无"
			}
			got := a.Summary()
			if got != want {
				t.Fatalf("Summary() = %q, want %q", got, want)
			}
			if again := a.Summary(); again != got {
				t.Fatalf("Summary() is not stable: %q then %q", got, again)
			}
			if !containsCJK(got) {
				t.Fatalf("Summary() = %q is not Chinese", got)
			}
		})
	}
}

// TestMissedAuditDefensiveCopies 验证 Record 深拷贝入参、Timeline 返回副本：调用方随后
// 修改入参或返回值都不会改变审计内部状态。
func TestMissedAuditDefensiveCopies(t *testing.T) {
	var a MissedAudit
	input := []MissedRun{auditRun(t, "2026-01-05T08:00:00Z", MissedRunOnce, true, reasonMissedRunOnce)}
	a.Record(input)

	// 修改入参（含底层数组元素）不应影响已记录内容。
	input[0].WillRun = false
	input[0].Reason = "被改坏"
	input = append(input, auditRun(t, "2026-01-05T09:00:00Z", MissedSkip, false, reasonMissedSkipped))

	// 修改 Timeline 返回值也不应影响内部状态。
	out := a.Timeline()
	if len(out) != 1 {
		t.Fatalf("timeline len = %d, want 1 after mutating input", len(out))
	}
	out[0].Reason = "再次改坏"
	out = append(out, auditRun(t, "2026-01-05T10:00:00Z", MissedSkip, false, reasonMissedSkipped))

	want := []MissedRun{auditRun(t, "2026-01-05T08:00:00Z", MissedRunOnce, true, reasonMissedRunOnce)}
	if got := a.Timeline(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Timeline() = %+v, want %+v (internal state was mutated)", got, want)
	}
	// Counts 同样不受影响。
	wantCounts := map[string]int{missedCountKey(true, reasonMissedRunOnce): 1}
	if got := a.Counts(); !reflect.DeepEqual(got, wantCounts) {
		t.Fatalf("Counts() = %v, want %v", got, wantCounts)
	}
}

// TestMissedAuditConcurrent 并发调用全部方法，验证无数据竞争且最终计数完整。
func TestMissedAuditConcurrent(t *testing.T) {
	const writers = 8
	const perWriter = 50
	var a MissedAudit
	var wg sync.WaitGroup

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				a.Record([]MissedRun{{
					Slot:    time.Unix(int64(i), 0),
					Policy:  MissedRunOnce,
					WillRun: i%2 == 0,
					Reason:  reasonMissedRunOnce,
				}})
			}
		}()
	}
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				_ = a.Timeline()
				_ = a.Counts()
				_ = a.Summary()
			}
		}()
	}
	wg.Wait()

	want := writers * perWriter
	if got := len(a.Timeline()); got != want {
		t.Fatalf("timeline len = %d, want %d", got, want)
	}
	total := 0
	for _, n := range a.Counts() {
		total += n
	}
	if total != want {
		t.Fatalf("counts total = %d, want %d", total, want)
	}
}

// itoa 是 strconv.Itoa 的局部别名，简化期望值构造。
func itoa(n int) string {
	return strconv.Itoa(n)
}
