package app

import (
	"context"
	"strings"

	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/security"
	"example.com/argentgo/internal/tools"
)

// 本文件把第 5 轮产出的 guardianInspector 接进 app 的检查门。接线刻意保持"骨架 +
// 注入缝"：默认的审阅函数是一个 fail-closed 占位实现（永远返回 RequireApproval），
// 真正的廉价模型审阅尚未接入；测试与未来的接入点只需替换传入的 review 参数即可。
//
// 安全约定：
//   - ARGENT_INSPECTION 是总开关。它关闭时返回裸链，Guardian 绝不生效——这是
//     两开关的"与"关系，由 inspectionGuardFor 保证。
//   - Guardian 自身还要求 ARGENT_GUARDIAN=1、次要模型非空、审阅函数非 nil，
//     三个条件缺一即不注册（guardianInspectorFor 返回 nil）。
//   - 默认审阅函数永不返回 Allow，避免审阅逻辑尚未完成时放宽既有链路。

// defaultGuardianReview 是未接入真实审阅模型时的安全默认：任何调用都返回
// RequireApproval，绝不返回 Allow。这样即使守卫被开启，也不会悄悄放宽
// 策略 -> 审批 -> 审计 链路。
func defaultGuardianReview(_ context.Context, _, _ string) (security.Verdict, error) {
	return guardianVerdict(security.InspectRequireApproval, "guardian reviewer not configured"), nil
}

// guardianInspectorFor 按接线条件构造守卫检查器。三个条件必须同时满足——
// 环境门开启、次要模型非空、审阅函数可用——否则返回 nil（不注册守卫）。
// review 是注入缝：测试可以传入计数/定值的假审阅函数，生产走 defaultGuardianReview。
func guardianInspectorFor(env func(string) string, secondaryModel string, review guardianReviewFunc) security.Inspector {
	if !guardianEnabled(env) || strings.TrimSpace(secondaryModel) == "" || review == nil {
		return nil
	}
	return newGuardianInspector(review, env)
}

// inspectionGuardFor 按两级开关构造检查门。ARGENT_INSPECTION 是总开关：
// 未开启时直接返回裸链，Guardian 永远不会生效（两开关的"与"关系）。
// 总开关开启时才构建 newInspectionGuard，并在其中按需追加 Guardian 检查器。
func inspectionGuardFor(chain *security.Chain, approver security.Approver, stdio cli.IO, secondaryModel string, review guardianReviewFunc) tools.Guard {
	if !inspectionEnabled(stdio.Env) {
		return chain
	}
	return newInspectionGuard(chain, approver, stdio, secondaryModel, review)
}
