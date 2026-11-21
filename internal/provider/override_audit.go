package provider

import (
	"sort"
	"strings"
	"sync"
)

// noOverlayAuditSummary 是 OverlayAudit.Summary 在没有任何记录时返回的固定字面量。
const noOverlayAuditSummary = "无覆盖审计记录"

// OverlayAudit 按层名记录覆盖差异，供诊断与审计使用。
// 它只做内存记录：零 I/O、零调用方，借助内部读写锁保证并发安全。
// 零值即可使用，内部 map 在首次 Record 时惰性初始化。
type OverlayAudit struct {
	mu      sync.RWMutex
	entries map[string][]OverlayDiff
}

// Record 按层名 name 记录一次覆盖差异 diffs。
// 它先复制 diffs（保留 nil 与空切片的区分），因此后续改动入参不会影响已记录的内容。
// 同一 name 重复记录会用最新内容覆盖旧记录。
// 本方法并发安全，可从任意 goroutine 调用。
func (a *OverlayAudit) Record(name string, diffs []OverlayDiff) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.entries == nil {
		a.entries = make(map[string][]OverlayDiff)
	}
	a.entries[name] = cloneOverlayDiffs(diffs)
}

// Last 返回层名 name 最近一次记录的差异副本，以及该层是否存在记录。
// 返回的切片是副本，调用方改动它不会影响内部记录。
// 本方法并发安全，可从任意 goroutine 调用。
func (a *OverlayAudit) Last(name string) ([]OverlayDiff, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	diffs, ok := a.entries[name]
	if !ok {
		return nil, false
	}
	return cloneOverlayDiffs(diffs), true
}

// Summary 生成稳定的中文汇总：按层名的 UTF-8 字节序逐行输出「层名：差异摘要」，
// 摘要复用 SummarizeDiffs。没有任何记录时返回固定字面量 noOverlayAuditSummary。
// 本方法并发安全，可从任意 goroutine 调用。
func (a *OverlayAudit) Summary() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.entries) == 0 {
		return noOverlayAuditSummary
	}
	names := make([]string, 0, len(a.entries))
	for name := range a.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	lines := make([]string, 0, len(names))
	for _, name := range names {
		lines = append(lines, name+"："+SummarizeDiffs(a.entries[name]))
	}
	return strings.Join(lines, "\n")
}

// cloneOverlayDiffs 返回 diffs 的副本，保留 nil 与空切片的区分。
// OverlayDiff 仅含值类型字段，浅拷贝即可隔离内部状态。
func cloneOverlayDiffs(diffs []OverlayDiff) []OverlayDiff {
	if diffs == nil {
		return nil
	}
	out := make([]OverlayDiff, len(diffs))
	copy(out, diffs)
	return out
}
