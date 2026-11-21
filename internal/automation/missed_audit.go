package automation

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// MissedAudit 是错过执行报告的审计轨迹原语：按调用顺序保存多次 Record 的批次，
// 并可展开为完整时间线、按 (WillRun, Reason) 分类计数或生成稳定中文汇总。
//
// 它只保存 MissedRun 的副本，零 I/O、无调用方，全部导出方法由内部 mutex 保护，
// 可被多个 goroutine 并发调用。零值可直接使用（等价于尚无审计记录）。
// 本类型刻意不与 daemon/missed_report 的实际流程耦合：调用方若要接入，需自行
// 把 ReportMissedRuns 的结果传入 Record。
type MissedAudit struct {
	mu      sync.Mutex
	records [][]MissedRun
}

// Record 追加一次审计记录。runs 按值深拷贝到新切片：调用方随后修改 runs 的元素
// 或重新分配底层数组都不会影响已记录的内容。即使 runs 为空或 nil，也算一次审计
// 记录（Timeline 中贡献零条），因此 len(records) 反映 Record 的调用次数。
func (a *MissedAudit) Record(runs []MissedRun) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.records = append(a.records, append([]MissedRun(nil), runs...))
}

// Timeline 按 Record 的调用顺序展开全部批次，返回一个全新切片；调用方修改返回值
// 不会影响内部状态。零值或从未 Record 时返回非 nil 的空切片。
func (a *MissedAudit) Timeline() []MissedRun {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.timelineLocked()
}

// timelineLocked 展开全部批次。调用方必须持有 a.mu。
func (a *MissedAudit) timelineLocked() []MissedRun {
	total := 0
	for _, rec := range a.records {
		total += len(rec)
	}
	out := make([]MissedRun, 0, total)
	for _, rec := range a.records {
		out = append(out, rec...)
	}
	return out
}

// missedCountKey 把一条报告按 WillRun 与 Reason 归入稳定键。"运行"/"未运行" 前缀
// 使键自解释，Reason 本身是稳定中文字符串，因此键不随入参、时区或环境变化。
func missedCountKey(willRun bool, reason string) string {
	if willRun {
		return "运行|" + reason
	}
	return "未运行|" + reason
}

// Counts 按 (WillRun, Reason) 分类统计目前所有已记录的错过报告，返回一个新 map，
// 调用方可自由修改。map 本身无序，需要稳定顺序时调用方对 key 排序即可；Summary
// 内部即按 key 排序生成。零值或从未 Record 时返回非 nil 的空 map。
func (a *MissedAudit) Counts() map[string]int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.countsLocked()
}

// countsLocked 分类统计全部批次。调用方必须持有 a.mu。
func (a *MissedAudit) countsLocked() map[string]int {
	counts := make(map[string]int)
	for _, run := range a.timelineLocked() {
		counts[missedCountKey(run.WillRun, run.Reason)]++
	}
	return counts
}

// Summary 生成稳定中文汇总："累计 N 次审计，M 条错过记录；分类：…"。分类项按 key
// 字典序排列并以"，"连接，因此相同输入在任意次调用、任意环境下结果完全一致。
// 无任何记录时分类段为"无"。
func (a *MissedAudit) Summary() string {
	a.mu.Lock()
	defer a.mu.Unlock()

	counts := a.countsLocked()
	total := 0
	for _, n := range counts {
		total += n
	}

	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	fmt.Fprintf(&b, "累计 %d 次审计，%d 条错过记录；分类：", len(a.records), total)
	if len(keys) == 0 {
		b.WriteString("无")
		return b.String()
	}
	for i, k := range keys {
		if i > 0 {
			b.WriteString("，")
		}
		fmt.Fprintf(&b, "%s=%d", k, counts[k])
	}
	return b.String()
}
