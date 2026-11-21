package server

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// 本文件提供一个只读诊断原语：把每一次工具审批决策（是否授予）按工具名
// 累计成指标。它刻意不接线——零调用方、纯内存、不注册路由、不读全局状态、
// 不写磁盘。用途是未来把这些决策汇聚到 /v1/status 或审计日志；当前只作为
// 可并发调用的计数器存在。

// ApprovalMetric 是单个工具累计到的审批指标。
//
// Asked 为询问总次数；Granted 与 Denied 分别为授予与拒绝次数，恒有
// Asked == Granted + Denied；LastMS 为最近一次决策的 Unix 毫秒时间戳。
type ApprovalMetric struct {
	Tool    string
	Asked   int
	Granted int
	Denied  int
	LastMS  int64
}

// ApprovalBoard 按工具名累计审批决策。所有方法都可安全并发调用。
//
// 零值即可用：tools 延迟初始化，因此 var b ApprovalBoard 与
// &ApprovalBoard{} 行为一致，默认空（无任何工具记录）。
type ApprovalBoard struct {
	mu    sync.RWMutex
	tools map[string]ApprovalMetric
}

// Record 记录一次针对 tool 的审批决策：granted 为 true 表示授予，false 表示拒绝。
// 每次调用都让 Asked 加一，并相应增加 Granted 或 Denied，同时把 LastMS 更新为
// 当前 Unix 毫秒时间戳。
func (b *ApprovalBoard) Record(tool string, granted bool) {
	b.recordAt(tool, granted, time.Now().UnixMilli())
}

// recordAt 与 Record 语义一致，但时间戳由调用方给定，便于测试注入确定性时间。
func (b *ApprovalBoard) recordAt(tool string, granted bool, nowMS int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tools == nil {
		b.tools = make(map[string]ApprovalMetric)
	}
	m := b.tools[tool]
	m.Tool = tool
	m.Asked++
	if granted {
		m.Granted++
	} else {
		m.Denied++
	}
	m.LastMS = nowMS
	b.tools[tool] = m
}

// Snapshot 返回当前所有工具指标的副本。返回值是新建的映射，调用方修改它
// 不会影响审批板；由于 ApprovalMetric 全由值类型字段组成，按值复制即完整副本。
// 零值审批板返回一个非 nil 的空映射，便于调用方直接写入。
func (b *ApprovalBoard) Snapshot() map[string]ApprovalMetric {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make(map[string]ApprovalMetric, len(b.tools))
	for tool, metric := range b.tools {
		out[tool] = metric
	}
	return out
}

// Summary 返回稳定、可读的中文汇总：工具按名称字典序排列，因此相同状态总
// 产生逐字节一致的字符串，与 Record 的调用顺序无关。零值（空）审批板返回
// 固定的“暂无记录”提示语。
func (b *ApprovalBoard) Summary() string {
	snap := b.Snapshot()
	names := make([]string, 0, len(snap))
	for name := range snap {
		names = append(names, name)
	}
	sort.Strings(names)

	if len(names) == 0 {
		return "审批决策汇总：暂无记录"
	}

	var totalAsked, totalGranted, totalDenied int
	for _, name := range names {
		m := snap[name]
		totalAsked += m.Asked
		totalGranted += m.Granted
		totalDenied += m.Denied
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "审批决策汇总（共 %d 次询问，授予 %d 次，拒绝 %d 次）\n",
		totalAsked, totalGranted, totalDenied)
	for _, name := range names {
		m := snap[name]
		fmt.Fprintf(&sb, "  %s：询问 %d 次，授予 %d 次，拒绝 %d 次，最后决策于 %dms\n",
			m.Tool, m.Asked, m.Granted, m.Denied, m.LastMS)
	}
	return strings.TrimSuffix(sb.String(), "\n")
}
