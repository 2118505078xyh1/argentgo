package agent

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/calcutil"
	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// costChildRunner 是被取消子级结果的测试替身：直接返回给定的 Result 与错误。
type costChildRunner struct {
	res Result
	err error
}

func (c costChildRunner) Run(context.Context, string) (Result, error) { return c.res, c.err }

type costStatsRow struct {
	kind    string
	payload any
}

// costRecorder 捕获 Recorder 写入的所有行，供接线测试断言 stats 记录。
type costRecorder struct {
	mu   sync.Mutex
	rows []costStatsRow
}

func (r *costRecorder) Append(kind string, payload any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, costStatsRow{kind: kind, payload: payload})
	return nil
}

func (r *costRecorder) stats() []sessionStatsPayload {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []sessionStatsPayload
	for _, row := range r.rows {
		if row.kind != "stats" {
			continue
		}
		st, ok := row.payload.(sessionStatsPayload)
		if !ok {
			continue
		}
		out = append(out, st)
	}
	return out
}

func (r *costRecorder) count(kind string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, row := range r.rows {
		if row.kind == kind {
			n++
		}
	}
	return n
}

// costTaskTools 构建一个带价格表与 Recorder 的 taskTools，子级由给定的 Result 驱动。
func costTaskTools(rec *costRecorder, model string, prices map[string]Price, child costChildRunner) *taskTools {
	return &taskTools{
		inner: nopTools{},
		parent: Options{
			Model:  model,
			Prices: prices,
			Record: rec,
		},
		cfg: &SubagentConfig{
			MaxDepth: DefaultMaxDepth,
			NewChild: func(Options) ChildRunner { return child },
		},
	}
}

func cancelledChild(usage llm.Usage) costChildRunner {
	return costChildRunner{
		res: Result{Stopped: StopCancelled, Usage: usage},
		err: context.Canceled,
	}
}

// TestReconcileCancelledChildCostAccumulatesMultipleChildren 验证多个被取消子级的部分成本
// 各自补记一条 stats 记录，聚合后等于各子级成本之和；成本为 0 的子级不写记录。
func TestReconcileCancelledChildCostAccumulatesMultipleChildren(t *testing.T) {
	prices := map[string]Price{"child-model": {Input: 1, Output: 2}}
	rec := &costRecorder{}
	// 三个不同标签的子级，成本分别为 3.0、0.5、0。
	children := []costChildRunner{
		cancelledChild(llm.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000}), // 1*1 + 1*2 = 3
		cancelledChild(llm.Usage{PromptTokens: 500_000}),                                // 0.5
		cancelledChild(llm.Usage{}),                                                     // 0，不应写记录
	}
	i := 0
	tt := &taskTools{
		inner:  nopTools{},
		parent: Options{Model: "child-model", Prices: prices, Record: rec},
		cfg: &SubagentConfig{
			MaxDepth: DefaultMaxDepth,
			NewChild: func(Options) ChildRunner { c := children[i]; i++; return c },
		},
	}

	labels := []string{"甲子任务", "乙子任务", "丙子任务"}
	for _, label := range labels {
		_, err := tt.runTaskOnce(context.Background(), taskArgsJSON(label, "do work"), taskArgs{Description: label, Prompt: "do work"})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("子级取消错误 = %v, want context.Canceled", err)
		}
	}

	stats := rec.stats()
	if len(stats) != 2 {
		t.Fatalf("stats 记录数 = %d, want 2（零成本子级不写记录）", len(stats))
	}
	want := []float64{3, 0.5}
	for i, w := range want {
		if stats[i].CostUSD != w {
			t.Errorf("stats[%d].CostUSD = %v, want %v", i, stats[i].CostUSD, w)
		}
		if stats[i].CostSource != "estimated" {
			t.Errorf("stats[%d].CostSource = %q, want %q", i, stats[i].CostSource, "estimated")
		}
	}
	total := stats[0].CostUSD + stats[1].CostUSD
	if total != 3.5 {
		t.Errorf("累计成本 = %v, want 3.5", total)
	}
}

// TestReconcileCancelledChildCostOnlyForCancelledRuns 验证只有被取消的运行才补记：
// 正常完成的子级自身会写 stats，父级不得重复记账。
func TestReconcileCancelledChildCostOnlyForCancelledRuns(t *testing.T) {
	prices := map[string]Price{"m": {Input: 1, Output: 1}}
	usage := llm.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000}
	cases := []struct {
		name  string
		child costChildRunner
		want  int
	}{
		{"cancelled", cancelledChild(usage), 1},
		{"completed", costChildRunner{res: Result{Stopped: StopComplete, Usage: usage}}, 0},
		{"provider error", costChildRunner{res: Result{Stopped: StopProviderError, Usage: usage}, err: errors.New("boom")}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &costRecorder{}
			tt := costTaskTools(rec, "m", prices, tc.child)
			args := taskArgs{Description: "子任务", Prompt: "do work"}
			_, _ = tt.runTaskOnce(context.Background(), taskArgsJSON(args.Description, args.Prompt), args)
			if got := rec.count("stats"); got != tc.want {
				t.Fatalf("stats 记录数 = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestReconcileCancelledChildCostNoChildBaseline 验证没有子代理时完全不触碰 Recorder。
func TestReconcileCancelledChildCostNoChildBaseline(t *testing.T) {
	rec := &costRecorder{}
	tt := costTaskTools(rec, "m", map[string]Price{"m": {Input: 1}}, costChildRunner{})

	// 非 task 工具 delegating 给 inner，不产生任何 stats 记录。
	_, _ = tt.Run(context.Background(), "other-tool", `{}`)
	if got := rec.count("stats"); got != 0 {
		t.Fatalf("无子代理时 stats 记录数 = %d, want 0", got)
	}
}

// TestReconcileCancelledChildCostZeroCostNoRecord 验证成本为 0（无价格表）时不写记录。
func TestReconcileCancelledChildCostZeroCostNoRecord(t *testing.T) {
	rec := &costRecorder{}
	tt := costTaskTools(rec, "m", nil, cancelledChild(llm.Usage{PromptTokens: 1_000_000}))
	args := taskArgs{Description: "子任务", Prompt: "do work"}
	_, _ = tt.runTaskOnce(context.Background(), taskArgsJSON(args.Description, args.Prompt), args)
	if got := rec.count("stats"); got != 0 {
		t.Fatalf("成本为 0 时 stats 记录数 = %d, want 0", got)
	}
}

// TestReconcileCancelledChildCostBackground 验证后台子级被取消时同样补记成本。
func TestReconcileCancelledChildCostBackground(t *testing.T) {
	prices := map[string]Price{"m": {Input: 1, Output: 2}}
	rec := &costRecorder{}
	bg := NewBackgroundResults(0, 4)
	child := cancelledChild(llm.Usage{PromptTokens: 2_000_000, CompletionTokens: 500_000})
	tt := &taskTools{
		inner:  nopTools{},
		parent: Options{Model: "m", Prices: prices, Record: rec},
		cfg: &SubagentConfig{
			MaxDepth:   DefaultMaxDepth,
			Background: bg,
			NewChild:   func(Options) ChildRunner { return child },
		},
	}
	raw, _ := json.Marshal(taskArgs{Description: "后台子任务", Prompt: "do work", Background: true})
	if _, err := tt.Run(context.Background(), TaskToolName, string(raw)); err != nil {
		t.Fatal(err)
	}
	// 等待后台 goroutine 完成写入。
	deadline := time.Now().Add(2 * time.Second)
	for rec.count("stats") == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	stats := rec.stats()
	if len(stats) != 1 {
		t.Fatalf("stats 记录数 = %d, want 1", len(stats))
	}
	want := 2*1 + 0.5*2 // 3
	if stats[0].CostUSD != want {
		t.Errorf("CostUSD = %v, want %v", stats[0].CostUSD, want)
	}
}

// TestReconcileChildCostCJKChildID 验证 CJK ChildID 按字节序稳定排序（接线使用的原语语义）。
func TestReconcileChildCostCJKChildID(t *testing.T) {
	children := []calcutil.CostContribution{
		{ChildID: "中文乙", CostUSD: 0.2, Tokens: 2},
		{ChildID: "中文甲", CostUSD: 0.1, Tokens: 1},
		{ChildID: "中文乙", CostUSD: 0.3, Tokens: 3},
	}
	total, breakdown := calcutil.ReconcileChildCost(0, children)
	if total != 0.6 {
		t.Fatalf("total = %v, want 0.6", total)
	}
	// Go 的字符串比较按 UTF-8 字节序：乙(U+4E59) < 甲(U+7532)。
	wantIDs := []string{"中文乙", "中文乙", "中文甲"}
	wantCosts := []float64{0.2, 0.3, 0.1}
	for i := range wantIDs {
		if breakdown[i].ChildID != wantIDs[i] || breakdown[i].CostUSD != wantCosts[i] {
			t.Errorf("breakdown[%d] = %+v, want id=%s cost=%v", i, breakdown[i], wantIDs[i], wantCosts[i])
		}
	}
}

// TestReconcileChildCostFiltersInvalid 验证负数/NaN/±Inf 被过滤、零值保留（接线使用的原语语义）。
func TestReconcileChildCostFiltersInvalid(t *testing.T) {
	children := []calcutil.CostContribution{
		{ChildID: "neg", CostUSD: -1.5},
		{ChildID: "nan", CostUSD: math.NaN()},
		{ChildID: "posinf", CostUSD: math.Inf(1)},
		{ChildID: "neginf", CostUSD: math.Inf(-1)},
		{ChildID: "zero", CostUSD: 0, Tokens: 7},
		{ChildID: "ok", CostUSD: 0.75, Tokens: 8},
	}
	total, breakdown := calcutil.ReconcileChildCost(1, children)
	if total != 1.75 {
		t.Fatalf("total = %v, want 1.75", total)
	}
	if len(breakdown) != 2 {
		t.Fatalf("breakdown 长度 = %d, want 2", len(breakdown))
	}
	if breakdown[0].ChildID != "ok" || breakdown[1].ChildID != "zero" {
		t.Fatalf("breakdown = %+v, want [ok zero]", breakdown)
	}
}
