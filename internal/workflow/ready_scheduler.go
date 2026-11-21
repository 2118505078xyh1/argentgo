package workflow

import (
	"strings"
)

// ReadySteps 返回当前就绪、可执行的步骤名：步骤的全部依赖都已在 completed
// 中完成，步骤自身尚未完成，也没有因依赖未满足而被跳过。
//
// 返回值按 steps 的声明顺序稳定排列（与 StepExecutionOrder 的字典序平局规则
// 不同）；步骤名按 TrimSpace 归一化，与其它 DAG 辅助函数保持一致。重名步骤
// 保留第一次声明，与 StepDependencyMap 一致。依赖来源同 IsStepReady，只解析
// Prompt/Args 中的 {{steps.NAME.output}} 引用。
//
// completed 中值为 true 的条目表示已完成；缺失或为 false 的条目都按未完成处理。
// completed 为 nil 时按空集合处理：无依赖的步骤仍就绪，有依赖的步骤未就绪。
//
// 本函数是纯函数：不修改 steps 或 completed，返回的切片与入参零共享，也不参与
// Run/RunGuarded 执行路径。
func ReadySteps(steps []Step, completed map[string]bool) []string {
	out := make([]string, 0, len(steps))
	seen := make(map[string]bool, len(steps))
	for _, step := range steps {
		name := strings.TrimSpace(step.Name)
		if seen[name] {
			continue
		}
		seen[name] = true
		// 已完成的步骤不再返回。
		if completed[name] {
			continue
		}
		// 依赖未全部满足的步骤在本轮被视为跳过，不构成就绪集合。
		if !IsStepReady(step, completed) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// SimulateRun 基于 StepExecutionOrder 对步骤依赖图做一次纯模拟：不调用模型、
// 不运行工具、不产生任何副作用，只计算合法的执行顺序。
//
// 图有效时返回与 StepExecutionOrder 完全一致的执行顺序与 nil；存在环时返回
// nil 与可用 errors.Is 匹配 ErrStepDependencyCycle 的诊断错误。重名步骤与缺失
// 依赖也会由 StepExecutionOrder 报错（分别可用 ErrDuplicateStepName 与
// ErrStepMissingDependency 匹配），错误原样透传以保留哨兵链。
//
// 本函数是纯函数，不修改 steps，也不参与 Run/RunGuarded 执行路径。
func SimulateRun(steps []Step) (order []string, cycleErr error) {
	order, err := StepExecutionOrder(steps)
	if err != nil {
		return nil, err
	}
	return order, nil
}
