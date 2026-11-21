package mcp

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
)

// wireSnapshotReporter 是可控的 HealthReporter fake：返回预设快照并记录调用次数，
// 全部访问在 mu 下完成，便于并发测试与 PollReporters 调用次数断言。
type wireSnapshotReporter struct {
	mu    sync.Mutex
	snap  HealthSnapshot
	calls int
}

func newWireSnapshotReporter(s HealthSnapshot) *wireSnapshotReporter {
	return &wireSnapshotReporter{snap: s}
}

func (r *wireSnapshotReporter) HealthSnapshot() HealthSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return r.snap
}

func (r *wireSnapshotReporter) set(s HealthSnapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snap = s
}

func (r *wireSnapshotReporter) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// TestHealthAggregatorZeroValue 验证零值可直接使用：默认空报告的
// OverallHealthy=true、Servers 为非 nil 空切片、Gen=0；且零值上的
// Track/Observe/PollReporters/Untrack/Report 均不 panic。
func TestHealthAggregatorZeroValue(t *testing.T) {
	var a HealthAggregator

	want := MCPHealthReport{Servers: []ServerHealth{}, OverallHealthy: true, Gen: 0}
	if got := a.Report(); !reflect.DeepEqual(got, want) {
		t.Fatalf("零值 Report = %+v, want %+v", got, want)
	}

	// 在零值上直接执行一整套生命周期操作，确认惰性初始化正确。
	a.Track("srv", newWireSnapshotReporter(HealthSnapshot{Healthy: true, ToolCount: 1, Gen: 1}))
	a.Observe("srv", HealthSnapshot{Healthy: true, ToolCount: 2, Gen: 3})
	a.PollReporters()
	a.Untrack("srv")
	if got := a.Report(); !reflect.DeepEqual(got, want) {
		t.Fatalf("零值生命周期后 Report = %+v, want %+v", got, want)
	}
}

// TestHealthAggregatorTrackUntrack 表驱动覆盖 Track/Untrack 的注册语义：
// Track 后源立即出现在报告中（状态为 disconnected），Untrack 后消失；
// 重复 Track 同名会重置快照与状态；nil reporter 可注册但不被轮询。
func TestHealthAggregatorTrackUntrack(t *testing.T) {
	healthy := HealthSnapshot{Healthy: true, ToolCount: 4, Gen: 5}

	tests := []struct {
		name    string
		track   []string
		observe map[string]HealthSnapshot
		retrack string
		untrack []string
		want    MCPHealthReport
	}{
		{
			name:  "Track 后立即出现且状态为 disconnected",
			track: []string{"alpha"},
			want: MCPHealthReport{
				Servers:        []ServerHealth{{Name: "alpha", State: "disconnected"}},
				OverallHealthy: false,
			},
		},
		{
			name:    "Observe 后派生 ready 状态",
			track:   []string{"alpha"},
			observe: map[string]HealthSnapshot{"alpha": healthy},
			want: MCPHealthReport{
				Servers:        []ServerHealth{{Name: "alpha", Healthy: true, ToolCount: 4, State: "ready"}},
				OverallHealthy: true,
				Gen:            5,
			},
		},
		{
			name:    "Untrack 后源消失",
			track:   []string{"alpha", "beta"},
			observe: map[string]HealthSnapshot{"alpha": healthy, "beta": healthy},
			untrack: []string{"alpha"},
			want: MCPHealthReport{
				Servers:        []ServerHealth{{Name: "beta", Healthy: true, ToolCount: 4, State: "ready"}},
				OverallHealthy: true,
				Gen:            5,
			},
		},
		{
			name:    "重复 Track 重置快照与状态",
			track:   []string{"alpha"},
			observe: map[string]HealthSnapshot{"alpha": healthy},
			retrack: "alpha",
			want: MCPHealthReport{
				Servers:        []ServerHealth{{Name: "alpha", State: "disconnected"}},
				OverallHealthy: false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var a HealthAggregator
			for _, name := range tt.track {
				a.Track(name, newWireSnapshotReporter(healthy))
			}
			for name, snap := range tt.observe {
				a.Observe(name, snap)
			}
			if tt.retrack != "" {
				a.Track(tt.retrack, nil)
			}
			for _, name := range tt.untrack {
				a.Untrack(name)
			}
			if got := a.Report(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Report = %+v, want %+v", got, tt.want)
			}
			// 对不存在的名字 Untrack 必须是空操作。
			a.Untrack("不存在")
			if got := a.Report(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("重复 Untrack 后 Report = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestHealthAggregatorObserveUpdates 验证 Observe 的“最后一次写入生效”语义：
// 同名多次观测后快照与派生状态以最新值为准，Gen 也是最新值（非最大值）。
func TestHealthAggregatorObserveUpdates(t *testing.T) {
	var a HealthAggregator
	a.Track("srv", nil)

	a.Observe("srv", HealthSnapshot{Healthy: true, ToolCount: 2, Gen: 7})
	if got := a.Report(); got.Gen != 7 || got.Servers[0].State != "ready" {
		t.Fatalf("第一次观测后 = %+v, want Gen=7/State=ready", got)
	}

	a.Observe("srv", HealthSnapshot{Healthy: false, ToolCount: 9, LastError: "boom", Gen: 3})
	want := MCPHealthReport{
		Servers:        []ServerHealth{{Name: "srv", Healthy: false, ToolCount: 9, State: "failed", LastError: "boom"}},
		OverallHealthy: false,
		Gen:            3,
	}
	if got := a.Report(); !reflect.DeepEqual(got, want) {
		t.Errorf("第二次观测后 = %+v, want %+v", got, want)
	}
}

// TestHealthAggregatorReportAggregation 表驱动验证 Report 调 AggregateHealth
// 的聚合语义：多源排序、全健康/部分健康、Gen 取最大、以及 CJK 名称按字节序
// 稳定排序。
func TestHealthAggregatorReportAggregation(t *testing.T) {
	tests := []struct {
		name    string
		observe map[string]HealthSnapshot
		want    MCPHealthReport
	}{
		{
			name: "多源全健康且按名称排序",
			observe: map[string]HealthSnapshot{
				"zeta":  {Healthy: true, ToolCount: 1, Gen: 2},
				"alpha": {Healthy: true, ToolCount: 3, Gen: 9},
			},
			want: MCPHealthReport{
				Servers: []ServerHealth{
					{Name: "alpha", Healthy: true, ToolCount: 3, State: "ready"},
					{Name: "zeta", Healthy: true, ToolCount: 1, State: "ready"},
				},
				OverallHealthy: true,
				Gen:            9,
			},
		},
		{
			name: "部分健康带错误文本",
			observe: map[string]HealthSnapshot{
				"ok":   {Healthy: true, ToolCount: 5, Gen: 4},
				"fail": {Healthy: false, ToolCount: 2, LastError: "timeout", Gen: 8},
			},
			want: MCPHealthReport{
				Servers: []ServerHealth{
					{Name: "fail", Healthy: false, ToolCount: 2, State: "failed", LastError: "timeout"},
					{Name: "ok", Healthy: true, ToolCount: 5, State: "ready"},
				},
				OverallHealthy: false,
				Gen:            8,
			},
		},
		{
			name: "CJK 名按字节序稳定排序",
			observe: map[string]HealthSnapshot{
				"服务甲": {Healthy: true, ToolCount: 1, Gen: 1},
				"服务乙": {Healthy: true, ToolCount: 2, Gen: 2},
				"api": {Healthy: true, ToolCount: 3, Gen: 3},
			},
			want: MCPHealthReport{
				Servers: []ServerHealth{
					{Name: "api", Healthy: true, ToolCount: 3, State: "ready"},
					{Name: "服务乙", Healthy: true, ToolCount: 2, State: "ready"},
					{Name: "服务甲", Healthy: true, ToolCount: 1, State: "ready"},
				},
				OverallHealthy: true,
				Gen:            3,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var a HealthAggregator
			for name, snap := range tt.observe {
				a.Track(name, nil)
				a.Observe(name, snap)
			}
			// 多次调用结果必须逐字节一致，验证与 map 遍历顺序无关。
			for i := 0; i < 20; i++ {
				if got := a.Report(); !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("第 %d 次 Report = %+v, want %+v", i, got, tt.want)
				}
			}
		})
	}
}

// TestHealthAggregatorPollReporters 表驱动验证 PollReporters 从各 tracked 源
// 拉取最新快照：首轮拉取、reporter 变化后再次拉取刷新、nil reporter 跳过、
// 以及未被 Track 的源不受影响。
func TestHealthAggregatorPollReporters(t *testing.T) {
	tests := []struct {
		name       string
		first      map[string]HealthSnapshot // Track 时各 reporter 的初始快照
		second     map[string]HealthSnapshot // 变化后的快照（nil 表示不变）
		extraTrack string                    // 另一轮 Track 的 nil reporter 名字
		want       MCPHealthReport
		wantCalls  map[string]int
	}{
		{
			name:  "首轮拉取并派生状态",
			first: map[string]HealthSnapshot{"a": {Healthy: true, ToolCount: 2, Gen: 5}},
			want: MCPHealthReport{
				Servers:        []ServerHealth{{Name: "a", Healthy: true, ToolCount: 2, State: "ready"}},
				OverallHealthy: true,
				Gen:            5,
			},
			wantCalls: map[string]int{"a": 1},
		},
		{
			name:   "变化后再次拉取刷新",
			first:  map[string]HealthSnapshot{"a": {Healthy: true, ToolCount: 2, Gen: 5}},
			second: map[string]HealthSnapshot{"a": {Healthy: false, ToolCount: 7, LastError: "down", Gen: 11}},
			want: MCPHealthReport{
				Servers:        []ServerHealth{{Name: "a", Healthy: false, ToolCount: 7, State: "failed", LastError: "down"}},
				OverallHealthy: false,
				Gen:            11,
			},
			wantCalls: map[string]int{"a": 2},
		},
		{
			name:       "nil reporter 跳过且不影响其他源",
			first:      map[string]HealthSnapshot{"b": {Healthy: true, ToolCount: 1, Gen: 3}},
			extraTrack: "nil-source",
			want: MCPHealthReport{
				Servers: []ServerHealth{
					{Name: "b", Healthy: true, ToolCount: 1, State: "ready"},
					{Name: "nil-source", State: "disconnected"},
				},
				OverallHealthy: false,
				Gen:            3,
			},
			wantCalls: map[string]int{"b": 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var a HealthAggregator
			reporters := make(map[string]*wireSnapshotReporter, len(tt.first))
			for name, snap := range tt.first {
				rep := newWireSnapshotReporter(snap)
				reporters[name] = rep
				a.Track(name, rep)
			}
			if tt.extraTrack != "" {
				a.Track(tt.extraTrack, nil)
			}

			a.PollReporters()
			if tt.second != nil {
				for name, snap := range tt.second {
					reporters[name].set(snap)
				}
				a.PollReporters()
			}

			if got := a.Report(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Report = %+v, want %+v", got, tt.want)
			}
			for name, wantCalls := range tt.wantCalls {
				if got := reporters[name].callCount(); got != wantCalls {
					t.Errorf("%s 的 HealthSnapshot 调用次数 = %d, want %d", name, got, wantCalls)
				}
			}
		})
	}
}

// TestHealthAggregatorPollReportersDoesNotOverwriteUntracked 验证 Untrack 后该源
// 不再被轮询，也不再出现在报告中；同时 Track 的新源能被后续 PollReporters 拉取。
func TestHealthAggregatorPollReportersDoesNotOverwriteUntracked(t *testing.T) {
	var a HealthAggregator
	oldRep := newWireSnapshotReporter(HealthSnapshot{Healthy: true, ToolCount: 1, Gen: 1})
	newRep := newWireSnapshotReporter(HealthSnapshot{Healthy: true, ToolCount: 9, Gen: 8})
	a.Track("old", oldRep)
	a.Track("new", newRep)
	a.Untrack("old")

	a.PollReporters()
	want := MCPHealthReport{
		Servers:        []ServerHealth{{Name: "new", Healthy: true, ToolCount: 9, State: "ready"}},
		OverallHealthy: true,
		Gen:            8,
	}
	if got := a.Report(); !reflect.DeepEqual(got, want) {
		t.Errorf("Report = %+v, want %+v", got, want)
	}
	if got := oldRep.callCount(); got != 0 {
		t.Errorf("已 Untrack 的 reporter 不应被调用, calls=%d", got)
	}
}

// TestHealthAggregatorReportIsolation 验证 Report 返回报告与内部状态无别名：
// 修改返回报告不会污染下一次 Report 的结果。
func TestHealthAggregatorReportIsolation(t *testing.T) {
	var a HealthAggregator
	a.Track("srv", nil)
	a.Observe("srv", HealthSnapshot{Healthy: true, ToolCount: 3, Gen: 6})

	got := a.Report()
	got.Servers[0].Name = "mutated"
	got.Servers[0].ToolCount = 999
	got.Gen = 12345

	want := MCPHealthReport{
		Servers:        []ServerHealth{{Name: "srv", Healthy: true, ToolCount: 3, State: "ready"}},
		OverallHealthy: true,
		Gen:            6,
	}
	if next := a.Report(); !reflect.DeepEqual(next, want) {
		t.Errorf("修改返回报告污染了内部状态: next = %+v, want %+v", next, want)
	}
}

// TestHealthAggregatorConcurrent 在多个写入者（Track/Observe/PollReporters）与
// 读者（Report）并存的场景下验证并发安全：无 panic、无数据竞争，最终报告包含
// 全部 n 个源、整体健康、Gen 取各源最大值；随后并发 Untrack 一半，验证计数收敛。
// 配合 go test -race 可检出竞态。
func TestHealthAggregatorConcurrent(t *testing.T) {
	var a HealthAggregator
	const n = 16
	const pollGen = uint64(1000)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("srv-%02d", i)
			rep := newWireSnapshotReporter(HealthSnapshot{Healthy: true, ToolCount: i, Gen: pollGen})
			a.Track(name, rep)
			for j := 0; j < 50; j++ {
				// Observe 的 Gen 小于 pollGen，故最终 Gen 由 PollReporters 决定的 pollGen 主导。
				a.Observe(name, HealthSnapshot{Healthy: true, ToolCount: i, Gen: uint64(j + 1)})
				if j%5 == 0 {
					a.PollReporters()
				}
				_ = a.Report()
			}
		}(i)
	}
	// 并发读者：持续调用 Report 与 PollReporters，制造读写交错。
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = a.Report()
				a.PollReporters()
			}
		}()
	}
	wg.Wait()

	report := a.Report()
	if len(report.Servers) != n {
		t.Fatalf("并发后源数量 = %d, want %d", len(report.Servers), n)
	}
	if !report.OverallHealthy {
		t.Error("并发后所有源均健康, OverallHealthy 应为 true")
	}
	if report.Gen != pollGen {
		t.Errorf("并发后 Gen = %d, want %d", report.Gen, pollGen)
	}
	for i, srv := range report.Servers {
		wantName := fmt.Sprintf("srv-%02d", i)
		if srv.Name != wantName || srv.State != "ready" {
			t.Errorf("Servers[%d] = %+v, want Name=%s/State=ready", i, srv, wantName)
		}
	}

	for i := 0; i < n/2; i++ {
		a.Untrack(fmt.Sprintf("srv-%02d", i))
	}
	if got := len(a.Report().Servers); got != n-n/2 {
		t.Errorf("Untrack 一半后源数量 = %d, want %d", got, n-n/2)
	}
}
