package workflow

import (
	"fmt"
	"strings"
)

// validator 只读检查的三态。
const (
	// ValidatorPass 表示 validator 声明合法，执行器会读取并执行它。
	ValidatorPass = "pass"
	// ValidatorFail 表示 validator 已声明却不会被执行（或在声明校验下会给出可读错误）。
	ValidatorFail = "fail"
	// ValidatorMissing 表示步骤没有声明该类 validator。
	ValidatorMissing = "missing"
)

// validator 类别。
const (
	// ValidatorSuccessCheck 是主动作成功后运行的客观 shell 检查。
	ValidatorSuccessCheck = "success_check"
	// ValidatorPrecondition 是步骤执行前校验的前置条件（Guard 的一部分）。
	ValidatorPrecondition = "precondition"
)

// ValidatorCheck 是单个步骤某一类 validator 的只读检查结果。
//
// Step 是裁剪后的步骤名，CJK 名称原样保留；State 为 ValidatorFail 时
// Detail 给出中文可读原因，可直接用于 CI 诊断。
type ValidatorCheck struct {
	Step   string
	Kind   string
	State  string
	Detail string
}

// CheckStepValidators 只读检查工作流计划中每个步骤声明的 validator 是否真的会被
// CI/执行器读取，回答"声明的 validator 在执行时会生效吗"。
//
// 缺口背景：执行器在 attempt() 中仅在 success_check.command 非空白时运行该检查，
// 而 Definition.Validate()/ParseDefinition() 都不检查 validator 声明本身。于是
// "success_check":{"command":"   "} 能通过解析、能被 CLI 列出，却在运行时没有任何
// 校验——这正是"validator 定义了但执行时不校验"。同理，guard.preconditions 里
// 空 key / 未知 op 只在 RunGuarded（显式 opt-in）路径被 validateGuards 拒绝，
// 默认 Run/RunWithParams 与 ParseDefinition 都会静默放过。
//
// 三态：
//   - ValidatorMissing：步骤没有声明该类 validator；
//   - ValidatorPass：声明合法，执行器会读取并执行；
//   - ValidatorFail：声明了却不会被执行（Detail 给出可读原因）。
//
// 每个步骤返回两条结果（success_check 与 precondition），顺序与步骤声明一致。
// 本函数是纯只读原语：不修改 def、不运行命令、不参与 Run/RunGuarded 执行路径。
// 零调用方，默认关闭，因此现有行为逐字节不变。
func CheckStepValidators(def *Definition) []ValidatorCheck {
	if def == nil {
		return nil
	}
	checks := make([]ValidatorCheck, 0, len(def.Steps)*2)
	for _, step := range def.Steps {
		name := strings.TrimSpace(step.Name)
		checks = append(checks, checkSuccessCheck(name, step.SuccessCheck))
		checks = append(checks, checkPreconditions(name, step.Guard))
	}
	return checks
}

// checkSuccessCheck 返回 success_check 的三态结果。空白 command 在执行时会被
// attempt() 视为"未配置"，因此记为 ValidatorFail 而不是 ValidatorMissing。
func checkSuccessCheck(name string, check *StepCheck) ValidatorCheck {
	result := ValidatorCheck{Step: name, Kind: ValidatorSuccessCheck}
	switch {
	case check == nil:
		result.State = ValidatorMissing
	case strings.TrimSpace(check.Command) == "":
		result.State = ValidatorFail
		result.Detail = fmt.Sprintf("步骤 %q 声明了 success_check，但 command 为空白，执行器不会校验它", name)
	default:
		result.State = ValidatorPass
	}
	return result
}

// checkPreconditions 返回 guard.preconditions 的三态结果。判定标准与
// validateGuards 一致，但以三态与中文可读原因呈现，便于 CI 在解析阶段发现
// 声明了却不会被执行的前置条件。
func checkPreconditions(name string, guard *StepGuard) ValidatorCheck {
	result := ValidatorCheck{Step: name, Kind: ValidatorPrecondition}
	if guard == nil || len(guard.Preconditions) == 0 {
		result.State = ValidatorMissing
		return result
	}
	for i, p := range guard.Preconditions {
		if strings.TrimSpace(p.Key) == "" {
			result.State = ValidatorFail
			result.Detail = fmt.Sprintf("步骤 %q 的第 %d 条 precondition 缺少 key，不会被执行前校验", name, i+1)
			return result
		}
		switch p.op() {
		case CondEquals, CondNotEquals, CondContains, CondNotContains, CondExists, CondAbsent:
		default:
			result.State = ValidatorFail
			result.Detail = fmt.Sprintf("步骤 %q 的第 %d 条 precondition 的 op %q 未知，不会被执行前校验", name, i+1, p.op())
			return result
		}
	}
	result.State = ValidatorPass
	return result
}
