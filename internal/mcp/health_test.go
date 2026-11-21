package mcp

import (
	"errors"
	"sync"
	"testing"
)

// observeStep 描述一次健康观测的输入。
type observeStep struct {
	healthy   bool
	toolCount int
	err       error
}

// TestHealthTrackerObserve 表驱动覆盖健康快照的核心语义：初始零值、Observe
// 更新、失败回退保留 last-good、Gen 单调，以及恢复健康后的状态收敛。
func TestHealthTrackerObserve(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name  string
		steps []observeStep
		want  HealthSnapshot
	}{
		{
			name:  "初始零值",
			steps: nil,
			want:  HealthSnapshot{},
		},
		{
			name:  "健康观测更新工具数并置 Gen",
			steps: []observeStep{{healthy: true, toolCount: 5}},
			want:  HealthSnapshot{Healthy: true, ToolCount: 5, Gen: 1},
		},
		{
			name: "连续健康观测 Gen 单调递增",
			steps: []observeStep{
				{healthy: true, toolCount: 2},
				{healthy: true, toolCount: 7},
				{healthy: true, toolCount: 9},
			},
			want: HealthSnapshot{Healthy: true, ToolCount: 9, Gen: 3},
		},
		{
			name: "错误保留 last-good 工具数",
			steps: []observeStep{
				{healthy: true, toolCount: 5},
				{healthy: false, toolCount: 99, err: boom},
			},
			want: HealthSnapshot{Healthy: false, ToolCount: 5, LastError: "boom", Gen: 2},
		},
		{
			name: "连续错误不污染 last-good 且记录最新错误",
			steps: []observeStep{
				{healthy: true, toolCount: 8},
				{healthy: false, toolCount: 100, err: errors.New("first")},
				{healthy: false, toolCount: 200, err: errors.New("second")},
			},
			want: HealthSnapshot{Healthy: false, ToolCount: 8, LastError: "second", Gen: 3},
		},
		{
			name: "恢复健康后清空错误并更新 last-good",
			steps: []observeStep{
				{healthy: true, toolCount: 4},
				{healthy: false, toolCount: 50, err: boom},
				{healthy: true, toolCount: 6},
			},
			want: HealthSnapshot{Healthy: true, ToolCount: 6, Gen: 3},
		},
		{
			name: "healthy=false 且无错误也回退 last-good，错误文本为空",
			steps: []observeStep{
				{healthy: true, toolCount: 3},
				{healthy: false, toolCount: 77},
			},
			want: HealthSnapshot{Healthy: false, ToolCount: 3, Gen: 2},
		},
		{
			name:  "首个观测即失败时 last-good 为零值",
			steps: []observeStep{{healthy: false, toolCount: 42, err: boom}},
			want:  HealthSnapshot{Healthy: false, ToolCount: 0, LastError: "boom", Gen: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h HealthTracker
			for _, s := range tt.steps {
				h.Observe(s.healthy, s.toolCount, s.err)
			}
			if got := h.Snapshot(); got != tt.want {
				t.Errorf("Snapshot() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestHealthTrackerChangedSince 表驱动覆盖变更探测：查询小于等于当前 Gen 时
// 报告“有变化”，查询大于或等于当前 Gen 时报告“无变化”。
func TestHealthTrackerChangedSince(t *testing.T) {
	tests := []struct {
		name    string
		observe int
		query   uint64
		want    bool
	}{
		{name: "初始 Gen=0 查询 0 未变更", observe: 0, query: 0, want: false},
		{name: "初始 Gen=0 查询更大值未变更", observe: 0, query: 1, want: false},
		{name: "一次观测后查询 0 已变更", observe: 1, query: 0, want: true},
		{name: "一次观测后查询 1 未变更", observe: 1, query: 1, want: false},
		{name: "三次观测后查询 2 已变更", observe: 3, query: 2, want: true},
		{name: "三次观测后查询 3 未变更", observe: 3, query: 3, want: false},
		{name: "查询未来版本未变更", observe: 1, query: 99, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h HealthTracker
			for i := 0; i < tt.observe; i++ {
				h.Observe(true, i, nil)
			}
			if got := h.ChangedSince(tt.query); got != tt.want {
				t.Errorf("ChangedSince(%d) = %v, want %v (Gen=%d)",
					tt.query, got, tt.want, h.Snapshot().Gen)
			}
		})
	}
}

// TestHealthTrackerSnapshotIndependent 验证快照独立性：调用方修改返回的快照
// 不会污染 tracker 内部状态。
func TestHealthTrackerSnapshotIndependent(t *testing.T) {
	var h HealthTracker
	h.Observe(true, 5, nil)

	snap := h.Snapshot()
	snap.Healthy = false
	snap.ToolCount = 999
	snap.LastError = "mutated"
	snap.Gen = 12345

	want := HealthSnapshot{Healthy: true, ToolCount: 5, Gen: 1}
	if got := h.Snapshot(); got != want {
		t.Errorf("快照被污染: got %+v, want %+v", got, want)
	}
}

// TestHealthTrackerConcurrentObserveSnapshot 在并发 Observe/Snapshot/ChangedSince
// 下验证无数据竞争，且 Gen 等于观测总次数。供 -race 运行。
func TestHealthTrackerConcurrentObserveSnapshot(t *testing.T) {
	const goroutines = 8
	const iterations = 1000
	var h HealthTracker

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				h.Observe(true, id, nil)
				_ = h.Snapshot()
				_ = h.ChangedSince(0)
			}
		}(g)
	}
	wg.Wait()

	snap := h.Snapshot()
	wantGen := uint64(goroutines) * uint64(iterations)
	if snap.Gen != wantGen {
		t.Errorf("Gen = %d, want %d", snap.Gen, wantGen)
	}
	if !snap.Healthy {
		t.Error("并发观测后应为健康")
	}
	if snap.LastError != "" {
		t.Errorf("并发观测后 LastError 应为空, got %q", snap.LastError)
	}
}
