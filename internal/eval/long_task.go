package eval

// 本文件提供一个脚本化"长任务"验收框架的纯原语。
//
// 背景：evals/core.json 中的场景都依赖脚本化假 provider，无法据此验收"真实模型质量"。
// 这里交付的是与模型无关的一层：调用方把一次运行产生的工具调用序列整理为 transcript，
// 再用 EvaluateTask 按步骤顺序核对，得到可跨进程、跨版本稳定比对的结果。
//
// 真正的脚本化 provider 场景数据由后续填充；本文件只负责可测试的评估逻辑。
// 两个函数都不修改入参、不依赖时钟/随机数/全局状态，也不触碰 golden 评分路径。

import "strings"

// 停止原因。以稳定的字符串常量而非自由文本返回，便于 golden 比对与调用方分支判断。
const (
	// TaskStopCompleted 表示所有步骤都已按顺序命中。
	TaskStopCompleted = "completed"
	// TaskStopMaxTurns 表示在步骤全部命中之前已用尽 MaxTurns 额度。
	TaskStopMaxTurns = "max_turns"
	// TaskStopTranscriptExhausted 表示 transcript 已读完但仍有步骤未命中。
	TaskStopTranscriptExhausted = "transcript_exhausted"
	// TaskStopNoSteps 表示任务没有步骤，按真空真值视为完成。
	TaskStopNoSteps = "no_steps"
)

// TaskStep 是长任务中的一个期望步骤。
//
// ExpectedTool 与 ExpectedPath 都按子串匹配（strings.Contains），空字符串表示对该维度不设约束；
// 两者只是便宜的静态筛子。Verify（非 nil）在静态筛子通过后对整条 transcript 条目求值，
// 用于承载更细的判定（例如参数内容、路径大小写、结果摘要）。只有三者都满足时步骤才算命中。
type TaskStep struct {
	ExpectedTool string
	ExpectedPath string
	Verify       func(string) bool
}

// LongTask 描述一个脚本化长任务。
//
// Steps 定义必须按顺序命中的步骤；MaxTurns 限制最多消费多少条 transcript 条目。
// MaxTurns <= 0 表示不设上限——这让零值 LongTask 也能直接用于评估，
// 同时给"无界脚本"留一个明确的入口（负数没有模拟意义，回退为无界而非报错）。
type LongTask struct {
	Name     string
	Steps    []TaskStep
	MaxTurns int
}

// TaskResult 是一次长任务评估的可观察结果。
//
// StepsDone/StepsTotal 描述完成度，TurnsUsed 是实际消费的 transcript 条目数，
// StopReason 解释评估为何结束。Completed 仅当 StepsTotal 个步骤全部按顺序命中时为 true。
type TaskResult struct {
	TaskName   string
	Completed  bool
	StepsDone  int
	StepsTotal int
	TurnsUsed  int
	StopReason string
}

// EvaluateTask 按脚本化 transcript 逐步核对任务步骤，返回确定的评估结果。
//
// transcript 的每条字符串模拟一个工具调用（例如 "read config.yaml"）。算法从左到右扫描 transcript：
//   - 当前步骤命中（参见 TaskStep）则前进一步，StepsDone 加一；
//   - 未命中的条目被跳过，但仍计入 TurnsUsed（它们是噪声轮次）；
//   - 步骤只朝一个方向推进，因此乱序的 transcript 无法满足"按顺序出现"的要求。
//
// 结束条件（按优先级）：所有步骤命中 -> TaskStopCompleted；消费数达到 MaxTurns -> TaskStopMaxTurns；
// transcript 读完 -> TaskStopTranscriptExhausted。无步骤时直接返回 TaskStopNoSteps。
//
// 它是纯函数：不修改 task.Steps、其中的 Verify、也不修改 transcript；输出只由入参决定。
// 匹配全部使用 strings.Contains，不对字符串做字节切片，因此 CJK/emoji 安全：
// 子串匹配只能在完整 rune 边界上成立，不会出现半个 UTF-8 字符。
func EvaluateTask(task LongTask, transcript []string) TaskResult {
	result := TaskResult{
		TaskName:   task.Name,
		StepsTotal: len(task.Steps),
	}

	// 没有步骤就没有未满足的期望：按真空真值视为完成，且不消费任何轮次。
	if result.StepsTotal == 0 {
		result.Completed = true
		result.StopReason = TaskStopNoSteps
		return result
	}

	unlimited := task.MaxTurns <= 0
	stepIndex := 0

	for _, entry := range transcript {
		// 在消费条目之前检查额度，保证最多消费 MaxTurns 条。
		if !unlimited && result.TurnsUsed >= task.MaxTurns {
			result.StopReason = TaskStopMaxTurns
			return result
		}
		result.TurnsUsed++

		if !stepMatches(task.Steps[stepIndex], entry) {
			continue
		}
		stepIndex++
		result.StepsDone++
		if stepIndex == result.StepsTotal {
			result.Completed = true
			result.StopReason = TaskStopCompleted
			return result
		}
	}

	// 循环自然结束：要么还有步骤没命中，要么恰好在最后一条完成（后者已在循环内返回）。
	result.StopReason = TaskStopTranscriptExhausted
	return result
}

// stepMatches 判断单条 transcript 是否满足一个步骤的全部约束。
func stepMatches(step TaskStep, entry string) bool {
	if step.ExpectedTool != "" && !strings.Contains(entry, step.ExpectedTool) {
		return false
	}
	if step.ExpectedPath != "" && !strings.Contains(entry, step.ExpectedPath) {
		return false
	}
	if step.Verify != nil && !step.Verify(entry) {
		return false
	}
	return true
}

// TaskCompletionRate 返回已完成结果在 results 中的比例（0 到 1）。
//
// 空输入返回 0，避免 0/0 产生 NaN 污染上层汇总。它只读取 Completed 字段，不修改 results。
func TaskCompletionRate(results []TaskResult) float64 {
	if len(results) == 0 {
		return 0
	}
	done := 0
	for _, result := range results {
		if result.Completed {
			done++
		}
	}
	return float64(done) / float64(len(results))
}
