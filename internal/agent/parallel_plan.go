package agent

// PlanBatches 把一串工具调用规划成可依次执行的有序批次。
//
// 这里提供一个纯函数原语，把"哪些工具能并行"的决策从 mutating 串行规则里抽离出来：
//
//   - 变更型（Mutating）工具各自单独成一个串行批次，既不与其他工具并发，也不彼此并发，
//     从结构上保证副作用顺序安全。
//   - 非变更型工具按 maxParallel 切成若干并行批次，每批最多 maxParallel 个。
//   - 只把"输入中连续的一段"非变更型工具放进同一个并行批次，不跨过变更型工具重排；
//     因此把所有批次按序展开后与输入顺序完全一致。
//   - maxParallel<=1 时不做任何并行：每个工具各自成一个串行批次。
//
// 本函数是纯函数：只读 jobs，不修改入参；不访问时钟、环境或全局状态；相同输入永远得到
// 相同输出。工具名（Name）随 ToolJob 按值拷贝、按字节原样保留，不做截断或切分，
// 因此 CJK 名字安全。
func PlanBatches(jobs []ToolJob, maxParallel int) []Batch {
	if len(jobs) == 0 {
		return nil
	}

	// maxParallel<=1：全部串行，每个工具各自成一个串行批次。
	if maxParallel <= 1 {
		batches := make([]Batch, 0, len(jobs))
		for _, job := range jobs {
			batches = append(batches, Batch{Sequential: []ToolJob{job}})
		}
		return batches
	}

	var batches []Batch

	// flushParallel 把 [start, end) 这段连续的非变更型工具切成若干并行批次。
	// 每块都新建切片并拷贝，保证输出不与入参共享底层数组。
	flushParallel := func(start, end int) {
		for s := start; s < end; s += maxParallel {
			e := s + maxParallel
			if e > end {
				e = end
			}
			chunk := make([]ToolJob, e-s)
			copy(chunk, jobs[s:e])
			batches = append(batches, Batch{Parallel: chunk})
		}
	}

	start := 0
	for i, job := range jobs {
		if job.Mutating {
			// 先把之前累积的连续非变更型工具冲刷成并行批次。
			flushParallel(start, i)
			// 变更型工具单独成一个串行批次。
			batches = append(batches, Batch{Sequential: []ToolJob{job}})
			start = i + 1
		}
	}
	flushParallel(start, len(jobs))
	return batches
}

// ToolJob 是 PlanBatches 的最小输入单元：一次工具调用的标识与并发属性。
// ID、Name 原样 Pass-through；Mutating 决定是否必须串行执行。
type ToolJob struct {
	ID       string
	Name     string
	Mutating bool
}

// Batch 是一组可一起交给调度器的工具。
// Parallel 内的工具可并发执行（最多 maxParallel 个），Sequential 内的工具必须按序逐个执行。
// PlanBatches 产出的每个批次只会填充其中一个字段。
type Batch struct {
	Parallel   []ToolJob
	Sequential []ToolJob
}
