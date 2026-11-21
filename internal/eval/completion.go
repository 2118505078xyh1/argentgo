package eval

// 本文件把脚本化 eval 的运行结果（Result）接到已有的长任务完成率原语（TaskCompletionRate）上。
// 这样 R16 的完成率不再只是纯原语：调用方可以把一次套件运行的真实 Result 列表传进来，
// 得到与长任务同一套语义的完成率。所有函数都是纯函数：只读入参、不依赖时钟/随机/全局状态。

// ScenarioCompletionRate 返回通过的场景在 results 中的比例（0 到 1）。
//
// 它把每个 Result.Passed 映射为 TaskResult.Completed，再复用 TaskCompletionRate 统计，
// 使场景级完成率与长任务完成率共用同一套语义（包括空输入返回 0 的 NaN 守卫）。
// 只读取 results，不修改入参；相同入参重复调用返回相同结果。
func ScenarioCompletionRate(results []Result) float64 {
	tasks := make([]TaskResult, len(results))
	for i, result := range results {
		tasks[i] = TaskResult{Completed: result.Passed}
	}
	return TaskCompletionRate(tasks)
}
