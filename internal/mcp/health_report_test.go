package mcp

import (
	"reflect"
	"testing"
)

// TestAggregateHealth 表驱动覆盖聚合原语的核心语义：全健康、部分健康、空输入、
// 名称排序、Gen 取最大、CJK 名称稳定排序，以及 reports/states 键并集与缺失值。
func TestAggregateHealth(t *testing.T) {
	tests := []struct {
		name    string
		reports map[string]HealthSnapshot
		states  map[string]ConnState
		want    MCPHealthReport
	}{
		{
			name: "全健康",
			reports: map[string]HealthSnapshot{
				"a": {Healthy: true, ToolCount: 3, Gen: 1},
				"b": {Healthy: true, ToolCount: 5, Gen: 2},
			},
			states: map[string]ConnState{"a": ConnReady, "b": ConnReady},
			want: MCPHealthReport{
				Servers: []ServerHealth{
					{Name: "a", Healthy: true, ToolCount: 3, State: "ready"},
					{Name: "b", Healthy: true, ToolCount: 5, State: "ready"},
				},
				OverallHealthy: true,
				Gen:            2,
			},
		},
		{
			name: "部分健康带错误文本",
			reports: map[string]HealthSnapshot{
				"a": {Healthy: true, ToolCount: 2, Gen: 3},
				"b": {Healthy: false, ToolCount: 4, LastError: "boom", Gen: 7},
			},
			states: map[string]ConnState{"a": ConnReady, "b": ConnFailed},
			want: MCPHealthReport{
				Servers: []ServerHealth{
					{Name: "a", Healthy: true, ToolCount: 2, State: "ready"},
					{Name: "b", Healthy: false, ToolCount: 4, State: "failed", LastError: "boom"},
				},
				OverallHealthy: false,
				Gen:            7,
			},
		},
		{
			name:    "空输入",
			reports: nil,
			states:  nil,
			want: MCPHealthReport{
				Servers:        []ServerHealth{},
				OverallHealthy: true,
				Gen:            0,
			},
		},
		{
			name: "按名称排序",
			reports: map[string]HealthSnapshot{
				"zeta":  {Healthy: true, ToolCount: 1, Gen: 1},
				"alpha": {Healthy: true, ToolCount: 2, Gen: 2},
				"mid":   {Healthy: true, ToolCount: 3, Gen: 3},
			},
			states: map[string]ConnState{
				"zeta":  ConnReady,
				"alpha": ConnReady,
				"mid":   ConnReady,
			},
			want: MCPHealthReport{
				Servers: []ServerHealth{
					{Name: "alpha", Healthy: true, ToolCount: 2, State: "ready"},
					{Name: "mid", Healthy: true, ToolCount: 3, State: "ready"},
					{Name: "zeta", Healthy: true, ToolCount: 1, State: "ready"},
				},
				OverallHealthy: true,
				Gen:            3,
			},
		},
		{
			name: "Gen 取最大值",
			reports: map[string]HealthSnapshot{
				"a": {Healthy: true, Gen: 1},
				"b": {Healthy: true, Gen: 99},
				"c": {Healthy: true, Gen: 7},
			},
			states: map[string]ConnState{"a": ConnReady, "b": ConnReady, "c": ConnReady},
			want: MCPHealthReport{
				Servers: []ServerHealth{
					{Name: "a", Healthy: true, State: "ready"},
					{Name: "b", Healthy: true, State: "ready"},
					{Name: "c", Healthy: true, State: "ready"},
				},
				OverallHealthy: true,
				Gen:            99,
			},
		},
		{
			name: "CJK 名按字节序稳定排序",
			reports: map[string]HealthSnapshot{
				"服务甲": {Healthy: true, ToolCount: 1, Gen: 1},
				"服务乙": {Healthy: true, ToolCount: 2, Gen: 2},
				"api": {Healthy: true, ToolCount: 3, Gen: 3},
			},
			states: map[string]ConnState{
				"服务甲": ConnReady,
				"服务乙": ConnReady,
				"api": ConnReady,
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
		{
			name: "键并集与缺失值",
			reports: map[string]HealthSnapshot{
				"only-report": {Healthy: true, ToolCount: 2, Gen: 5},
			},
			states: map[string]ConnState{
				"only-state": ConnConnecting,
			},
			want: MCPHealthReport{
				Servers: []ServerHealth{
					{Name: "only-report", Healthy: true, ToolCount: 2},
					{Name: "only-state", Healthy: false, State: "connecting"},
				},
				OverallHealthy: false,
				Gen:            5,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AggregateHealth(tt.reports, tt.states)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("AggregateHealth() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestAggregateHealthDeterministic 验证在 map 遍历顺序不固定的前提下，多次聚合
// 产出逐字节一致，CJK 名称不会因 locale 或遍历顺序发生漂移。
func TestAggregateHealthDeterministic(t *testing.T) {
	reports := map[string]HealthSnapshot{
		"服务甲":  {Healthy: true, ToolCount: 1, Gen: 1},
		"服务乙":  {Healthy: true, ToolCount: 2, Gen: 2},
		"api":  {Healthy: true, ToolCount: 3, Gen: 3},
		"zeta": {Healthy: false, ToolCount: 4, LastError: "bad", Gen: 4},
	}
	states := map[string]ConnState{
		"服务甲":  ConnReady,
		"服务乙":  ConnReady,
		"api":  ConnReady,
		"zeta": ConnFailed,
	}

	want := AggregateHealth(reports, states)
	for i := 0; i < 50; i++ {
		got := AggregateHealth(reports, states)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("第 %d 次聚合结果不稳定: got %+v, want %+v", i, got, want)
		}
	}
}

// TestAggregateHealthDoesNotMutateInputs 验证聚合原语不修改入参，且修改返回报告
// 也不会回写输入：所有字段均为值拷贝，与传入 map 无别名。
func TestAggregateHealthDoesNotMutateInputs(t *testing.T) {
	reports := map[string]HealthSnapshot{
		"a": {Healthy: true, ToolCount: 2, Gen: 3},
		"b": {Healthy: false, ToolCount: 1, LastError: "x", Gen: 9},
	}
	states := map[string]ConnState{"a": ConnReady, "b": ConnFailed}

	reportsCopy := make(map[string]HealthSnapshot, len(reports))
	for name, snap := range reports {
		reportsCopy[name] = snap
	}
	statesCopy := make(map[string]ConnState, len(states))
	for name, state := range states {
		statesCopy[name] = state
	}

	got := AggregateHealth(reports, states)
	if !reflect.DeepEqual(reports, reportsCopy) {
		t.Errorf("reports 被修改: got %+v, want %+v", reports, reportsCopy)
	}
	if !reflect.DeepEqual(states, statesCopy) {
		t.Errorf("states 被修改: got %+v, want %+v", states, statesCopy)
	}

	got.Servers[0].Name = "mutated"
	got.Servers[0].ToolCount = 999
	got.Servers[0].State = "mutated"
	got.Servers[0].LastError = "mutated"
	got.Gen = 12345
	if !reflect.DeepEqual(reports, reportsCopy) {
		t.Errorf("修改返回报告污染了 reports: got %+v, want %+v", reports, reportsCopy)
	}
	if !reflect.DeepEqual(states, statesCopy) {
		t.Errorf("修改返回报告污染了 states: got %+v, want %+v", states, statesCopy)
	}
}
