package app

import (
	"context"
	"encoding/json"
	"strings"

	"example.com/argentgo/internal/security"
)

// 本文件提供一个可选的"模型守卫"检查器。它实现 security.Inspector，把一次调用交给
// 调用方注入的廉价模型审阅函数，并把审阅结果折叠成 security 的检查判定。
//
// 它刻意不包含任何真实模型调用：审阅函数是纯注入的接缝，构造后即可用假的审阅函数
// 做零 I/O 的表驱动测试。它也不接入 inspectionGuard / Chain.Check —— 接线由未来的
// 组合点自行决定，当前零调用方。
//
// 安全约定：除"明确由审阅函数返回 Allow"之外的所有路径都失败即拒绝为
// RequireApproval，绝不返回 Allow。这样审阅器缺失、报错、context 取消/超时、
// 返回未知动作等异常都不会悄悄放宽已有的 策略 -> 审批 -> 审计 链路。

const (
	// guardianInspectorName 是判定中固定携带的检查器名，便于日志定位。
	guardianInspectorName = "guardian"
	// guardianEnvKey 是启用守卫的环境变量键；只有值恰为 "1" 才开启。
	guardianEnvKey = "ARGENT_GUARDIAN"
)

// guardianReviewFunc 是由调用方注入的廉价模型审阅函数。它只读工具名与参数，
// 不接收审批器，也不能绕过 security 的既有链路。
//
// 注意：任务描述中的 security.InspectVerdict 在本仓库中并不存在；security.Inspector
// 的 Inspect 实际返回 security.Verdict，审阅函数因此沿用同一类型，确保判定可以
// 原样映射回检查器接口。
type guardianReviewFunc func(ctx context.Context, toolName, args string) (security.Verdict, error)

// guardianInspector 实现 security.Inspector。env 由调用方注入，便于测试隔离，
// 与 app 包既有的 inspectionEnabled 保持同样的环境注入风格。
type guardianInspector struct {
	review guardianReviewFunc
	env    func(string) string
}

// newGuardianInspector 用审阅函数与环境读取器构建守卫检查器。
// 两者都可以是 nil：nil 审阅器被视为不可用（失败即拒绝），nil env 被视为未启用。
func newGuardianInspector(review guardianReviewFunc, env func(string) string) *guardianInspector {
	return &guardianInspector{review: review, env: env}
}

// Name 满足 security.Inspector；它必须廉价且无副作用。
func (g *guardianInspector) Name() string { return guardianInspectorName }

// Inspect 满足 security.Inspector。判定顺序刻意从最便宜、最安全的分支开始：
//
//  1. 环境门未开启 -> Allow（守卫退化为"没有额外关卡"，链式策略与审计照常运行）；
//  2. context 已取消或超时 -> RequireApproval（绝不因审阅被打断而放行）；
//  3. 只读工具 -> Allow（无需付费审阅）；
//  4. 审阅器缺失、报错或返回未知动作 -> RequireApproval；
//  5. 审阅器明确返回 Allow / RequireApproval / Deny -> 原样映射对应动作。
//
// 本方法永不返回非 nil error：所有异常都被折叠为明确的 RequireApproval 判定，
// 以免被 InspectionManager 当作软失败跳过时丢失原因。
func (g *guardianInspector) Inspect(ctx context.Context, tool string, args json.RawMessage) (security.Verdict, error) {
	if !guardianEnabled(g.env) {
		return guardianVerdict(security.InspectAllow, "guardian disabled"), nil
	}
	if err := ctx.Err(); err != nil {
		return guardianVerdict(security.InspectRequireApproval, "guardian review interrupted: context done"), nil
	}
	if guardianReadOnlyTool(tool) {
		return guardianVerdict(security.InspectAllow, "read-only tool"), nil
	}
	if g.review == nil {
		return guardianVerdict(security.InspectRequireApproval, "guardian reviewer unavailable"), nil
	}

	verdict, err := g.review(ctx, tool, string(args))
	if err != nil {
		return guardianVerdict(security.InspectRequireApproval, "guardian review failed"), nil
	}
	// 审阅函数返回后再次检查 context：超时后产出的判定一律不予采信。
	if err := ctx.Err(); err != nil {
		return guardianVerdict(security.InspectRequireApproval, "guardian review expired"), nil
	}

	switch verdict.Action {
	case security.InspectAllow:
		return guardianVerdict(security.InspectAllow, "guardian allowed"), nil
	case security.InspectRequireApproval:
		return guardianVerdict(security.InspectRequireApproval, "guardian required approval"), nil
	case security.InspectDeny:
		return guardianVerdict(security.InspectDeny, "guardian denied"), nil
	default:
		// 未知动作不能被当作 Allow：否则有缺陷的审阅器会悄悄降级决策。
		return guardianVerdict(security.InspectRequireApproval, "guardian returned unknown action"), nil
	}
}

// guardianEnabled 报告守卫是否开启。与 inspectionEnabled 一致：只有
// ARGENT_GUARDIAN=1 才开启；未设置、空值或任何其他值都保持关闭。
func guardianEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env(guardianEnvKey)) == "1"
}

// guardianReadOnlyTool 报告工具是否为只读工具。它复用 security 只读沙箱的工具
// 清单（通过 IsMutatingAllowed 查询），避免两处名单漂移；未知工具一律返回 false，
// 从而进入审阅而不是被静默放行。
func guardianReadOnlyTool(tool string) bool {
	return security.SandboxReadOnly.IsMutatingAllowed(strings.ToLower(strings.TrimSpace(tool)))
}

// guardianVerdict 构造一条确定性的检查判定。Reason 固定为内部文案，绝不把审阅
// 模型的原始输出带入判定，避免任何模型文本进入日志、提示或审计记录。
func guardianVerdict(action security.InspectionAction, reason string) security.Verdict {
	return security.Verdict{
		Action:     action,
		Reason:     reason,
		Confidence: 1,
		Inspector:  guardianInspectorName,
	}
}
