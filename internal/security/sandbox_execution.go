package security

// 本文件在既有沙箱模式（SandboxMode）之外，增加"每次调用的沙箱执行描述"。
// 它把沙箱从单一的"模式"维度升级为多维度视图：本次调用实际生效的执行强度、
// 是否以及因何被拒绝、异常时是否以失败即拒绝收尾，以及一份可供审计/遥测使用的
// 上报结构。思想借鉴自"沙箱维度升级"：不再只用一个二值开关描述沙箱，而是显式
// 建模 Enforcement、Denial、FailClosed 三个正交维度。
//
// 这里全部是纯数据与纯函数：不访问全局状态、不做 I/O、不产生副作用。它不接入
// Chain.Check，也不会改变策略 → 审批 → 审计的既有路径；零值描述表示"本次调用
// 没有额外的沙箱执行信息"，对现有行为完全无影响。调用方按需 opt-in 使用。

// SandboxEnforcement 描述一次调用中沙箱实际生效的执行强度。
// 它把"是否处于沙箱"升级为两个正交的级别：full 表示全部沙箱维度都按配置
// 强制执行；partial 表示仅部分维度生效（例如限制了写入根目录但未约束网络）。
type SandboxEnforcement string

const (
	// SandboxEnforcementFull 表示全部沙箱维度均按配置强制执行。
	SandboxEnforcementFull SandboxEnforcement = "full"
	// SandboxEnforcementPartial 表示只有部分沙箱维度生效，存在被降级的维度。
	SandboxEnforcementPartial SandboxEnforcement = "partial"
)

// String 返回稳定的小写字符串表示，适合直接写入遥测或审计下游。
func (e SandboxEnforcement) String() string { return string(e) }

// IsFull 报告执行强度是否为完整。零值或未知取值一律视为非完整，
// 保持失败即拒绝的保守口径：只有确证的 full 才被当作完整执行。
func (e SandboxEnforcement) IsFull() bool { return e == SandboxEnforcementFull }

// ClassifySandboxEnforcement 把两个可观测信号归约为执行强度。
//
// 它是纯函数：不读写状态，对同一输入始终返回相同结果。
//   - active 为 false（沙箱未参与本次调用）时返回零值，从而不影响未启用沙箱的
//     现有路径；
//   - active 且 degraded 为 true（任一维度被降级/绕过）时返回 partial；
//   - active 且未降级时返回 full。
func ClassifySandboxEnforcement(active, degraded bool) SandboxEnforcement {
	if !active {
		return ""
	}
	if degraded {
		return SandboxEnforcementPartial
	}
	return SandboxEnforcementFull
}

// SandboxDenial 是一次沙箱调用的拒绝分类。取值稳定、与语言无关，
// 可直接用于遥测聚合；文件系统/网络/权限三类与沙箱违规分类器共用同一口径，
// 避免两处对"拒绝类别"的理解产生偏差。
type SandboxDenial string

const (
	// SandboxDenialNone 是零值：本次调用未发生拒绝，不影响现有路径。
	SandboxDenialNone SandboxDenial = ""
	// SandboxDenialFileSystem 表示文件系统类拒绝（只读文件系统、策略拦截等）。
	SandboxDenialFileSystem SandboxDenial = sandboxViolationFileSystem
	// SandboxDenialNetwork 表示网络类拒绝（出网、代理、DNS 等被拦截）。
	SandboxDenialNetwork SandboxDenial = sandboxViolationNetwork
	// SandboxDenialPermission 表示权限类拒绝（EACCES / permission denied）。
	SandboxDenialPermission SandboxDenial = sandboxViolationPermission
	// SandboxDenialUnknown 表示发生了拒绝但无法归入已知类别。
	SandboxDenialUnknown SandboxDenial = "unknown"
)

// String 返回稳定的字符串表示。零值映射为 "none"，
// 使上报结构里不会出现含义不明的空字符串。
func (d SandboxDenial) String() string {
	if d == SandboxDenialNone {
		return "none"
	}
	return string(d)
}

// Denied 报告该分类是否表示发生了拒绝。零值以外的所有取值
// （包括 unknown）都视为发生了拒绝，保持失败即拒绝的保守口径。
func (d SandboxDenial) Denied() bool { return d != SandboxDenialNone }

// ClassifySandboxDenial 把违规分类器产出的 kind 字符串归一为拒绝分类。
//
// 它是纯函数：空串表示没有违规，返回零值；已知三类原样映射；其余
// （包括 classifier 返回的空值之外的任何未知词）一律归为 unknown，
// 使无法识别的拒绝不会被静默当成"无拒绝"。
func ClassifySandboxDenial(kind string) SandboxDenial {
	switch SandboxDenial(kind) {
	case SandboxDenialNone, SandboxDenialFileSystem, SandboxDenialNetwork, SandboxDenialPermission:
		return SandboxDenial(kind)
	default:
		return SandboxDenialUnknown
	}
}

// ClassifySandboxFailClosed 判定一次调用是否以失败即拒绝（fail-closed）收尾。
//
// 它是纯函数，不读取任何状态，只依据传入的三维信号：
//   - unresolved 表示存在无法判定或异常的信号；
//   - 有未决信号时：只有确实发生了拒绝（denial.Denied()）才算 fail-closed；
//     未决却放行属于失败即通过（fail-open），返回 false。
//   - 无未决信号时：完整执行（full）本身即为 fail-closed；部分执行（partial）
//     或零值（沙箱未参与）不构成 fail-closed 保证，返回 false。
func ClassifySandboxFailClosed(enforcement SandboxEnforcement, denial SandboxDenial, unresolved bool) bool {
	if unresolved {
		return denial.Denied()
	}
	return enforcement.IsFull()
}

// SandboxExecution 是一次工具调用的沙箱执行描述。
// 三个字段分别对应执行强度、拒绝分类与失败即拒绝标志；
// 零值表示"本次调用没有沙箱执行信息"，对现有路径无影响。
type SandboxExecution struct {
	Enforcement SandboxEnforcement
	Denial      SandboxDenial
	FailClosed  bool
}

// SandboxExecutionReport 是沙箱执行描述的上报结构。所有取值都是稳定的
// 小写字符串或布尔值，适合直接写入审计日志或遥测下游做聚合；
// 字面值不是对外的文本承诺，调用方不应解析其拼写。
type SandboxExecutionReport struct {
	Enforcement string
	Denial      string
	FailClosed  bool
}

// Report 把执行描述转换为可上报结构。它只做取值映射，
// 不改变任何字段的语义，也不产生副作用。
func (e SandboxExecution) Report() SandboxExecutionReport {
	return SandboxExecutionReport{
		Enforcement: e.Enforcement.String(),
		Denial:      e.Denial.String(),
		FailClosed:  e.FailClosed,
	}
}

// DescribeSandboxExecution 把可观测信号归约为完整的沙箱执行描述。
//
// 它是纯函数，组合了上面的三个分类函数：
//   - active / degraded 决定执行强度；
//   - violationKind 决定拒绝分类（可直接传入 classifySandboxViolation 的 kind）；
//   - unresolved 参与 fail-closed 判定。
func DescribeSandboxExecution(active, degraded, unresolved bool, violationKind string) SandboxExecution {
	enforcement := ClassifySandboxEnforcement(active, degraded)
	denial := ClassifySandboxDenial(violationKind)
	return SandboxExecution{
		Enforcement: enforcement,
		Denial:      denial,
		FailClosed:  ClassifySandboxFailClosed(enforcement, denial, unresolved),
	}
}
