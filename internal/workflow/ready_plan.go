package workflow

import (
	"sort"
	"strings"
)

// StepPlan 是一次"就绪步骤执行计划"的不可变快照：
//   - Order 是合法执行顺序（由 SimulateRun 给出），图无效时为空（nil）。
//   - ReadyNow 是当下可执行的步骤名，按声明顺序排列（由 ReadySteps 给出）。
//   - Blocked 记录每个仍被依赖阻塞的未完成步骤名到其未满足依赖名；无未满足
//     依赖的就绪步骤不出现在其中。
//
// StepPlan 是纯值对象：构造与推进都不共享入参底层数组，也不参与
// Run/RunGuarded 执行路径。零调用方，默认关闭。
type StepPlan struct {
	Order    []string
	ReadyNow []string
	Blocked  map[string][]string
}

// BuildStepPlan 基于步骤声明与已完成集合构造就绪步骤执行计划。
//
// 三个字段的来源如下：
//   - Order：直接取 SimulateRun(steps) 的结果。图有效时是合法执行顺序；存在
//     环、重名或缺失依赖时 SimulateRun 失败，Order 为 nil（即"环时为空"）。
//   - ReadyNow：取 ReadySteps(steps, completed)，按声明顺序稳定排列。
//   - Blocked：取 StepDependencyMap(steps) 与 completed 的差集。对每个未完成
//     步骤收录其尚未满足（completed 中不为 true）的依赖名；每个条目都是独立
//     副本，依赖名顺序保持 StepDependencyMap 的字典序。无未满足依赖的步骤不
//     进入 Blocked，它们（若未完成）列在 ReadyNow 中。
//
// completed 中缺失或为 false 的条目都按未完成处理；重名步骤保留第一次声明，
// 与其它 DAG 辅助函数一致。本函数是纯函数：不修改 steps 或 completed，返回
// 值与二者零共享。
func BuildStepPlan(steps []Step, completed map[string]bool) StepPlan {
	// 图无效时 order 为 nil，直接保留这一"空"语义，不重算也不伪造顺序。
	order, _ := SimulateRun(steps)
	if order != nil {
		// SimulateRun 内部已返回新切片，这里再显式复制一层，确保调用方修改
		// 返回的 Order 不会影响任何内部状态。
		copied := make([]string, len(order))
		copy(copied, order)
		order = copied
	}

	deps := StepDependencyMap(steps)
	blocked := make(map[string][]string)
	seen := make(map[string]bool, len(steps))
	for _, step := range steps {
		name := strings.TrimSpace(step.Name)
		if seen[name] {
			continue
		}
		seen[name] = true
		if completed[name] {
			continue
		}
		var unmet []string
		for _, dep := range deps[name] {
			if !completed[dep] {
				unmet = append(unmet, dep)
			}
		}
		if len(unmet) > 0 {
			blocked[name] = unmet
		}
	}

	return StepPlan{
		Order:    order,
		ReadyNow: ReadySteps(steps, completed),
		Blocked:  blocked,
	}
}

// AdvancePlan 把"justCompleted 这一步已完成"的效果投影到新的就绪计划上，
// 返回全新的 StepPlan（不修改 plan 及其任何成员，Order 原样保留、不重算）。
//
// 推进规则：
//   - justCompleted 从 ReadyNow 中移除（它已完成），并从 Blocked 中删除该键。
//   - 对每个仍被阻塞的步骤，从它的未满足依赖里移除 justCompleted；若列表清空
//     则该步骤变为就绪，从 Blocked 移出并加入 ReadyNow。
//   - 其余步骤状态不变。由于只完成一步，被解除阻塞的步骤仅变为"就绪"而非
//     "已完成"，因此不会再次级联解锁依赖它的步骤。
//
// ReadyNow 中原有步骤的相对顺序保持不变；新变为就绪的步骤按字典序追加，
// 保证输出确定（StepPlan 不保留声明顺序，故此处无法严格恢复声明顺序）。
// justCompleted 为空、不在计划中或不是任何步骤的依赖时，返回的 plan 与入参
// 等价（但仍为全新副本）。
func AdvancePlan(plan StepPlan, justCompleted string) StepPlan {
	var order []string
	if plan.Order != nil {
		order = make([]string, len(plan.Order))
		copy(order, plan.Order)
	}

	// 先保留原有就绪步骤（去掉刚完成的那个）的相对顺序。
	ready := make([]string, 0, len(plan.ReadyNow)+len(plan.Blocked))
	for _, name := range plan.ReadyNow {
		if name == justCompleted {
			continue
		}
		ready = append(ready, name)
	}

	blocked := make(map[string][]string, len(plan.Blocked))
	var newlyReady []string
	for name, deps := range plan.Blocked {
		if name == justCompleted {
			continue
		}
		remaining := make([]string, 0, len(deps))
		for _, dep := range deps {
			if dep == justCompleted {
				continue
			}
			remaining = append(remaining, dep)
		}
		if len(remaining) == 0 {
			newlyReady = append(newlyReady, name)
			continue
		}
		blocked[name] = remaining
	}

	// 新就绪步骤按字典序追加，确保多次调用结果一致。
	sort.Strings(newlyReady)
	ready = append(ready, newlyReady...)

	return StepPlan{
		Order:    order,
		ReadyNow: ready,
		Blocked:  blocked,
	}
}
