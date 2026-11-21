package tools

import (
	"context"
	"encoding/json"
)

// PolicyOnlyGuard 是一个"仅策略"守卫：它只执行注入的策略拒绝判定，
// 不做审批、不写审计。用途是给推测（预跑）注册表装一层轻量守卫，
// 让被策略拒绝的路径在预跑阶段就被拦截（不越权读取），同时不会
// 重复触发审批与审计。
//
// tools 包不能 import internal/security，因此策略判定由调用方（app）
// 用 security.Chain 中的策略部分构造成谓词后注入。谓词为 nil 时
// Check 恒返回 nil，即默认不接入任何策略。该类型当前零调用方、
// 默认不接入任何注册表，仅作为可供 opt-in 的原语存在。
type PolicyOnlyGuard struct {
	policy func(name string, args json.RawMessage) error
}

// 确保 PolicyOnlyGuard 满足 Guard 接口。
var _ Guard = (*PolicyOnlyGuard)(nil)

// NewPolicyOnlyGuard 构造一个仅策略守卫。
// policy 为 nil 时，返回的守卫对所有调用放行。
func NewPolicyOnlyGuard(policy func(string, json.RawMessage) error) *PolicyOnlyGuard {
	return &PolicyOnlyGuard{policy: policy}
}

// Check 把入参原样转发给注入的策略谓词：不修改 name/args，也不做
// 审批或审计。谓词为 nil 时直接放行。ctx 仅用于满足 Guard 接口，
// 不参与判定（策略谓词的签名刻意不含 context）。
func (g *PolicyOnlyGuard) Check(_ context.Context, name string, args json.RawMessage) error {
	if g.policy == nil {
		return nil
	}
	return g.policy(name, args)
}
