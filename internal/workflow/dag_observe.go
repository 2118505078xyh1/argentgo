package workflow

import (
	"sort"
	"strings"
)

// StepDependencyMap 返回每个步骤名到其直接依赖步骤名的映射。
//
// 依赖来源与 ValidateStepDAG/StepExecutionOrder 完全一致：只解析步骤 Prompt
// 与 Args 中的 {{steps.NAME.output}} 引用，不读取其它字段。值按字典序稳定排序，
// 且每个值都是独立副本——调用方修改返回的 map 或其中的切片都不会影响入参、
// 其它条目或后续调用。
//
// 本函数是纯观测辅助：不校验依赖是否存在、不检测环、不修改 steps，也不参与
// Run/RunGuarded 执行路径。步骤名按 TrimSpace 归一化以与其它 DAG 辅助函数保持
// 一致；重名步骤不在此校验，保留第一次声明的映射（重名由 ValidateStepDAG 报错）。
func StepDependencyMap(steps []Step) map[string][]string {
	out := make(map[string][]string, len(steps))
	for _, step := range steps {
		name := strings.TrimSpace(step.Name)
		if _, exists := out[name]; exists {
			continue
		}
		refs := referencedSteps(step)
		sort.Strings(refs)
		// referencedSteps 已返回新切片；这里再显式复制，保证返回值与内部状态零共享，
		// 且无依赖的步骤得到非 nil 的空切片，便于调用方直接修改。
		deps := make([]string, len(refs))
		copy(deps, refs)
		out[name] = deps
	}
	return out
}

// IsStepReady 报告 step 的所有直接依赖是否都已在 completed 中完成。
//
// 依赖来源同 StepDependencyMap（Prompt/Args 里的 {{steps.NAME.output}} 引用）。
// completed 为 nil 时按空集合处理：无依赖的步骤仍然就绪，有依赖的步骤未就绪。
// 本函数是纯函数，不修改 step 或 completed，也不参与 Run/RunGuarded 执行路径。
func IsStepReady(step Step, completed map[string]bool) bool {
	for _, dep := range referencedSteps(step) {
		if !completed[dep] {
			return false
		}
	}
	return true
}
