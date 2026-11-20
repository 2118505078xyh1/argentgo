package workflow

// StepRecord 是持久化步骤日志中的一条记录。Index 是步骤在
// StepRecord 是持久化步骤日志中的一条记录。Index 是步骤在工作流定义中的位置；Name 和 Status 镜像被记录的 StepResult。
// 被记录的 StepResult。记录可能以任意顺序到达（例如从日志
// 记录可能以任意顺序到达（例如从日志重放运行时），因此用 Index 而非切片位置标识步骤。
// 只有 StatusOK 算完成；失败和跳过的步骤必须重试。
type StepRecord struct {
	Index  int
	Name   string
	Status string
}

// RecoveryStep 根据已记录步骤的日志和当前步骤索引决定幂等重放应从哪里恢复。
// 已记录步骤的日志和当前步骤索引。
//
// 当日志中至少有一条该索引的记录且状态为 StatusOK 时，
// 步骤被视为完成；失败和跳过的记录不算。函数
// 从 current 向前遍历，返回第一个未完成的索引作为
// 当日志中至少有一条该索引的记录且状态为 StatusOK 时，步骤被视为完成；失败和跳过的记录不算。函数从 current 向前遍历，返回第一个未完成的索引作为 resumeFrom。skipCompleted 报告遍历是否越过了
// current（current 处的步骤及可能的更多步骤已完成，
// 应跳过而非重跑）。若从 current 到最后一条记录索引的所有步骤
// current（current 处的步骤及可能的更多步骤已完成，应跳过而非重跑）。若从 current 到最后一条记录索引的所有步骤都已完成，resumeFrom 为该最后索引之后一位，
// 表示没有内容需要重放。
//
// 表示没有内容需要重放。
// 函数是纯函数：从不修改日志，也不依赖切片顺序。负数 current 视为零。Index 为负的记录被忽略。
// 被忽略。
func RecoveryStep(journal []StepRecord, current int) (resumeFrom int, skipCompleted bool) {
	if current < 0 {
		current = 0
	}
	completed := make(map[int]bool, len(journal))
	for _, rec := range journal {
		if rec.Index < 0 || rec.Status != StatusOK {
			continue
		}
		completed[rec.Index] = true
	}
	resumeFrom = current
	for completed[resumeFrom] {
		resumeFrom++
	}
	return resumeFrom, resumeFrom > current
}
