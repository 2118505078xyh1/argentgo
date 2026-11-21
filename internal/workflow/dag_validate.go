package workflow

import (
	"errors"
	"fmt"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/pluginapi"
)

// 步骤依赖图的诊断哨兵错误。调用方可用 errors.Is 精确匹配某类校验失败，
// 同时通过错误文本获得可读的路径信息。所有错误都以这些哨兵为起点用 %w
// 包裹底层诊断，因此 errors.Is 对本包哨兵与 pluginapi 哨兵均成立。
var (
	// ErrDuplicateStepName 表示存在重名的步骤声明。
	ErrDuplicateStepName = errors.New("workflow: duplicate step name")
	// ErrStepMissingDependency 表示步骤依赖了未声明的步骤。
	ErrStepMissingDependency = errors.New("workflow: missing step dependency")
	// ErrStepDependencyCycle 表示步骤依赖图中存在环。
	ErrStepDependencyCycle = errors.New("workflow: step dependency cycle")
)

// stepGraph 把步骤声明转换为依赖图并复用 pluginapi.TopoSort 求拓扑顺序，
// 返回步骤名形式的合法执行顺序；出错时返回 nil 与带路径的诊断。
//
// Step 没有独立的 depends_on 字段，依赖来源是步骤 prompt/args 中的
// {{steps.NAME.output}} 引用（见 referencedSteps），这是现有结构里唯一
// 能声明步骤间依赖的字段。平局按步骤名字典序打破，因而结果与入参排列无关。
// 本函数是纯函数，不修改 steps。
func stepGraph(steps []Step) ([]string, error) {
	features := make([]pluginapi.Feature, 0, len(steps))
	seen := make(map[string]int, len(steps))
	for i, step := range steps {
		name := strings.TrimSpace(step.Name)
		if first, dup := seen[name]; dup {
			return nil, fmt.Errorf("%w: %q (declared at index %d and %d)", ErrDuplicateStepName, name, first, i)
		}
		seen[name] = i
		features = append(features, pluginapi.Feature{
			Name: name,
			Deps: referencedSteps(step),
		})
	}

	order, err := pluginapi.TopoSort(features)
	if err != nil {
		switch {
		case errors.Is(err, pluginapi.ErrDuplicateFeature):
			return nil, fmt.Errorf("%w: %w", ErrDuplicateStepName, err)
		case errors.Is(err, pluginapi.ErrMissingDependency):
			return nil, fmt.Errorf("%w: %w", ErrStepMissingDependency, err)
		default: // ErrCycle（或理论上的未知错误）统一按环处理。
			return nil, fmt.Errorf("%w: %w", ErrStepDependencyCycle, err)
		}
	}
	return order, nil
}

// ValidateStepDAG 校验步骤依赖图：步骤名唯一、每个依赖都指向已声明步骤、
// 且图中无环。依赖来自 prompt/args 中的 {{steps.NAME.output}} 引用。
// 出错时返回带路径诊断的错误，可用 errors.Is 匹配上面的哨兵；成功返回 nil。
//
// 本函数是纯函数，不修改 steps，也不参与 Run/RunGuarded 执行路径。
func ValidateStepDAG(steps []Step) error {
	_, err := stepGraph(steps)
	return err
}

// StepExecutionOrder 返回步骤名形式的合法执行顺序：任何步骤都排在其依赖之后。
// 平局按步骤名字典序打破，因此打乱入参顺序不会改变结果。
// 依赖图无效时返回 nil 与诊断错误（同 ValidateStepDAG）。
//
// 本函数是纯函数，不修改 steps，也不参与 Run/RunGuarded 执行路径。
func StepExecutionOrder(steps []Step) ([]string, error) {
	return stepGraph(steps)
}
