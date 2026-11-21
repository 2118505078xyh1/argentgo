package mcp

import "sort"

// ServerHealth 是单个 MCP 服务器的健康视图。字段均为值类型，由 AggregateHealth
// 从健康快照与连接状态逐字段拷贝而来，与输入 map 完全隔离。
type ServerHealth struct {
	Name      string // 服务器名，作为排序与聚合的稳定键
	Healthy   bool   // 最近一次健康观测是否健康
	ToolCount int    // 工具数
	State     string // 连接状态文本（ConnState 的字符串表示）
	LastError string // 最近一次失败的错误文本，健康时为 ""
}

// MCPHealthReport 是所有 MCP 服务器的聚合健康报告。Servers 按 Name 以字节序
// 升序排列；OverallHealthy 报告是否全部服务器健康；Gen 为所有快照生成号的最大值。
type MCPHealthReport struct {
	Servers        []ServerHealth
	OverallHealthy bool
	Gen            uint64
}

// AggregateHealth 把按服务名索引的健康快照与连接状态聚合为一份有序报告。
// 该函数是纯读取原语：只读入参、返回全新的切片与值，不修改传入的 map，也不
// 持有任何对输入内容的引用；刻意不接入任何现有调用流程，由调用方按需使用。
//
// 行为约定：
//   - 服务器集合取 reports 与 states 键的并集，只出现在一个 map 中的服务器
//     同样进入报告。
//   - 缺失的快照按零值处理：Healthy=false、ToolCount=0、LastError=""、Gen=0。
//   - 缺失的连接状态按空字符串处理。
//   - Servers 按 Name 以 UTF-8 字节序升序排列，不依赖 locale，CJK 名称同样稳定。
//   - OverallHealthy 为“全部服务器均健康”；空集合按空真值处理为 true。
//   - Gen 取所有快照 Gen 的最大值；空集合为 0。
func AggregateHealth(reports map[string]HealthSnapshot, states map[string]ConnState) MCPHealthReport {
	// 先收集并去重服务器名，再排序，保证输出顺序稳定、与 map 遍历顺序无关。
	names := make([]string, 0, len(reports)+len(states))
	seen := make(map[string]struct{}, len(reports)+len(states))
	for name := range reports {
		if _, dup := seen[name]; !dup {
			seen[name] = struct{}{}
			names = append(names, name)
		}
	}
	for name := range states {
		if _, dup := seen[name]; !dup {
			seen[name] = struct{}{}
			names = append(names, name)
		}
	}
	sort.Strings(names)

	report := MCPHealthReport{
		Servers:        make([]ServerHealth, 0, len(names)),
		OverallHealthy: true,
	}
	for _, name := range names {
		snap := reports[name] // 缺失键取零值快照
		state := states[name] // 缺失键取零值状态
		report.Servers = append(report.Servers, ServerHealth{
			Name:      name,
			Healthy:   snap.Healthy,
			ToolCount: snap.ToolCount,
			State:     string(state),
			LastError: snap.LastError,
		})
		if !snap.Healthy {
			report.OverallHealthy = false
		}
		if snap.Gen > report.Gen {
			report.Gen = snap.Gen
		}
	}
	return report
}
