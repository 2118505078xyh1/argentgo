package jobs

import (
	"sort"
	"strings"
)

// taskCompletionStats 汇总一组作业的完成态统计，供任务组的
// 完成视图（例如成功率与失败清单）使用。返回的 map 始终包含
// 下列键：
//
//   - total：作业总数（int）
//   - running / exited / killed：各状态数量（int）
//   - succeeded：成功数（int）
//   - failed：失败数（int）
//   - completed：succeeded + failed，即已得出结果的作业数（int）
//   - successRate：succeeded / completed，取值 0..1（float64）
//   - failedNames：失败任务名清单，按字节序稳定排序（[]string）
//
// 判定规则：
//   - running 作业仍在运行，既不算成功也不算失败，且不进入成功率分母；
//   - exited 且 ExitCode == 0 视为成功；
//   - exited 且 ExitCode != 0（含无法恢复的 -1）以及 killed 视为失败。
//
// 空输入（含 nil）时各计数为 0、successRate 为 0、failedNames 为
// 空切片而非 nil，便于直接编码为 JSON 的 []。本函数为纯函数：
// 不修改入参，也不持有对入切的引用。失败名按字节序排序，因此
// 结果稳定且与区域设置无关，对 CJK 名称同样安全。
func taskCompletionStats(jobs []JobInfo) map[string]any {
	statusCounts := make(map[Status]int, 3)
	succeeded := 0
	failedNames := make([]string, 0)

	for _, job := range jobs {
		statusCounts[job.Status]++
		switch {
		case job.Status == StatusExited && job.ExitCode == 0:
			succeeded++
		case job.Status == StatusExited || job.Status == StatusKilled:
			failedNames = append(failedNames, failedJobName(job))
		}
	}

	completed := succeeded + len(failedNames)
	var successRate float64
	if completed > 0 {
		successRate = float64(succeeded) / float64(completed)
	}
	// 字节序排序对任意 UTF-8 都确定，且不受输入顺序影响。
	sort.Strings(failedNames)

	return map[string]any{
		"total":       len(jobs),
		"running":     statusCounts[StatusRunning],
		"exited":      statusCounts[StatusExited],
		"killed":      statusCounts[StatusKilled],
		"succeeded":   succeeded,
		"failed":      len(failedNames),
		"completed":   completed,
		"successRate": successRate,
		"failedNames": failedNames,
	}
}

// failedJobName 返回失败任务在清单中的显示名。优先使用命令，
// 命令为空（或只有空白）时回退到作业 ID。保留原值不裁剪，
// 只把纯空白的命令当作缺失，以免改变有内容的名称。
func failedJobName(job JobInfo) string {
	if strings.TrimSpace(job.Command) != "" {
		return job.Command
	}
	return job.ID
}
