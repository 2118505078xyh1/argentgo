package app

import (
	"encoding/json"
	"sync"

	"example.com/argentgo/internal/llm"
	"example.com/argentgo/internal/tools"
)

// usageSnapshot 是 `usage` 工具报告的 JSON 负载。它刻意对应 agent.EventUsage 携带的字段
// （见 internal/agent/events.go）：从 1 开始的轮次号、本次请求的用量增量以及运行总量。
// 货币/花费来源单独持久化在会话统计中；EventUsage 不携带花费，因此该工具报告事件流上实际存在的
// token 数据，而不是凭空编造一个数字。
type usageSnapshot struct {
	Turn       int       `json:"turn"`
	Usage      llm.Usage `json:"usage"`
	TotalUsage llm.Usage `json:"total_usage"`
}

// usageSource 是满足 tools.UsageSource 的线程安全持有者。
//
// agent 的实时用量位于 runState（internal/agent）中，而 internal/tools 无法导入它。
// 沿用 historySource 的模式，app 侧持有者把消费方接口留在 tools 中，并在这里适配运行数据。
// Execute 循环通过 toolset.UpdateUsage 把每个 EventUsage 推入持有者；`usage` 工具通过 UsageJSON 读回。
// 零值持有者是有效的，在第一个事件到达之前报告零快照。
type usageSource struct {
	mu   sync.RWMutex
	snap usageSnapshot
}

var _ tools.UsageSource = (*usageSource)(nil)

// newUsageSource 返回一个空的持有者。在任何 Update 之前使用它都是安全的。
func newUsageSource() *usageSource { return &usageSource{} }

// Update 记录最新的用量事件。turn 是事件从 1 开始的轮次号，usage 是当前请求的增量，
// total 是累积的运行总量。
func (u *usageSource) Update(turn int, usage, total llm.Usage) {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.snap = usageSnapshot{Turn: turn, Usage: usage, TotalUsage: total}
}

// Snapshot 为需要结构化值而非 JSON 的调用方返回当前快照的副本。
func (u *usageSource) Snapshot() usageSnapshot {
	if u == nil {
		return usageSnapshot{}
	}
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.snap
}

// UsageJSON 实现 tools.UsageSource。即使在第一次 Update 之前（全零），它也始终返回形状稳定的合法 JSON。
func (u *usageSource) UsageJSON() string {
	snap := u.Snapshot()
	data, err := json.Marshal(snap)
	if err != nil {
		// usageSnapshot 只由 int 和 llm.Usage（也是 int）组成，因此这不可能发生；
		// 保持该工具始终返回 JSON 的契约。
		return `{}`
	}
	return string(data)
}

// wireUsage 把一个用量源安装到注册表上。Registry.SetUsageSource 会就地重建该工具，
// 因此注册顺序和公布的工具规格保持稳定——这与 SetHistorySource 所依赖的保证相同。
func wireUsage(registry *tools.Registry, src *usageSource) {
	if registry == nil || src == nil {
		return
	}
	registry.SetUsageSource(src)
}
