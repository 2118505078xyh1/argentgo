// Package prof 提供一个默认开启、常驻内存的轻量采样 profiler。
//
// 设计要点：
//   - 固定容量的环形缓冲（[10000]Sample），写满后覆盖最旧样本，内存恒定；
//   - 热路径只有一次 time.Now、一次 time.Since 与一次带锁数组写入，无堆分配；
//   - 以 region 为单位记录耗时，Snapshot 聚合出 folded stack 与分 region
//     的耗时/调用数/占比，输出 markdown，供人工诊断；
//   - ARGENT_PROF=0 作为紧急回退关闭采样（默认开启，用户零选择）。
//
// 它刻意不做符号化调用栈、不依赖 pprof、不写磁盘：目标是回答"这一轮时间
// 花在哪个阶段"，而不是替代开发期的 pprof 基准。
package prof

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Capacity 是环形缓冲的固定容量。写满后覆盖最旧样本，因此长时间运行的内存占用恒定。
const Capacity = 10000

// 常见热区的稳定 region 名。埋点使用这些常量，避免拼写漂移导致聚合被拆成多行。
const (
	RegionAgentLoop = "agent-loop"
	RegionLLMStream = "llm-stream"
	RegionToolRun   = "tool-run"
	RegionCompact   = "compact"
	RegionSearch    = "search"
	RegionGrep      = "grep"
)

// Sample 是一条采样记录：region 名、耗时（微秒）与起始时间戳（Unix 微秒）。
type Sample struct {
	Region string
	DurUS  int64
	TSUS   int64
}

// 环形缓冲状态。所有读写都在 mu 下进行；写入是最热的路径，临界区只有一次数组赋值。
var (
	mu         sync.Mutex
	ring       [Capacity]Sample
	next       int
	count      int
	enabled    = os.Getenv("ARGENT_PROF") != "0"
	windowFull bool
)

// Guard 是一次 region 计时的句柄。用 defer Region(name).End() 记录；
// 关闭采样时 Region 返回零值 Guard，End 变成一次布尔分支，不触碰共享状态。
type Guard struct {
	region string
	start  time.Time
	active bool
}

// Region 开始一次 region 计时。返回的 Guard 必须调用 End（通常用 defer）。
// 采样默认开启；ARGENT_PROF=0 时返回空操作 Guard。
func Region(name string) Guard {
	if !enabled {
		return Guard{}
	}
	return Guard{region: name, start: time.Now(), active: true}
}

// End 结束计时并把一条样本写入环形缓冲。它只做一次时间差、一次加锁数组写入。
func (g Guard) End() {
	if !g.active {
		return
	}
	dur := time.Since(g.start).Microseconds()
	if dur < 0 {
		dur = 0
	}
	record(Sample{Region: g.region, DurUS: dur, TSUS: g.start.UnixMicro()})
}

func record(s Sample) {
	mu.Lock()
	ring[next] = s
	next++
	if next == Capacity {
		next = 0
		windowFull = true
	}
	if count < Capacity {
		count++
	}
	mu.Unlock()
}

// Reset 清空环形缓冲。仅用于测试与诊断，生产路径不调用。
func Reset() {
	mu.Lock()
	ring = [Capacity]Sample{}
	next = 0
	count = 0
	windowFull = false
	mu.Unlock()
}

// RegionStat 是一个 region 的聚合结果。
type RegionStat struct {
	Region   string
	Calls    int
	TotalUS  int64
	AvgUS    int64
	SharePct float64
}

// Stats 聚合 window 时间窗内的样本（按起始时间戳过滤）；window<=0 表示全部样本。
// 返回按总耗时降序、region 名升序排列的切片。
func Stats(window time.Duration) []RegionStat {
	var cutoff int64
	if window > 0 {
		cutoff = time.Now().Add(-window).UnixMicro()
	}
	type agg struct {
		calls int
		total int64
	}
	byRegion := make(map[string]*agg)

	mu.Lock()
	start, n := 0, count
	if windowFull {
		// 环形缓冲已满：最旧样本在 next 处，共 Capacity 条。
		start, n = next, Capacity
	}
	for k := 0; k < n; k++ {
		s := ring[(start+k)%Capacity]
		if cutoff > 0 && s.TSUS < cutoff {
			continue
		}
		a := byRegion[s.Region]
		if a == nil {
			a = &agg{}
			byRegion[s.Region] = a
		}
		a.calls++
		a.total += s.DurUS
	}
	mu.Unlock()

	all := int64(0)
	for _, a := range byRegion {
		all += a.total
	}
	stats := make([]RegionStat, 0, len(byRegion))
	for name, a := range byRegion {
		share := 0.0
		if all > 0 {
			share = float64(a.total) / float64(all) * 100
		}
		avg := int64(0)
		if a.calls > 0 {
			avg = a.total / int64(a.calls)
		}
		stats = append(stats, RegionStat{
			Region: name, Calls: a.calls, TotalUS: a.total, AvgUS: avg, SharePct: share,
		})
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].TotalUS != stats[j].TotalUS {
			return stats[i].TotalUS > stats[j].TotalUS
		}
		return stats[i].Region < stats[j].Region
	})
	return stats
}

// Snapshot 返回 window 时间窗内聚合结果的 markdown 文本；window<=0 表示全部样本。
// 输出包含一段 folded-stack（单行帧 + 总耗时，便于火焰图工具消费）和一张分
// region 的耗时/调用数/占比表。
func Snapshot(window time.Duration) string {
	stats := Stats(window)
	totalCalls, totalUS := 0, int64(0)
	for _, s := range stats {
		totalCalls += s.Calls
		totalUS += s.TotalUS
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## 采样剖析快照（窗口 %s，样本 %d，总耗时 %s）\n\n", windowLabel(window), totalCalls, fmtDur(totalUS))
	if len(stats) == 0 {
		b.WriteString("（窗口内没有采样。profiler 默认开启，热区会用 defer Region 埋点。）\n")
		return b.String()
	}

	b.WriteString("### folded stacks（region 总耗时，µs）\n\n")
	for _, s := range stats {
		fmt.Fprintf(&b, "%s %d\n", s.Region, s.TotalUS)
	}

	b.WriteString("\n### 分 region 明细\n\n")
	b.WriteString("| region | calls | total | avg | share |\n")
	b.WriteString("|---|---:|---:|---:|---:|\n")
	for _, s := range stats {
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %.1f%% |\n",
			s.Region, s.Calls, fmtDur(s.TotalUS), fmtDur(s.AvgUS), s.SharePct)
	}
	return b.String()
}

func windowLabel(window time.Duration) string {
	if window <= 0 {
		return "全部"
	}
	return window.String()
}

func fmtDur(us int64) string {
	switch {
	case us >= 1_000_000:
		return fmt.Sprintf("%.2fs", float64(us)/1e6)
	case us >= 1000:
		return fmt.Sprintf("%.2fms", float64(us)/1e3)
	default:
		return fmt.Sprintf("%dµs", us)
	}
}
