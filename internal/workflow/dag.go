package workflow

import (
	"fmt"
	"strings"
)

// 步骤输出引用由这些符号包围所引用的步骤
// 名，例如 {{steps.scan.output}} 表示"本步骤读取 scan 的输出"。
const (
	stepOutputPrefix = "steps."
	stepOutputSuffix = ".output"
)

// referencedSteps 返回某步骤读取其输出的步骤名
// referencedSteps 返回某步骤通过其 prompt 和 args 中的 {{steps.NAME.output}} 占位符读取输出的
// 步骤名。这些是 runner 唯一替换的两个模板字段（见 Executor.attempt），
// 因此这是步骤能依赖另一个步骤的唯一位置。名称
// 因此也是步骤能依赖另一个步骤的唯一位置。名称去重且 NAME 为空的畸形引用被忽略。
func referencedSteps(step Step) []string {
	seen := make(map[string]bool)
	var names []string
	for _, field := range []string{step.Prompt, step.Args} {
		for _, ph := range ExtractPlaceholders(field) {
			if !strings.HasPrefix(ph, stepOutputPrefix) || !strings.HasSuffix(ph, stepOutputSuffix) {
				continue
			}
			name := strings.TrimSuffix(strings.TrimPrefix(ph, stepOutputPrefix), stepOutputSuffix)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

// ValidateDAG 检查步骤输出依赖构成有向无环
// 图，并返回步骤索引的顺序，使每个步骤的依赖
// 图，并返回步骤索引，使每个步骤的依赖
//
// 都在其之前。
// 图是隐式的：当 i 的 prompt 或 args 包含 {{steps.<name of j>.output}} 时，步骤 i 依赖步骤 j。这把
// runner 已解析的模板语法提升为可执行图，使损坏的定义能在任何模型或工具被调用前
// 被拒绝。错误会点名违规步骤，便于共享工作流并粘贴到 issue；
// 被拒绝。错误会点名违规步骤，便于共享工作流并把消息粘贴到 issue 中；
// 声明的值从不被回显。
//
// 声明的值从不被回显。
// 返回的顺序是确定的：在所有就绪步骤中，总是选择声明索引最小者。没有步骤
// 引用的定义原样返回声明顺序。
func ValidateDAG(steps []Step) (order []int, err error) {
	n := len(steps)
	if n == 0 {
		return nil, nil
	}

	// 将步骤名映射到索引。从这里调用时 Validate() 已保证名称唯一且
	// 非空名称；对于直接调用，第一次声明胜出，使函数保持完备且确定。
	// 非空；对于直接调用，第一次声明胜出，使函数保持完备且确定。
	index := make(map[string]int, n)
	for i, step := range steps {
		name := strings.TrimSpace(step.Name)
		if _, exists := index[name]; !exists {
			index[name] = i
		}
	}

	deps := make([][]int, n)       // steps that each step depends on
	indegree := make([]int, n)     // number of unmet dependencies
	dependents := make([][]int, n) // reverse edges, for the topological pass
	for i, step := range steps {
		local := make(map[int]bool)
		for _, ref := range referencedSteps(step) {
			j, ok := index[ref]
			if !ok {
				return nil, fmt.Errorf("workflow: step %q depends on undefined step %q", strings.TrimSpace(step.Name), ref)
			}
			// 自引用（j == i）被记录为指向自身的边，
			// 被下面的拓扑遍历报告为环。
			if !local[j] {
				local[j] = true
				deps[i] = append(deps[i], j)
			}
		}
		indegree[i] = len(deps[i])
		for _, j := range deps[i] {
			dependents[j] = append(dependents[j], i)
		}
	}

	ready := make([]bool, n)
	done := make([]bool, n)
	for i := range ready {
		ready[i] = indegree[i] == 0
	}
	order = make([]int, 0, n)
	for len(order) < n {
		picked := -1
		for i := range done {
			if ready[i] && !done[i] {
				picked = i
				break
			}
		}
		if picked < 0 {
			// 没有就绪步骤剩余，因此尚未处理的步骤构成至少
			// 一个环（可能包含依赖该环的步骤）。
			stuck := make([]string, 0, n-len(order))
			for i := range done {
				if !done[i] {
					stuck = append(stuck, fmt.Sprintf("%q", strings.TrimSpace(steps[i].Name)))
				}
			}
			return nil, fmt.Errorf("workflow: cyclic step dependency among %s", strings.Join(stuck, ", "))
		}
		done[picked] = true
		order = append(order, picked)
		for _, d := range dependents[picked] {
			indegree[d]--
			if indegree[d] == 0 {
				ready[d] = true
			}
		}
	}
	return order, nil
}
