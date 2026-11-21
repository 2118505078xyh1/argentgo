package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 前置条件支持的比较操作。未指定时默认为 equals。
const (
	CondEquals      = "equals"
	CondNotEquals   = "not_equals"
	CondContains    = "contains"
	CondNotContains = "not_contains"
	CondExists      = "exists"
	CondAbsent      = "absent"
)

// 前置条件不满足时的处理策略。
const (
	// OnUnmetSkip 跳过当前步骤，继续运行后续步骤。
	OnUnmetSkip = "skip"
	// OnUnmetStop 跳过当前步骤并停止整个工作流。
	OnUnmetStop = "stop"
)

// 步骤失败后的回退策略。
const (
	// FallbackNone 不回退，沿用步骤自身的 on_failure 决策。
	FallbackNone = "none"
	// FallbackSkip 把失败步骤记为跳过并继续运行；不使整个运行失败。
	FallbackSkip = "skip"
	// FallbackRollback 重新执行从 rollback_to 到当前步骤（含）的区间一次。
	FallbackRollback = "rollback"
)

// Precondition 是步骤执行前必须成立的客观校验。
// 校验只读取当前已知的步骤输出、已解析的参数和用户输入，不调用模型或 shell。
type Precondition struct {
	// Key 指定被检查的值："input" 表示用户输入，"steps.NAME.output" 表示某步骤的输出，
	// 其他字符串按已解析的工作流参数名查找。
	Key string `json:"key"`
	// Op 是比较操作；为空时按 equals 处理。
	Op string `json:"op,omitempty"`
	// Value 是期望值；exists/absent 操作忽略它。
	Value string `json:"value"`
}

// StepGuard 是步骤的可选守卫：前置条件与失败回退。
// 它只在 RunGuarded 路径生效，默认的 Run/RunWithParams 路径完全忽略它。
type StepGuard struct {
	// Preconditions 全部成立时步骤才会执行；任一不成立按 OnUnmet 处理。
	Preconditions []Precondition `json:"preconditions,omitempty"`
	// OnUnmet 是前置条件不满足时的策略：skip（默认）或 stop。
	OnUnmet string `json:"on_unmet,omitempty"`
	// Fallback 是步骤失败后的回退策略：none（默认）、skip 或 rollback。
	Fallback string `json:"fallback,omitempty"`
	// RollbackTo 是 Fallback=rollback 时回滚到的步骤名；该步骤必须已声明。
	RollbackTo string `json:"rollback_to,omitempty"`
}

// op 返回规范化后的操作名。
func (p Precondition) op() string {
	op := strings.ToLower(strings.TrimSpace(p.Op))
	if op == "" {
		return CondEquals
	}
	return op
}

// onUnmet 返回规范化后的前置条件不满足策略。
func (g *StepGuard) onUnmet() string {
	policy := strings.ToLower(strings.TrimSpace(g.OnUnmet))
	if policy == "" {
		return OnUnmetSkip
	}
	return policy
}

// fallback 返回规范化后的失败回退策略。
func (g *StepGuard) fallback() string {
	policy := strings.ToLower(strings.TrimSpace(g.Fallback))
	if policy == "" {
		return FallbackNone
	}
	return policy
}

// guardValue 取出前置条件引用的值。ok 报告该键是否存在。
// 优先级：内置 input > 步骤输出 > 工作流参数。
func guardValue(key string, input string, outputs, params map[string]string) (string, bool) {
	key = strings.TrimSpace(key)
	if key == "input" {
		return input, true
	}
	if strings.HasPrefix(key, stepOutputPrefix) && strings.HasSuffix(key, stepOutputSuffix) {
		name := strings.TrimSuffix(strings.TrimPrefix(key, stepOutputPrefix), stepOutputSuffix)
		value, ok := outputs[name]
		return value, ok
	}
	value, ok := params[key]
	return value, ok
}

// matches 报告单条前置条件是否成立。缺失的键只对 exists/absent 有意义；
// 对比较操作，缺失的键一律视为不成立。
func (p Precondition) matches(input string, outputs, params map[string]string) bool {
	value, exists := guardValue(p.Key, input, outputs, params)
	switch p.op() {
	case CondExists:
		return exists
	case CondAbsent:
		return !exists
	}
	if !exists {
		return false
	}
	expected := p.Value
	switch p.op() {
	case CondEquals:
		return value == expected
	case CondNotEquals:
		return value != expected
	case CondContains:
		return strings.Contains(value, expected)
	case CondNotContains:
		return !strings.Contains(value, expected)
	}
	return false
}

// unmetPrecondition 返回第一条不成立的前置条件的描述；全部成立时 ok 为 true。
func unmetPrecondition(conditions []Precondition, input string, outputs, params map[string]string) (reason string, ok bool) {
	for _, p := range conditions {
		if p.matches(input, outputs, params) {
			continue
		}
		return fmt.Sprintf("%s %s %q", p.Key, p.op(), p.Value), false
	}
	return "", true
}

// validateGuards 在守卫路径启用时检查声明的合法性，避免拼写错误被静默忽略。
func validateGuards(steps []Step) error {
	names := make(map[string]bool, len(steps))
	for _, step := range steps {
		names[strings.TrimSpace(step.Name)] = true
	}
	for _, step := range steps {
		guard := step.Guard
		if guard == nil {
			continue
		}
		for i, p := range guard.Preconditions {
			if strings.TrimSpace(p.Key) == "" {
				return fmt.Errorf("workflow: step %q precondition %d has no key", step.Name, i+1)
			}
			switch p.op() {
			case CondEquals, CondNotEquals, CondContains, CondNotContains, CondExists, CondAbsent:
			default:
				return fmt.Errorf("workflow: step %q precondition %d has an unknown op %q", step.Name, i+1, p.op())
			}
		}
		switch guard.onUnmet() {
		case OnUnmetSkip, OnUnmetStop:
		default:
			return fmt.Errorf("workflow: step %q has an unknown on_unmet %q", step.Name, guard.onUnmet())
		}
		switch guard.fallback() {
		case FallbackNone, FallbackSkip, FallbackRollback:
		default:
			return fmt.Errorf("workflow: step %q has an unknown fallback %q", step.Name, guard.fallback())
		}
		if guard.fallback() == FallbackRollback {
			target := strings.TrimSpace(guard.RollbackTo)
			if target == "" {
				return fmt.Errorf("workflow: step %q needs rollback_to for fallback rollback", step.Name)
			}
			if !names[target] {
				return fmt.Errorf("workflow: step %q rolls back to undefined step %q", step.Name, target)
			}
		}
	}
	return nil
}

// RunGuarded 在按依赖排序执行步骤的同时应用步骤守卫：执行前校验前置条件，
// 失败时按回退策略跳过或回滚。它是显式 opt-in 路径；默认的 Run/RunWithParams
// 从不读取 Guard 字段，其行为逐字节保持不变。
//
// 步骤失败本身仍属于结果而不是返回值；仅在定义、守卫声明或参数无效时返回错误。
func (e *Executor) RunGuarded(ctx context.Context, def *Definition, input string, params map[string]string) (Run, error) {
	if def == nil {
		return Run{}, errors.New("workflow: the definition is nil")
	}
	local := *def
	local.Steps = append([]Step(nil), def.Steps...)
	local.normalise()
	if err := local.Validate(); err != nil {
		return Run{}, err
	}
	if err := validateGuards(local.Steps); err != nil {
		return Run{}, err
	}
	resolved, err := ResolveParameters(local, params)
	if err != nil {
		return Run{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// 与原路径一致：ValidateDAG 计算出的顺序同时是执行顺序。
	order, err := ValidateDAG(local.Steps)
	if err != nil {
		return Run{}, err
	}
	backoff := e.Backoff
	if backoff <= 0 {
		backoff = defaultBackoff
	}
	for _, step := range local.Steps {
		if step.Tool != "" && e.Tools == nil {
			return Run{}, fmt.Errorf("workflow: step %q needs a tool runner for %q", step.Name, step.Tool)
		}
		if step.Tool == "" && e.Prompter == nil {
			return Run{}, fmt.Errorf("workflow: step %q needs a prompter", step.Name)
		}
	}

	run := Run{Name: local.Name, Status: StatusOK}
	outputs := make(map[string]string, len(local.Steps))
	var previous string
	havePrevious := false
	stopped := false

	for pos, index := range order {
		step := local.Steps[index]
		if stopped {
			run.Steps = append(run.Steps, StepResult{
				Name:   step.Name,
				Status: StatusSkipped,
				Error:  "an earlier step stopped the workflow",
			})
			continue
		}
		// 与默认路径相同的依赖检查：生产者没有输出时消费者不得运行。
		missing := ""
		for _, dep := range referencedSteps(step) {
			if _, ok := outputs[dep]; !ok {
				missing = dep
				break
			}
		}
		if missing != "" {
			run.Steps = append(run.Steps, StepResult{
				Name:   step.Name,
				Status: StatusSkipped,
				Error:  fmt.Sprintf("depends on step %q which did not produce an output", missing),
			})
			continue
		}
		if !step.When.matches(previous, havePrevious) {
			run.Steps = append(run.Steps, StepResult{
				Name:   step.Name,
				Status: StatusSkipped,
				Error:  "condition not met",
			})
			continue
		}

		// 守卫新增：执行前校验前置条件。
		if guard := step.Guard; guard != nil && len(guard.Preconditions) > 0 {
			if reason, ok := unmetPrecondition(guard.Preconditions, input, outputs, resolved); !ok {
				if guard.onUnmet() == OnUnmetStop {
					stopped = true
					run.Status = StatusFailed
				}
				run.Steps = append(run.Steps, StepResult{
					Name:   step.Name,
					Status: StatusSkipped,
					Error:  "precondition not met: " + reason,
				})
				continue
			}
		}

		result := e.runStep(ctx, step, input, outputs, resolved, backoff)
		run.Steps = append(run.Steps, result)
		if result.Status == StatusOK {
			outputs[step.Name] = result.Output
			previous = result.Output
			havePrevious = true
			continue
		}

		// 守卫新增：失败回退。skip 把失败转为跳过且不使运行失败；rollback
		// 重新执行 checkpoint 到当前步骤的区间一次。
		if guard := step.Guard; guard != nil {
			switch guard.fallback() {
			case FallbackSkip:
				run.Steps[len(run.Steps)-1] = StepResult{
					Name:     step.Name,
					Status:   StatusSkipped,
					Error:    "step failed, skipped by fallback: " + result.Error,
					Attempts: result.Attempts,
					Duration: result.Duration,
				}
				continue
			case FallbackRollback:
				targetPos := orderPosition(order, stepIndexByName(local.Steps, guard.RollbackTo))
				if targetPos >= 0 && targetPos <= pos {
					rerun, allOK := e.rerunRange(ctx, local.Steps, order, targetPos, pos, input, outputs, resolved, backoff)
					for i, item := range rerun {
						run.Steps[targetPos+i] = item
					}
					if allOK {
						last := rerun[len(rerun)-1]
						previous = last.Output
						havePrevious = true
						continue
					}
					// 回滚后仍失败：沿用当前步骤的 on_failure 决策。
					result = rerun[len(rerun)-1]
				}
			}
		}

		run.Status = StatusFailed
		if step.OnFailure != OnFailureContinue {
			stopped = true
		}
	}
	return run, nil
}

// stepIndexByName 按（裁剪后的）名称查找步骤声明位置；未找到返回 -1。
func stepIndexByName(steps []Step, name string) int {
	target := strings.TrimSpace(name)
	for i, step := range steps {
		if strings.TrimSpace(step.Name) == target {
			return i
		}
	}
	return -1
}

// orderPosition 返回步骤声明索引在执行顺序中的位置；未找到返回 -1。
func orderPosition(order []int, stepIndex int) int {
	for i, idx := range order {
		if idx == stepIndex {
			return i
		}
	}
	return -1
}

// rerunRange 重新执行 order[from..to] 对应的步骤一次，返回替换后的结果与是否全部成功。
// 成功的步骤输出会被就地写入 outputs，使后续步骤看到回滚后的新值。
func (e *Executor) rerunRange(ctx context.Context, steps []Step, order []int, from, to int,
	input string, outputs, params map[string]string, backoff time.Duration) ([]StepResult, bool) {
	results := make([]StepResult, 0, to-from+1)
	for k := from; k <= to; k++ {
		step := steps[order[k]]
		result := e.runStep(ctx, step, input, outputs, params, backoff)
		if result.Status == StatusOK {
			outputs[step.Name] = result.Output
		}
		results = append(results, result)
		if result.Status != StatusOK {
			return results, false
		}
	}
	return results, true
}
